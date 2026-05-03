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

	specs, err := LoadBlueprints(root, BlueprintSets["orchestrator"]...)
	if err != nil {
		return fmt.Errorf("loading blueprints: %w", err)
	}

	platBP, err := LoadBlueprint(root, PlatformBlueprint(platform))
	if err != nil {
		return fmt.Errorf("loading platform blueprint: %w", err)
	}

	prompt := buildOrchestratorPrompt(specs, platBP, platform)

	fmt.Println("  Generating orchestrator code...")
	fmt.Printf("  Platform: %s\n", platform)
	fmt.Printf("  Target:   %s/server/\n", root)
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

	targetDir := filepath.Join(root, "server")
	written, err := writeGeneratedFiles(targetDir, files)
	if err != nil {
		return err
	}

	fmt.Printf("  [ok] Generated %d files in server/\n", written)
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

	specs, err := LoadBlueprints(root, BlueprintSets["agent"]...)
	if err != nil {
		return fmt.Errorf("loading blueprints: %w", err)
	}

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

	specs, err := LoadBlueprints(root, BlueprintSets["domain"]...)
	if err != nil {
		return fmt.Errorf("loading blueprints: %w", err)
	}

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

	specs, err := LoadBlueprints(root, BlueprintSets["gateway"]...)
	if err != nil {
		return fmt.Errorf("loading blueprints: %w", err)
	}

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
	if err != nil {
		return nil, fmt.Errorf("AI provider required for code generation\n\n"+
			"  Configure an AI provider:\n"+
			"    WL_AI_PROVIDER=ollama     (local Ollama)\n"+
			"    WL_AI_PROVIDER=openai     (requires WL_AI_KEY)\n"+
			"    WL_AI_PROVIDER=anthropic  (requires WL_AI_KEY)\n\n"+
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
- Identity/crypto (Ed25519 keys, tokens, signing)
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
- Identity/crypto (Ed25519 keys, tokens, signing)
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

	written := 0
	for _, f := range files {
		fullPath := filepath.Join(targetDir, f.Path)

		if dir := filepath.Dir(fullPath); dir != targetDir {
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
