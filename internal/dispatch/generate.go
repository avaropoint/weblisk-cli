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
func contractViolation(content string, f ManifestFile) string {
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
	for _, sym := range f.MustDefine {
		if !strings.Contains(content, sym) {
			return fmt.Sprintf("%q must define %s, which does not appear", f.Path, sym)
		}
	}
	for _, ep := range f.MustServe {
		// Match on the path; the method may be expressed many ways in a router.
		parts := strings.Fields(ep)
		route := parts[len(parts)-1]
		if !strings.Contains(content, route) {
			return fmt.Sprintf("%q must serve %s, and %s does not appear", f.Path, ep, route)
		}
	}
	return ""
}

// filePrompt asks for exactly one file.
func filePrompt(f ManifestFile, target *ManifestTarget, platform string, specs, platBP string, written []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Generate exactly one file: %s\n\n", f.Path)
	fmt.Fprintf(&b, "Purpose: %s\n", f.Purpose)
	if len(f.MustDefine) > 0 {
		fmt.Fprintf(&b, "It MUST define: %s\n", strings.Join(f.MustDefine, ", "))
	}
	if len(f.MustServe) > 0 {
		fmt.Fprintf(&b, "It MUST serve these endpoints: %s\n", strings.Join(f.MustServe, ", "))
	}
	fmt.Fprintf(&b, "\nPlatform: %s\nTarget directory: %s\n", platform, target.Root)
	if len(written) > 0 {
		// Files already generated, so the model does not redefine what exists.
		fmt.Fprintf(&b, "\nAlready generated in this package (do NOT redeclare their symbols): %s\n",
			strings.Join(written, ", "))
	}
	fmt.Fprintf(&b, "\nThe complete file set for this target is: ")
	for i, mf := range target.Files {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(mf.Path)
	}
	b.WriteString("\n\n--- PLATFORM BLUEPRINT ---\n")
	b.WriteString(platBP)
	b.WriteString("\n\n--- PROTOCOL AND ARCHITECTURE BLUEPRINTS ---\n")
	b.WriteString(specs)
	return b.String()
}

const fileSystemPrompt = `You generate one source file at a time for the Weblisk framework.

Output rules, which are absolute:
- Output ONLY the contents of the requested file.
- No explanation, no commentary, no preamble, no summary.
- No markdown code fences.
- The first character of your response is the first character of the file.
- Use ONLY the target language's standard library unless the blueprint names a dependency.
- Follow the blueprints exactly. Where they specify a name, shape or status code, use it.`

// GenerateTarget generates every file in a manifest target.
//
// Files are written only after ALL of them generate successfully. A half-written
// target is worse than none: it looks like a build to fix rather than a run to
// repeat.
func GenerateTarget(provider Provider, target *ManifestTarget, platform, specs, platBP, root string, onProgress ProgressFunc) error {
	if onProgress == nil {
		onProgress = func(Progress) {}
	}
	generated := make([]GeneratedFile, 0, len(target.Files))
	written := make([]string, 0, len(target.Files))

	for i, f := range target.Files {
		var content string
		var lastViolation string

		for attempt := 1; attempt <= maxFileAttempts; attempt++ {
			status := "generating"
			if attempt > 1 {
				status = "retrying"
			}
			onProgress(Progress{Step: i + 1, Total: len(target.Files), Path: f.Path,
				Status: status, Attempt: attempt, Detail: lastViolation})

			prompt := filePrompt(f, target, platform, specs, platBP, written)
			if lastViolation != "" {
				prompt = "Your previous response was rejected: " + lastViolation +
					"\nProduce the file again, correctly.\n\n" + prompt
			}
			raw, err := provider.Chat([]Message{
				{Role: "system", Content: fileSystemPrompt},
				{Role: "user", Content: prompt},
			})
			if err != nil {
				onProgress(Progress{Step: i + 1, Total: len(target.Files), Path: f.Path,
					Status: "failed", Attempt: attempt, Detail: err.Error()})
				return fmt.Errorf("generating %s: %w", f.Path, err)
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
			onProgress(Progress{Step: i + 1, Total: len(target.Files), Path: f.Path,
				Status: "failed", Attempt: maxFileAttempts, Detail: lastViolation})
			return fmt.Errorf("%s could not be generated in %d attempts: %s",
				f.Path, maxFileAttempts, lastViolation)
		}

		generated = append(generated, GeneratedFile{Path: f.Path, Content: content, Lang: inferLang(f.Path)})
		written = append(written, f.Path)
		onProgress(Progress{Step: i + 1, Total: len(target.Files), Path: f.Path, Status: "written"})
	}

	targetDir := filepath.Join(root, target.Root)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	_, err := writeGeneratedFiles(targetDir, generated)
	return err
}
