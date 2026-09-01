package dispatch

// Loads blueprints, constructs prompts, sends to the user's configured
// AI model, parses the response into code files, and writes them to disk.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GeneratedFile represents a single file extracted from AI output.
type GeneratedFile struct {
	Path    string // relative file path
	Content string // file content
	Lang    string // language (go, js, toml, etc.)
}

// ServerInit generates orchestrator code using the AI model.
func ServerInit(root, platform string) error {
	provider, err := RequireProvider()
	if err != nil {
		return err
	}

	// One resolution, before anything reads it. The plan, the checklist and the
	// per-file prompts all come from THIS graph — see ResolveGraph for what went
	// wrong when they each had their own list.
	graph, err := ResolveGraph(root, "orchestrator", platform)
	if err != nil {
		return fmt.Errorf("resolving blueprints: %w", err)
	}
	specs := graph.Joined()

	platBP, err := LoadBlueprint(root, PlatformBlueprint(platform))
	if err != nil {
		return fmt.Errorf("loading platform blueprint: %w", err)
	}

	// Plan-driven generation. The blueprints state what must exist; the MODEL
	// decides how to arrange it, and the plan is validated against the
	// requirements before a single file is generated. See
	// architecture/generation.md.
	req := GatherRequirements(graph, "orchestrator")
	if len(req.Types) > 0 || len(req.Endpoints) > 0 {
		fmt.Printf("  Platform: %s\n", platform)
		fmt.Print(graph.Describe())
		if len(graph.Missing) > 0 {
			// Stated, never silent. A declared requirement this installation does
			// not carry is a gap in what the model was given, and the output should
			// be read knowing that.
			fmt.Printf("  [warn] declared requirements not found: %s\n", strings.Join(graph.Missing, ", "))
		}
		fmt.Printf("  Required: %s\n", req.Summary())
		if len(req.UnboundTypes) > 0 {
			// Stated, never supplied. A type the protocol defines that this
			// component's bindings do not claim is a gap in the blueprint's
			// contract, and quietly adding it would hide the gap while putting the
			// tooling's judgement back in charge of what a component needs.
			fmt.Printf("  [note] %d types are defined by the protocol and bound by no contract here.\n"+
				"         They are not required of this component. If one is genuinely needed,\n"+
				"         the blueprint's bindings are where that is declared.\n", len(req.UnboundTypes))
		}
		fmt.Println()

		// Reuse the plan when the requirements have not changed. Without this the
		// model re-plans every run — ten files where it planned twelve — and every
		// per-file cache entry is invalidated by a plan entry nobody changed.
		cache := NewGenerationCache(root)
		pk := planKey(req, "orchestrator", platform, platBP, planSystemPrompt)
		plan := cache.GetPlan(pk)
		if plan != nil {
			fmt.Println("  Plan reused — requirements unchanged since the last run")
		} else {
			var perr error
			plan, perr = MakePlan(provider, req, "orchestrator", platform, specs, platBP, printProgress)
			if perr != nil {
				return perr
			}
			cache.PutPlan(pk, plan)
		}
		// The tenant's name is the module path — platforms/go states it as
		// "module <tenant>". Set here rather than asked of the model, because a
		// fact two files must agree on should not be guessed twice.
		plan.Module = moduleNameFor(root)

		fmt.Printf("\n  Plan accepted: %d files in %s/\n", len(plan.Files), plan.Root)
		for _, f := range plan.Order() {
			fmt.Printf("    %s — %s\n", f.Path, f.Purpose)
		}
		fmt.Println()

		// One writer per target. Two generations sharing a directory produced
		// twenty-one redeclaration errors between two correct plans, which reads
		// exactly like a pipeline fault and is not one.
		release, lerr := AcquireTargetLock(root, plan.Root)
		if lerr != nil {
			return lerr
		}
		defer release()

		// The plan is a complete statement of what the target consists of, not an
		// addition to whatever is already there. A previous run that split the
		// registry differently left routing.go beside a new registry.go, and every
		// symbol in it was declared twice — nine correct files and one leftover,
		// producing a build no repair could fix because no file was wrong.
		if rec, rerr := ReconcileTarget(root, plan); rerr != nil {
			return fmt.Errorf("reconciling %s: %w", plan.Root, rerr)
		} else {
			for _, f := range rec.Stale {
				fmt.Printf("  Removed %s — written by a previous run, not in this plan\n", f)
			}
			if len(rec.Foreign) > 0 {
				// Not generation's to remove, and not generation's to hide.
				fmt.Printf("  [note] left in place, not written by generation: %s\n",
					strings.Join(rec.Foreign, ", "))
			}
			if len(rec.Stale) > 0 || len(rec.Foreign) > 0 {
				fmt.Println()
			}
		}

		files, gerr := GenerateTarget(provider, plan, platform, graph.Map, graph.Order, platBP, root,
			printProgress, req.Checklist, req.Bindings)
		if gerr != nil {
			return gerr
		}
		fmt.Printf("\n  [ok] Generated %d files in %s/\n\n", len(files), plan.Root)
		RecordWritten(root, plan, files)

		// Layer 2: build, and feed failures back. Generating blind and reporting
		// success is how eleven files that do not compile get called finished.
		if plan.Build != "" {
			result, repaired, rerr := BuildAndRepair(provider, plan, root, platBP, files, printProgress, req.Checklist, graph.Map,
				func(current []GeneratedFile) ([]ConformanceResult, string, error) {
					bin := builtBinary(root, plan.Build)
					if bin == "" {
						return nil, "", nil
					}
					return RunConformance(root, bin, "orchestrator", printProgress)
				})
			if rerr != nil {
				return rerr
			}
			files = repaired
			if !result.OK {
				reportChecklist(EvaluateChecklistAgainst(req.Checklist, files, graph.Map))
				fmt.Printf("  [failed] the implementation does not build\n\n")
				fmt.Println(indentBlock(result.Output, "    "))
				return fmt.Errorf("build failed: %s", plan.Build)
			}
			fmt.Printf("  [ok] builds with %q\n\n", plan.Build)

			// Layer 4's final word. The loop has already run the component and
			// repaired what it could; this is the report of where it ended.
			if bin := builtBinary(root, plan.Build); bin != "" {
				results, output, cerr := RunConformance(root, bin, "orchestrator", nil)
				reportConformance(results, output, cerr)
			}
		}

		// What the blueprints say, as read by the model — the authority.
		if verdicts, verr := SelfVerify(provider, files, req.Checklist); verr == nil {
			reportVerdicts(verdicts, req.Checklist)
		} else {
			fmt.Printf("  [warn] verification against the assertions could not be read: %v\n\n", verr)
		}
		// What the structural checks think — advice, and useful mainly when it
		// disagrees with the above.
		reportChecklist(EvaluateChecklistAgainst(req.Checklist, files, graph.Map))
		return nil
	}

	prompt := buildOrchestratorPrompt(specs, platBP, platform)

	fmt.Println("  Generating orchestrator code (no manifest for this platform)...")
	fmt.Printf("  Platform: %s\n", platform)
	fmt.Printf("  Target:   %s\n", root)
	fmt.Println()

	response, err := provider.Chat([]Message{
		{Role: "system", Content: orchestratorSystemPrompt},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return fmt.Errorf("AI generation failed: %w", err)
	}

	files := parseGeneratedFiles(response)
	if len(files) == 0 {
		return fmt.Errorf("AI returned no code files — try a different model or check the response")
	}

	// The tenant folder is the target. A component's own directory comes from the
	// plan; this fallback path has no plan, so it writes at the tenant root.
	targetDir := root
	written, err := writeGeneratedFiles(targetDir, files)
	if err != nil {
		return err
	}

	fmt.Printf("  [ok] Generated %d files\n", written)
	for _, f := range files {
		fmt.Printf("    %s\n", f.Path)
	}
	fmt.Println()

	return nil
}

// AgentCreate generates agent code using the AI model.
func AgentCreate(root, name, platform string) error {
	provider, err := RequireProvider()
	if err != nil {
		return err
	}

	graph, err := ResolveGraph(root, "agent", platform)
	if err != nil {
		return fmt.Errorf("resolving blueprints: %w", err)
	}
	specs := graph.Joined()

	platBP, err := LoadBlueprint(root, PlatformBlueprint(platform))
	if err != nil {
		return fmt.Errorf("loading platform blueprint: %w", err)
	}

	domainBP := ""
	if content, err := LoadBlueprint(root, DomainBlueprint(name)); err == nil {
		domainBP = content
	}

	prompt := buildAgentPrompt(specs, platBP, domainBP, name, platform)

	fmt.Printf("  Generating %s agent code...\n", name)
	fmt.Printf("  Platform: %s\n", platform)
	fmt.Printf("  Target:   %s/agents/%s/\n", root, name)
	fmt.Println()

	response, err := provider.Chat([]Message{
		{Role: "system", Content: agentSystemPrompt},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return fmt.Errorf("AI generation failed: %w", err)
	}

	files := parseGeneratedFiles(response)
	if len(files) == 0 {
		return fmt.Errorf("AI returned no code files — try a different model or check the response")
	}

	targetDir := filepath.Join(root, "agents", name)
	written, err := writeGeneratedFiles(targetDir, files)
	if err != nil {
		return err
	}

	fmt.Printf("  [ok] Generated %d files in agents/%s/\n", written, name)
	for _, f := range files {
		fmt.Printf("    %s\n", f.Path)
	}
	fmt.Println()

	return nil
}

// DomainCreate generates a domain controller using the AI model.
func DomainCreate(root, name, platform string) error {
	provider, err := RequireProvider()
	if err != nil {
		return err
	}

	graph, err := ResolveGraph(root, "domain", platform)
	if err != nil {
		return fmt.Errorf("resolving blueprints: %w", err)
	}
	specs := graph.Joined()

	platBP, err := LoadBlueprint(root, PlatformBlueprint(platform))
	if err != nil {
		return fmt.Errorf("loading platform blueprint: %w", err)
	}

	// Try to load domain-specific blueprint (e.g., domains/seo.md)
	domainBP := ""
	if content, err := LoadBlueprint(root, "agents/"+name+".md"); err == nil {
		domainBP = content
	}

	prompt := buildDomainPrompt(specs, platBP, domainBP, name, platform)

	fmt.Printf("  Generating %s domain controller...\n", name)
	fmt.Printf("  Platform: %s\n", platform)
	fmt.Printf("  Target:   %s/domains/%s/\n", root, name)
	fmt.Println()

	response, err := provider.Chat([]Message{
		{Role: "system", Content: domainSystemPrompt},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return fmt.Errorf("AI generation failed: %w", err)
	}

	files := parseGeneratedFiles(response)
	if len(files) == 0 {
		return fmt.Errorf("AI returned no code files — try a different model or check the response")
	}

	targetDir := filepath.Join(root, "domains", name)
	written, err := writeGeneratedFiles(targetDir, files)
	if err != nil {
		return err
	}

	fmt.Printf("  [ok] Generated %d files in domains/%s/\n", written, name)
	for _, f := range files {
		fmt.Printf("    %s\n", f.Path)
	}
	fmt.Println()

	return nil
}

// GatewayCreate generates the application gateway using the AI model.
func GatewayCreate(root, platform string) error {
	provider, err := RequireProvider()
	if err != nil {
		return err
	}

	graph, err := ResolveGraph(root, "gateway", platform)
	if err != nil {
		return fmt.Errorf("resolving blueprints: %w", err)
	}
	specs := graph.Joined()

	platBP, err := LoadBlueprint(root, PlatformBlueprint(platform))
	if err != nil {
		return fmt.Errorf("loading platform blueprint: %w", err)
	}

	prompt := buildGatewayPrompt(specs, platBP, platform)

	fmt.Println("  Generating application gateway...")
	fmt.Printf("  Platform: %s\n", platform)
	fmt.Printf("  Target:   %s/gateway/\n", root)
	fmt.Println()

	response, err := provider.Chat([]Message{
		{Role: "system", Content: gatewaySystemPrompt},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return fmt.Errorf("AI generation failed: %w", err)
	}

	files := parseGeneratedFiles(response)
	if len(files) == 0 {
		return fmt.Errorf("AI returned no code files — try a different model or check the response")
	}

	targetDir := filepath.Join(root, "gateway")
	written, err := writeGeneratedFiles(targetDir, files)
	if err != nil {
		return err
	}

	fmt.Printf("  [ok] Generated %d files in gateway/\n", written)
	for _, f := range files {
		fmt.Printf("    %s\n", f.Path)
	}
	fmt.Println()

	return nil
}

// PatternApply generates a pattern implementation using the AI model.
func PatternApply(root, pattern, resource string) error {
	provider, err := RequireProvider()
	if err != nil {
		return err
	}

	patternBP, err := LoadBlueprint(root, PatternBlueprint(pattern))
	if err != nil {
		return fmt.Errorf("loading pattern blueprint: %w", err)
	}

	prompt := buildPatternPrompt(patternBP, pattern, resource)

	fmt.Printf("  Applying pattern: %s\n", pattern)
	fmt.Printf("  Resource: %s\n", resource)
	fmt.Println()

	response, err := provider.Chat([]Message{
		{Role: "system", Content: patternSystemPrompt},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return fmt.Errorf("AI generation failed: %w", err)
	}

	files := parseGeneratedFiles(response)
	if len(files) == 0 {
		return fmt.Errorf("AI returned no code files — try a different model or check the response")
	}

	written, err := writeGeneratedFiles(root, files)
	if err != nil {
		return err
	}

	fmt.Printf("  [ok] Applied pattern — %d files generated\n", written)
	for _, f := range files {
		fmt.Printf("    %s\n", f.Path)
	}
	fmt.Println()

	return nil
}

// RequireProvider creates and validates an AI provider.
func RequireProvider() (Provider, error) {
	provider, err := NewProvider()
	if err == nil {
		// Every path that talks to a model retries a failure that calls itself
		// temporary. Two runs died on "529 Overloaded … usually temporary — try
		// again in a moment", one of them on file 49 of 49.
		provider = WithTransientRetry(provider)
	}
	if err != nil {
		return nil, fmt.Errorf("AI provider required for code generation\n\n"+
			"  Configure an AI provider:\n"+
			"  No account needed — runs on this machine:\n"+
			"    WL_AI_PROVIDER=claude-code  (Claude Code CLI, uses its own login)\n"+
			"    WL_AI_PROVIDER=ollama       (local Ollama, default http://localhost:11434)\n"+
			"    WL_AI_PROVIDER=local-cli    (any local tool; set WL_AI_COMMAND)\n\n"+
			"  Hosted, requires a key:\n"+
			"    WL_AI_PROVIDER=openai       (requires WL_AI_KEY)\n"+
			"    WL_AI_PROVIDER=anthropic    (requires WL_AI_KEY)\n\n"+
			"  Set in .env or environment: %w", err)
	}

	fmt.Println("  Verifying AI provider...")
	_, testErr := provider.Chat([]Message{
		{Role: "user", Content: "Respond with exactly: ok"},
	})
	if testErr != nil {
		return nil, fmt.Errorf("AI provider not reachable: %w\n\n"+
			"  Check your WL_AI_* configuration", testErr)
	}
	fmt.Println("  [ok] AI provider connected")

	return provider, nil
}

// DiscoverProvider checks for available AI providers and returns info.
func DiscoverProvider() string {
	api := os.Getenv("WL_AI_PROVIDER")
	model := os.Getenv("WL_AI_MODEL")

	if api == "" {
		return "not configured"
	}

	info := api
	if model != "" {
		info += " (" + model + ")"
	}

	provider, err := NewProvider()
	if err != nil {
		return info + " [error: " + err.Error() + "]"
	}

	_, err = provider.Chat([]Message{
		{Role: "user", Content: "Respond with exactly: ok"},
	})
	if err != nil {
		return info + " [unreachable]"
	}

	return info + " [ready]"
}

// ProviderStatus returns a JSON-serializable status of the AI provider.
func ProviderStatus() map[string]any {
	status := map[string]any{
		"provider": os.Getenv("WL_AI_PROVIDER"),
		"model":    os.Getenv("WL_AI_MODEL"),
		"base_url": os.Getenv("WL_AI_BASE_URL"),
		"has_key":  os.Getenv("WL_AI_KEY") != "",
	}

	_, err := NewProvider()
	if err != nil {
		status["status"] = "error"
		status["error"] = err.Error()
	} else {
		status["status"] = "configured"
	}

	return status
}

// PrintProviderStatus shows the current AI provider configuration.
func PrintProviderStatus() {
	s := ProviderStatus()
	data, _ := json.MarshalIndent(s, "  ", "  ")
	fmt.Printf("  AI Provider:\n  %s\n\n", string(data))
}

// Prompt Construction

const orchestratorSystemPrompt = `You are a code generation agent for the Weblisk framework.
You generate complete, working orchestrator server implementations.

Rules:
- Generate ALL required files for a fully working implementation
- Each file must start with a comment: // filename: <path>
- Use ONLY standard library (no external dependencies)
- Follow the protocol specification EXACTLY
- Include all protocol endpoints, auth, registration, audit
- The code must compile and run immediately
- Do NOT explain the code — just output the files

Output format — for each file:
// filename: <relative-path>
<complete file content>

Separate files with a blank line.`

const agentSystemPrompt = `You are a code generation agent for the Weblisk framework.
You generate complete, working agent implementations that follow
the universal Weblisk Agent Protocol.

Rules:
- Generate ALL required files for a fully working agent
- Each file must start with a comment: // filename: <path>
- Use ONLY standard library (no external dependencies)
- Follow the protocol specification EXACTLY
- The agent must implement all 5 protocol endpoints
- Include registration, messaging, and service discovery
- The code must compile and run immediately
- Do NOT explain the code — just output the files

Output format — for each file:
// filename: <relative-path>
<complete file content>

Separate files with a blank line.`

func buildOrchestratorPrompt(specs, platformBP, platform string) string {
	return fmt.Sprintf(`Generate a complete Weblisk orchestrator implementation.

## Platform
%s

## Specification
%s

## Platform-Specific Guidance
%s

Generate all files needed for a working orchestrator. Include:
- Entry point (main.go or index.js depending on platform)
- Protocol types
- Identity/crypto (ML-DSA-65 keys, tokens, signing)
- Orchestrator server (all endpoints from the spec)
- Helper utilities
- Build configuration (go.mod or package.json)

The implementation must pass protocol verification — every endpoint
must respond exactly as specified.`, platform, specs, platformBP)
}

func buildAgentPrompt(specs, platformBP, domainBP, name, platform string) string {
	domainSection := ""
	if domainBP != "" {
		domainSection = fmt.Sprintf("\n\n## Domain Knowledge\n%s", domainBP)
	}

	return fmt.Sprintf(`Generate a complete Weblisk agent implementation.

## Agent Name
%s

## Platform
%s

## Specification
%s

## Platform-Specific Guidance
%s%s

Generate all files needed for a working agent. Include:
- Entry point
- Protocol types (same contract as orchestrator)
- Identity/crypto
- Agent base framework (all 5 protocol endpoints)
- Domain-specific logic (Execute + HandleMessage)
- Build configuration

The agent must register with an orchestrator and handle all protocol
endpoints exactly as specified.`, name, platform, specs, platformBP, domainSection)
}

const domainSystemPrompt = `You are a code generation agent for the Weblisk framework.
You generate complete, working domain controller implementations.

Rules:
- Generate ALL required files for a fully working domain controller
- Each file must start with a comment: // filename: <path>
- Use ONLY standard library (no external dependencies)
- Follow the protocol specification EXACTLY
- Include workflow execution, agent dispatch, aggregation, and scoring
- The code must compile and run immediately
- Do NOT explain the code — just output the files

Output format — for each file:
// filename: <relative-path>
<complete file content>

Separate files with a blank line.`

const gatewaySystemPrompt = `You are a code generation agent for the Weblisk framework.
You generate complete, working application gateway implementations.

Rules:
- Generate ALL required files for a fully working gateway
- Each file must start with a comment: // filename: <path>
- Use ONLY standard library (no external dependencies)
- Include TLS termination, session management, ABAC, rate limiting
- Route requests to domain controllers via the orchestrator
- The code must compile and run immediately
- Do NOT explain the code — just output the files

Output format — for each file:
// filename: <relative-path>
<complete file content>

Separate files with a blank line.`

const patternSystemPrompt = `You are a code generation agent for the Weblisk framework.
You generate implementations of cross-cutting patterns (auth, webhooks,
real-time, etc.) that integrate into existing project code.

Rules:
- Generate files that implement the pattern specification
- Each file must start with a comment: // filename: <path>
- Use ONLY standard library (no external dependencies)
- Follow the pattern specification EXACTLY
- The code must integrate cleanly with the existing project
- Do NOT explain the code — just output the files

Output format — for each file:
// filename: <relative-path>
<complete file content>

Separate files with a blank line.`

func buildDomainPrompt(specs, platformBP, domainBP, name, platform string) string {
	domainSection := ""
	if domainBP != "" {
		domainSection = fmt.Sprintf("\n\n## Domain-Specific Agents\n%s", domainBP)
	}

	return fmt.Sprintf(`Generate a complete Weblisk domain controller implementation.

## Domain Name
%s

## Platform
%s

## Specification
%s

## Platform-Specific Guidance
%s%s

Generate all files needed for a working domain controller. Include:
- Entry point
- Protocol types
- Identity/crypto (ML-DSA-65 keys, tokens, signing)
- Domain controller (workflow execution, agent dispatch, aggregation)
- Scoring and feedback logic
- Registration with orchestrator
- Build configuration (go.mod or package.json)

The domain controller must register with the orchestrator, define
workflows, dispatch to work agents, aggregate results, and drive
the continuous optimization loop.`, name, platform, specs, platformBP, domainSection)
}

func buildGatewayPrompt(specs, platformBP, platform string) string {
	return fmt.Sprintf(`Generate a complete Weblisk application gateway implementation.

## Platform
%s

## Specification
%s

## Platform-Specific Guidance
%s

Generate all files needed for a working application gateway. Include:
- Entry point
- HTTP router with middleware pipeline
- TLS termination configuration
- Session management (secure cookies)
- ABAC authorization (attribute-based access control)
- Rate limiting (per-IP, configurable)
- Route proxying to domain controllers
- Health check endpoint
- Build configuration

The gateway is the public entry point. It authenticates users,
enforces policies, and routes requests to the appropriate domain
controllers via the orchestrator.`, platform, specs, platformBP)
}

func buildPatternPrompt(patternBP, pattern, resource string) string {
	return fmt.Sprintf(`Apply the following pattern to the specified resource.

## Pattern
%s

## Pattern Specification
%s

## Target Resource
%s

Generate all files needed to implement this pattern for the target
resource. Follow the specification exactly — implement all endpoints,
types, and behaviors described.`, pattern, patternBP, resource)
}

// Response Parsing

var reFilenameComment = regexp.MustCompile(`(?m)^//\s*filename:\s*(.+?)\s*$`)
var reCodeBlock = regexp.MustCompile("(?s)```(\\w+)?(?:\\s+(.+?))?\\n(.*?)```")

func parseGeneratedFiles(response string) []GeneratedFile {
	files := parseByFilenameComments(response)
	if len(files) > 0 {
		return files
	}
	return parseByCodeBlocks(response)
}

func parseByFilenameComments(response string) []GeneratedFile {
	matches := reFilenameComment.FindAllStringIndex(response, -1)
	if len(matches) == 0 {
		return nil
	}

	var files []GeneratedFile
	for i, loc := range matches {
		nameMatch := reFilenameComment.FindStringSubmatch(response[loc[0]:loc[1]])
		if len(nameMatch) < 2 {
			continue
		}
		path := strings.TrimSpace(nameMatch[1])

		contentStart := loc[1] + 1
		if contentStart >= len(response) {
			continue
		}
		contentEnd := len(response)
		if i+1 < len(matches) {
			contentEnd = matches[i+1][0]
		}

		content := strings.TrimRight(response[contentStart:contentEnd], "\n ")
		lang := inferLang(path)
		files = append(files, GeneratedFile{Path: path, Content: content, Lang: lang})
	}
	return files
}

func parseByCodeBlocks(response string) []GeneratedFile {
	blockMatches := reCodeBlock.FindAllStringSubmatch(response, -1)
	var files []GeneratedFile
	for _, m := range blockMatches {
		lang := m[1]
		path := strings.TrimSpace(m[2])
		content := m[3]

		if path == "" {
			continue
		}

		files = append(files, GeneratedFile{Path: path, Content: content, Lang: lang})
	}
	return files
}

func inferLang(path string) string {
	switch {
	case strings.HasSuffix(path, ".go"):
		return "go"
	case strings.HasSuffix(path, ".js"):
		return "javascript"
	case strings.HasSuffix(path, ".ts"):
		return "typescript"
	case strings.HasSuffix(path, ".rs"):
		return "rust"
	case strings.HasSuffix(path, ".py"):
		return "python"
	case strings.HasSuffix(path, ".toml"):
		return "toml"
	case strings.HasSuffix(path, ".json"):
		return "json"
	case strings.HasSuffix(path, ".mod"):
		return "go"
	default:
		return ""
	}
}

// File Writing

func writeGeneratedFiles(targetDir string, files []GeneratedFile) (int, error) {
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return 0, fmt.Errorf("creating target directory: %w", err)
	}

	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return 0, err
	}

	// Validate EVERY path before writing ANY file. A generation that tried to
	// escape is not a generation to half-apply — leaving some files written and
	// some refused would present as a partially-generated hub nobody can reason
	// about.
	fullPaths := make([]string, len(files))
	for i, f := range files {
		full, perr := safeGeneratedPath(absTarget, f.Path)
		if perr != nil {
			return 0, perr
		}
		fullPaths[i] = full
	}

	written := 0
	for i, f := range files {
		fullPath := fullPaths[i]
		if serr := withinAfterSymlinks(absTarget, fullPath); serr != nil {
			return written, serr
		}

		if dir := filepath.Dir(fullPath); dir != absTarget {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return written, fmt.Errorf("creating directory for %s: %w", f.Path, err)
			}
		}

		if err := os.WriteFile(fullPath, []byte(f.Content+"\n"), 0644); err != nil {
			return written, fmt.Errorf("writing %s: %w", f.Path, err)
		}
		written++
	}
	return written, nil
}

// printProgress renders generation progress on a terminal.
//
// A retry names the reason. "Retrying main.go" tells somebody nothing; "retrying
// main.go — the response began with prose" tells them whether to change model.
func printProgress(p Progress) {
	switch p.Status {
	case "generating":
		fmt.Printf("  [%d/%d] %s\n", p.Step, p.Total, p.Path)
	case "retrying":
		fmt.Printf("  [%d/%d] %s — retry %d: %s\n", p.Step, p.Total, p.Path, p.Attempt, p.Detail)
	case "written":
		fmt.Printf("  [%d/%d] %s [ok]\n", p.Step, p.Total, p.Path)
	case "failed":
		if p.Step == 0 {
			fmt.Printf("  %s [failed] %s\n", p.Path, p.Detail)
			return
		}
		fmt.Printf("  [%d/%d] %s [failed] %s\n", p.Step, p.Total, p.Path, p.Detail)
	case "planning":
		fmt.Println("  Asking the model to plan the implementation...")
	case "replanning":
		fmt.Printf("  Re-planning — %s\n", p.Detail)
	case "preparing":
		fmt.Println("  Resolving dependencies...")
	case "building":
		fmt.Printf("  Building (round %d)...\n", p.Attempt)
	case "progress":
		fmt.Printf("    %s\n", p.Detail)
	case "built":
		fmt.Println("  Build succeeded")
	case "reused":
		fmt.Printf("  [%d/%d] %s [reused]\n", p.Step, p.Total, p.Path)
	case "summary":
		fmt.Printf("\n  %s\n", p.Detail)
	case "repairing":
		fmt.Printf("    repairing %s — %s\n", p.Path, p.Detail)
	}
}

// reportChecklist prints the structural checks — as ADVICE, not as a verdict.
//
// These no longer drive the loop. The blueprints' assertions are sent to the
// model verbatim and it judges its own output against them, because a check
// written in Go is a transcription of a requirement into a second language and
// ten of them were subtly wrong in one session.
//
// They are still printed, for the one thing they are unambiguously good for:
// disagreeing. A structural check that refutes an assertion the model reported as
// satisfied is worth a human's attention, and resolving that disagreement
// silently in favour of either side is how a build comes to be trusted for the
// wrong reason.
func reportChecklist(results []ChecklistResult) {
	if len(results) == 0 {
		return
	}
	verified, failed, necessary, notApplicable, unchecked := ChecklistCounts(results)
	fmt.Printf("  Structural checks (advisory): %d verified, %d refuted, %d necessary-conditions-hold, %d not-applicable, %d no check\n",
		verified, failed, necessary, notApplicable, unchecked)
	for _, r := range results {
		if r.Outcome == OutcomeFailed {
			fmt.Printf("    [refuted] %s\n              %s\n", r.Item.Text, r.Detail)
		}
	}
	if failed > 0 {
		fmt.Printf("    %d structural check(s) disagree with the specification as read by the model.\n"+
			"    Neither is authoritative here — read both.\n", failed)
	}
	fmt.Println()
}

// reportVerdicts prints what the model found against the assertions.
func reportVerdicts(verdicts []Verdict, checklist []ChecklistItem) {
	if len(verdicts) == 0 {
		return
	}
	yes, no, unverifiable, unanswered := VerdictSummary(verdicts, len(checklist))
	fmt.Printf("  Verification against the blueprints' assertions: %d satisfied, %d unmet, %d unverifiable by reading source, %d unanswered\n",
		yes, no, unverifiable, unanswered)
	for _, v := range Violations(verdicts, checklist) {
		fmt.Printf("    [unmet] %s\n            %s", v.Item.Text, v.Evidence)
		if v.File != "" {
			fmt.Printf(" (%s)", v.File)
		}
		fmt.Println()
	}
	if unverifiable > 0 {
		fmt.Printf("    %d assertions cannot be settled by reading source — behaviour over time,\n"+
			"    across a restart, or under load. They need the conformance suite.\n", unverifiable)
	}
	if unanswered > 0 {
		fmt.Printf("    [warn] %d assertions were not judged at all — the verification is incomplete\n", unanswered)
	}
	fmt.Println()
}

// indentBlock indents every line, so build output is visibly subordinate to the
// message that introduced it.
func indentBlock(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// moduleNameFor is the import-path prefix for a tenant's own packages.
//
// The tenant directory's name, per platforms/go: "module <tenant>". A name Go
// will not accept as a module path is replaced rather than passed through, since
// an unusable module path fails at `go mod tidy` with an error about a file
// nobody wrote.
func moduleNameFor(root string) string {
	name := filepath.Base(filepath.Clean(root))
	if name == "." || name == string(filepath.Separator) || name == "" {
		if wd, err := os.Getwd(); err == nil {
			name = filepath.Base(wd)
		}
	}
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" {
		return "tenant"
	}
	return out
}

// builtBinary reads the output path out of the plan's build command.
//
// `go build -o bin/orchestrator ./cmd/orchestrator` — the -o argument. Taken
// from the command rather than assumed, because the command is the platform
// blueprint's and this should not hold a second opinion about where the binary
// lands.
func builtBinary(root, buildCmd string) string {
	fields := strings.Fields(buildCmd)
	for i, f := range fields {
		if f == "-o" && i+1 < len(fields) {
			return filepath.Join(root, fields[i+1])
		}
	}
	return ""
}

// reportConformance prints what running the component established.
func reportConformance(results []ConformanceResult, output string, err error) {
	if err != nil {
		// It never answered. Its own output is the finding.
		fmt.Printf("  [failed] the component does not run\n           %v\n\n", err)
		if t := strings.TrimSpace(output); t != "" {
			fmt.Println(indentBlock(lastLines(t, 12), "    "))
			fmt.Println()
		}
		return
	}
	passed, failed, unrun := ConformanceSummary(results)
	fmt.Printf("  Conformance L1: %d passed, %d failed, %d unrun\n", passed, failed, unrun)
	for _, r := range results {
		switch {
		case r.Unrun:
			fmt.Printf("    [unrun] %s %s — %s\n", r.ID, r.Name, r.Detail)
		case r.Passed:
			fmt.Printf("    [pass]  %s %s — %s\n", r.ID, r.Name, r.Evidence)
		default:
			fmt.Printf("    [FAIL]  %s %s\n            %s\n", r.ID, r.Name, r.Detail)
		}
	}
	if unrun > 0 {
		fmt.Printf("    %d test(s) have no harness yet — they are NOT passes\n", unrun)
	}
	fmt.Println()
}

// lastLines returns the tail of some output, which is where a startup failure
// says what went wrong.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
