package dispatch

// Manifest-driven generation.
//
// The previous approach asked one model for a whole orchestrator in a single
// call. It failed twice: once on a timeout with nothing to show for forty-five
// minutes, once returning output the parser did not recognise — and neither
// failure said which file was the problem, because there was no notion of files
// until the response was parsed.
//
// This generates one file per call, against the manifest the platform blueprint
// declares. The file set is the blueprint's; a failure names a file; a retry
// costs one file rather than all of them; and progress is real, which a
// graphical caller needs as much as a terminal does.
//
// # The output contract is enforced, not requested
//
// The old prompt ASKED, in prose, for a "// filename:" prefix, and a regex the
// model never saw decided whether it complied. A convention in a paragraph is
// honoured differently by different models — which is exactly the
// non-determinism the manifest exists to remove. Here each call produces ONE
// file, so there is no filename convention to honour, and what comes back is
// checked against what the manifest said that file must contain. A violation is
// retried with the specific complaint, not discovered minutes later as "no code
// files".

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Progress reports one step of a generation run.
type Progress struct {
	Step    int
	Total   int
	Path    string
	Status  string // "generating" | "retrying" | "written" | "failed"
	Attempt int
	Detail  string
}

// ProgressFunc receives progress. Never nil inside the generator.
type ProgressFunc func(Progress)

// maxFileAttempts bounds retries per file. Three is enough for a model that
// drifted from the contract and not enough to spend an afternoon on one that
// cannot meet it.
const maxFileAttempts = 3

var reFence = regexp.MustCompile("(?s)^\\s*```[a-zA-Z0-9]*\\n(.*?)```\\s*$")

// stripFence removes a single wrapping code fence.
//
// Models wrap code in fences by reflex even when asked not to. Refusing that
// output would fail on a response that is otherwise exactly right, so the fence
// is removed rather than rejected — but only when it wraps the WHOLE response,
// because a fence in the middle means prose surrounds it.
func stripFence(s string) string {
	if m := reFence.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return s
}

// contractViolation returns the reason a response is not an acceptable file, or
// "" when it is.
func contractViolation(content string, f PlannedFile) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "the response was empty"
	}
	// Prose before code is the common failure: an explanation, then the file.
	// Checked before symbols so the complaint names the real problem.
	firstLine := strings.TrimSpace(strings.SplitN(trimmed, "\n", 2)[0])
	for _, opener := range []string{"here is", "here's", "sure", "certainly", "i'll", "i will", "below is", "this file"} {
		if strings.HasPrefix(strings.ToLower(firstLine), opener) {
			return "the response began with prose (" + firstLine + ") instead of file content"
		}
	}
	// The response must LOOK like source in the target language. A real run
	// returned YAML frontmatter for one file — "---\nname: ...\ndescription: ..."
	// — and passed the symbol checks below, because the required names appeared
	// in its prose. Checking the shape first stops a document masquerading as
	// code.
	if why := notSourceIn(f.Path, trimmed); why != "" {
		return why
	}
	// Verify declarations by PARSING where a language extractor exists, not by
	// substring. A plan writes a method as "(ScopeLevel).Valid"; Go source writes
	// "func (s ScopeLevel) Valid() bool". Substring matching rejected correct code
	// three times over that notation gap, and the failure looked like the model's.
	if declared := ExtractDeclarations(f.Path, content); len(declared) > 0 {
		if missing := missingFrom(declared, f.Declares); missing != "" {
			return fmt.Sprintf("%q must define %s, which does not appear", f.Path, missing)
		}
	} else {
		for _, sym := range f.Declares {
			if !strings.Contains(content, bareSymbol(sym)) {
				return fmt.Sprintf("%q must define %s, which does not appear", f.Path, sym)
			}
		}
	}
	for _, ep := range f.Serves {
		// Match on the path; the method may be expressed many ways in a router.
		parts := strings.Fields(ep)
		route := parts[len(parts)-1]
		if !strings.Contains(content, route) {
			return fmt.Sprintf("%q must serve %s, and %s does not appear", f.Path, ep, route)
		}
	}
	return ""
}

// reMethodNotation matches how a plan names a method: "(Type).Method".
var reMethodNotation = regexp.MustCompile(`^\(?\*?([A-Za-z_][\w]*)\)?\.([A-Za-z_][\w]*)$`)

// bareSymbol reduces a plan's symbol to the identifier a language would write.
//
// Plans name methods "(ScopeLevel).Valid" and sometimes functions "func main".
// Neither appears literally in source.
func bareSymbol(sym string) string {
	sym = strings.TrimSpace(sym)
	if m := reMethodNotation.FindStringSubmatch(sym); m != nil {
		return m[2]
	}
	return strings.TrimPrefix(sym, "func ")
}

// missingFrom returns the first required symbol absent from the parsed
// declarations, or "" when all are present.
//
// A method is satisfied when some declaration mentions BOTH its receiver type
// and its name — a bare name match would accept Valid() declared on the wrong
// type, which is a different promise.
func missingFrom(declared []Declaration, required []string) string {
	names := map[string]bool{}
	var signatures []string
	for _, d := range declared {
		names[d.Name] = true
		signatures = append(signatures, d.Signature)
	}
	for _, sym := range required {
		if m := reMethodNotation.FindStringSubmatch(strings.TrimSpace(sym)); m != nil {
			recv, method := m[1], m[2]
			found := false
			for _, sig := range signatures {
				if strings.Contains(sig, recv) && strings.Contains(sig, method) {
					found = true
					break
				}
			}
			if !found {
				return sym
			}
			continue
		}
		if !names[bareSymbol(sym)] {
			return sym
		}
	}
	return ""
}

// notSourceIn reports why content is not plausibly source for a path's language.
//
// Deliberately shallow: it looks for the one construct the language cannot omit,
// and says nothing about anything else. A deep check would reject valid code for
// stylistic reasons, which is worse than the failure it prevents.
func notSourceIn(path, content string) string {
	if strings.HasPrefix(content, "---") {
		return "the response began with a YAML frontmatter block, not source"
	}
	switch strings.ToLower(pathExt(path)) {
	case ".go":
		if !reGoPackage.MatchString(content) {
			return "the response is not Go source — it has no package clause"
		}
	case ".mod":
		if !strings.Contains(content, "module ") {
			return "the response is not a go.mod — it has no module directive"
		}
	case ".json":
		if !strings.HasPrefix(content, "{") && !strings.HasPrefix(content, "[") {
			return "the response is not JSON"
		}
	}
	return ""
}

// reGoPackage matches a package clause, allowing leading comments and blank
// lines — a generated file legitimately opens with a doc comment.
var reGoPackage = regexp.MustCompile(`(?m)^package\s+[A-Za-z_][A-Za-z0-9_]*\s*$`)

// filePrompt asks for exactly one file.
//
// Carries the eight elements architecture/generation.md requires. Element 3 —
// accumulated DECLARATIONS rather than filenames — is the one the first real run
// proved: naming which files exist tells a model nothing about what is in them,
// and 36 of that run's 73 errors were symbols declared twice or called and never
// written.
func filePrompt(f PlannedFile, plan *Plan, platform string, blueprints map[string]string,
	bpOrder []string, platBP string, written []string, decls map[string][]Declaration,
	checklist []ChecklistItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Generate exactly one file: %s\n\n", f.Path)
	fmt.Fprintf(&b, "Purpose: %s\n", f.Purpose)
	if len(f.Declares) > 0 {
		fmt.Fprintf(&b, "It MUST define: %s\n", strings.Join(f.Declares, ", "))
	}
	if len(f.Serves) > 0 {
		fmt.Fprintf(&b, "It MUST serve these endpoints: %s\n", strings.Join(f.Serves, ", "))
	}
	fmt.Fprintf(&b, "\nPlatform: %s\nTarget directory: %s\n", platform, plan.Root)
	if len(written) > 0 {
		fmt.Fprintf(&b, "\nAlready generated in this package: %s\n", strings.Join(written, ", "))
		if d := FormatDeclarations(decls, written); d != "" {
			b.WriteString("\nThese symbols ALREADY EXIST. Do not redeclare them, and call them " +
				"exactly as declared:\n")
			b.WriteString(d)
			b.WriteString("\nIf this file needs a helper that is not listed above, declare it HERE " +
				"rather than assuming it exists elsewhere.\n")
		}
	}
	fmt.Fprintf(&b, "\nThe complete file set for this target is: ")
	for i, mf := range plan.Files {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(mf.Path)
	}
	if c := FormatChecklist(checklist); c != "" {
		b.WriteString("\n--- ACCEPTANCE CRITERIA ---\n")
		b.WriteString(c)
	}
	b.WriteString("\n\n--- PLATFORM BLUEPRINT ---\n")
	b.WriteString(platBP)
	// Only the blueprints this file needs. go.mod does not need the ML-DSA
	// specification, and sending it ten times is most of the run's cost.
	b.WriteString(joinBlueprints(relevantBlueprints(f, blueprints), bpOrder))
	return b.String()
}

const fileSystemPrompt = `You generate one source file at a time for the Weblisk framework.

Output rules, which are absolute:
- Output ONLY the contents of the requested file.
- No explanation, no commentary, no preamble, no summary.
- No markdown code fences.
- No YAML frontmatter. Do NOT begin with --- or any metadata block.
- No document ABOUT the file. The response IS the file.
- The first character of your response is the first character of the file.
  For a Go file that is a comment or the word "package".
- Use ONLY the target language's standard library unless the blueprint names a dependency.
- Follow the blueprints exactly. Where they specify a name, shape or status code, use it.

Scope, which is a prohibition and not a preference:
- Implement ONLY what this file was asked for. Nothing else.
- Do NOT add endpoints, routes or handlers beyond those named for this file.
- Do NOT add configuration, metrics, dashboards, health pages or admin surfaces
  that no blueprint specifies.
- Do NOT add dependencies beyond the stated policy.
- A helpful addition nobody asked for is a defect: it is unspecified, unreviewed,
  and in a hub holding a tenant's keys it enlarges the attack surface.`

// GenerateTarget generates every file in a manifest target.
//
// Files are written only after ALL of them generate successfully. A half-written
// target is worse than none: it looks like a build to fix rather than a run to
// repeat.
func GenerateTarget(provider Provider, plan *Plan, platform string, blueprints map[string]string,
	bpOrder []string, platBP, root string, onProgress ProgressFunc,
	checklist []ChecklistItem) ([]GeneratedFile, error) {
	if onProgress == nil {
		onProgress = func(Progress) {}
	}
	ordered := plan.Order()
	generated := make([]GeneratedFile, 0, len(ordered))
	written := make([]string, 0, len(ordered))
	decls := map[string][]Declaration{}

	for i, f := range ordered {
		var content string
		var lastViolation string

		for attempt := 1; attempt <= maxFileAttempts; attempt++ {
			status := "generating"
			if attempt > 1 {
				status = "retrying"
			}
			onProgress(Progress{Step: i + 1, Total: len(ordered), Path: f.Path,
				Status: status, Attempt: attempt, Detail: lastViolation})

			prompt := filePrompt(f, plan, platform, blueprints, bpOrder, platBP, written, decls, checklist)
			if lastViolation != "" {
				prompt = "Your previous response was rejected: " + lastViolation +
					"\nProduce the file again, correctly.\n\n" + prompt
			}
			raw, err := provider.Chat([]Message{
				{Role: "system", Content: fileSystemPrompt},
				{Role: "user", Content: prompt},
			})
			if err != nil {
				onProgress(Progress{Step: i + 1, Total: len(ordered), Path: f.Path,
					Status: "failed", Attempt: attempt, Detail: err.Error()})
				return nil, fmt.Errorf("generating %s: %w", f.Path, err)
			}
			candidate := stripFence(raw)
			if v := contractViolation(candidate, f); v != "" {
				lastViolation = v
				continue
			}
			content = candidate
			break
		}

		if content == "" {
			onProgress(Progress{Step: i + 1, Total: len(ordered), Path: f.Path,
				Status: "failed", Attempt: maxFileAttempts, Detail: lastViolation})
			return nil, fmt.Errorf("%s could not be generated in %d attempts: %s",
				f.Path, maxFileAttempts, lastViolation)
		}

		generated = append(generated, GeneratedFile{Path: f.Path, Content: content, Lang: inferLang(f.Path)})
		written = append(written, f.Path)
		decls[f.Path] = ExtractDeclarations(f.Path, content)
		onProgress(Progress{Step: i + 1, Total: len(ordered), Path: f.Path, Status: "written"})
	}

	// Layer 2, first half: a symbol declared in two files will not compile, and
	// naming it here is far clearer than the compiler's report.
	if dupes := DuplicateDeclarations(decls); len(dupes) > 0 {
		var b strings.Builder
		b.WriteString("coherence: the same symbol is declared in more than one file\n")
		for name, files := range dupes {
			fmt.Fprintf(&b, "  %s — %s\n", name, strings.Join(files, ", "))
		}
		return generated, fmt.Errorf("%s", b.String())
	}

	targetDir := filepath.Join(root, plan.Root)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return generated, err
	}
	if _, err := writeGeneratedFiles(targetDir, generated); err != nil {
		return generated, err
	}
	return generated, nil
}
