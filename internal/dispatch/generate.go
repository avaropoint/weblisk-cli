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
	//
	// Permissive by design, and that changed once Layer 2 existed. This check is
	// a fast pre-filter, not the authority — the COMPILER is. A false rejection
	// costs three regenerations and kills the run; a false acceptance is caught
	// by the build minutes later. Strictness here rejected a struct field the
	// plan had named, because a field is not a top-level declaration, and the
	// run died over correct code.
	declared := ExtractDeclarations(f.Path, content)
	for _, sym := range f.Declares {
		parsedOK := len(declared) > 0 && missingFrom(declared, []string{sym}) == ""
		if parsedOK {
			continue
		}
		// Method notation states a receiver, so it is unambiguous about intent and
		// must be verified by parsing. Falling back to a substring here would
		// accept Valid() declared on the wrong type — a different promise from the
		// one the plan made.
		if reMethodNotation.MatchString(strings.TrimSpace(sym)) {
			return fmt.Sprintf("%q must define %s, which does not appear", f.Path, sym)
		}
		// A bare identifier is ambiguous: the plan may mean a struct field, which
		// no top-level parse can see.
		if strings.Contains(content, bareSymbol(sym)) {
			continue
		}
		return fmt.Sprintf("%q must define %s, which does not appear", f.Path, sym)
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
		return notGoSource(content)
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

// rePackageClause matches a package clause on its own, after leading comments
// have been stripped.
var rePackageClause = regexp.MustCompile(`^package\s+[A-Za-z_][A-Za-z0-9_]*\s*$`)

// notGoSource reports why content is not a Go file, or "" when it is plausibly one.
//
// # The check this replaces
//
// A multiline-anchored search for `^package name$` finds the clause ANYWHERE in
// the response. That is what a leading doc comment needs, and it is also what let
// this through a check named "is this Go source":
//
//	# Orchestrator
//
//	package main
//
// A model that answered in prose with the code beneath it passed, and the prose
// was written to a .go file.
//
// # Why the rule is not a heuristic
//
// Go permits exactly comments and blank lines before the package clause. So
// walking the head and requiring the first non-comment content to BE the package
// clause cannot reject valid source — which matters, because this is a
// pre-filter and a wrong check is more persuasive than no check.
func notGoSource(content string) string {
	inBlock := false
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if inBlock {
			i := strings.Index(line, "*/")
			if i < 0 {
				continue
			}
			inBlock = false
			line = strings.TrimSpace(line[i+2:])
		}
		// Consume any closed block comments opening on this line, so
		// `/* c */ package main` — legal, if unusual — is read as a package clause.
		for strings.HasPrefix(line, "/*") {
			i := strings.Index(line[2:], "*/")
			if i < 0 {
				inBlock = true
				line = ""
				break
			}
			line = strings.TrimSpace(line[2+i+2:])
		}
		switch {
		case line == "" || inBlock:
			continue
		case strings.HasPrefix(line, "//"):
			continue
		case rePackageClause.MatchString(line):
			return ""
		default:
			return fmt.Sprintf("the response is not Go source — %q precedes any package clause", truncateLine(raw))
		}
	}
	return "the response is not Go source — it has no package clause"
}

func truncateLine(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

// filePrompt asks for exactly one file.
//
// Carries the eight elements architecture/generation.md requires. Element 3 —
// accumulated DECLARATIONS rather than filenames — is the one the first real run
// proved: naming which files exist tells a model nothing about what is in them,
// and 36 of that run's 73 errors were symbols declared twice or called and never
// written.
func filePrompt(f PlannedFile, plan *Plan, platform string, blueprints map[string]string,
	bpOrder []string, platBP string, written []string, decls map[string][]Declaration,
	checklist []ChecklistItem, bindings []Binding, st *TenantState) string {
	var b strings.Builder

	// INVARIANT PREFIX — identical for every file in a run.
	//
	// This block used to be last. The per-file ask was first, so the very first
	// bytes of the prompt differed on every call and the cacheable prefix was
	// nothing. Twenty-seven files each reprocessed the same ~52,000 tokens of
	// specification from scratch.
	//
	// Nothing here is changed in content — the model receives exactly what it
	// received before, in the order that lets a cache work on it. The specific
	// ask moves to the end, where an instruction belongs anyway.
	b.WriteString("--- BLUEPRINTS ---\n")
	b.WriteString(joinBlueprints(relevantBlueprints(f, blueprints), bpOrder))
	b.WriteString("\n\n--- PLATFORM BLUEPRINT ---\n")
	b.WriteString(platBP)
	if c := FormatChecklist(checklist); c != "" {
		b.WriteString("\n\n--- ACCEPTANCE CRITERIA ---\n")
		b.WriteString(c)
	}
	if bd := FormatBindings(bindings); bd != "" {
		b.WriteString("\n" + bd)
	}
	if tp := st.FormatTenantPackages(); tp != "" {
		// In the invariant prefix: identical for every file in the run, so it is
		// paid for once. A file that does not know internal/protocol exports
		// ErrorResponse writes its own, and the duplicate is found by the build
		// rather than by the prompt.
		b.WriteString("\n\n" + tp)
	}
	fmt.Fprintf(&b, "\nPlatform: %s\nTarget directory: %s\n", platform, plan.Root)
	if mod := plan.Module; mod != "" {
		// The module path is a fact every file needs and nothing used to carry.
		//
		// With one flat package there were no import paths, so this could not go
		// wrong. The multi-package layout created the requirement: go.mod is
		// generated first and declares the module, then fifty-two later files
		// each have to name it in every import. In one run go.mod said
		// "module weblisk" and all fifty-two imported "weblisk-server/internal/…"
		// — consistent with each other, and wrong.
		fmt.Fprintf(&b, "Module path: %s\n"+
			"Every import of this project's own packages begins with it, "+
			"and go.mod declares exactly this module.\n", mod)
	}
	b.WriteString("\nThe complete file set for this target is: ")
	for i, pf := range plan.Order() {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(pf.Path)
	}
	b.WriteString("\n")

	// VARIABLE SUFFIX — grows as files are written, then the ask itself.
	if len(written) > 0 {
		fmt.Fprintf(&b, "\nAlready generated in this package: %s\n", strings.Join(written, ", "))
		if d := FormatDeclarations(decls, written); d != "" {
			b.WriteString("\nThese symbols ALREADY EXIST. Do not redeclare them, and call them " +
				"with exactly these signatures:\n")
			b.WriteString(d)
			b.WriteString("\nIf this file needs a helper that is not listed above, declare it HERE " +
				"rather than assuming it exists.\n")
		}
	}

	fmt.Fprintf(&b, "\n--- YOUR TASK ---\nGenerate exactly one file: %s\n\n", f.Path)
	fmt.Fprintf(&b, "Purpose: %s\n", f.Purpose)
	if len(f.Declares) > 0 {
		fmt.Fprintf(&b, "It MUST define: %s\n", strings.Join(f.Declares, ", "))
	}
	if len(f.Serves) > 0 {
		fmt.Fprintf(&b, "It MUST serve these endpoints: %s\n", strings.Join(f.Serves, ", "))
	}
	return b.String()
}

// fileSystemPrompt carries the output contract and nothing else.
//
// # What was removed from it, and why
//
// It used to carry two blocks of policy I had written:
//
//	Use ONLY the target language's standard library unless the blueprint
//	names a dependency.
//
//	Scope, which is a prohibition and not a preference:
//	- Do NOT add configuration, metrics, dashboards, health pages or admin
//	  surfaces that no blueprint specifies.
//	...
//
// No blueprint says either of those things. The first was a dependency policy
// that belongs to the platform blueprint — and it was the same stdlib-only rule
// that turned out to contradict the key-derivation function the protocol
// requires, so I was enforcing a corrected rule's old version from a second place.
//
// The second was worse. architecture/observability requires GET /metrics on every
// component. It was in no blueprint's requires list, so it never reached a prompt
// — and my prohibition told the model not to add metrics. The model was
// instructed not to build a required endpoint and never shown the requirement.
//
// A prompt is where the blueprints speak. Anything in it that no blueprint says
// is the tooling overriding the specification, and 1.5 KB of prohibition
// overrides 200 KB of specification perfectly well. What remains here is only
// what is mechanically unavoidable: the response has to BE the file, because a
// response about the file cannot be written to disk.
const fileSystemPrompt = `You generate one source file at a time for the Weblisk framework.

Output rules, which are absolute:
- Output ONLY the contents of the requested file.
- No explanation, no commentary, no preamble, no summary.
- No markdown code fences.
- No YAML frontmatter. Do NOT begin with --- or any metadata block.
- No document ABOUT the file. The response IS the file.
- The first character of your response is the first character of the file.
  For a Go file that is a comment or the word "package".

The blueprints below are the specification. Follow them exactly: where they
specify a name, shape, status code, dependency or endpoint, use it. Where they
are silent, they are silent — this prompt adds no requirements of its own.`

// GenerateTarget generates every file in a manifest target.
//
// Files are written only after ALL of them generate successfully. A half-written
// target is worse than none: it looks like a build to fix rather than a run to
// repeat.
func GenerateTarget(provider Provider, plan *Plan, platform string, blueprints map[string]string,
	bpOrder []string, platBP, root string, onProgress ProgressFunc,
	checklist []ChecklistItem, bindings []Binding, st *TenantState,
	keep map[string]bool) ([]GeneratedFile, error) {
	cache := NewGenerationCache(root)
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

		// Decided KEEP by the rebuild table. The file on disk is the output —
		// read it so later files still see its declarations, and so the returned
		// set is the whole component rather than only what changed.
		if keep[f.Path] {
			body, rerr := os.ReadFile(filepath.Join(root, plan.Root, f.Path))
			if rerr != nil {
				return nil, fmt.Errorf("keeping %s: %w", f.Path, rerr)
			}
			onProgress(Progress{Step: i + 1, Total: len(ordered), Path: f.Path, Status: "kept"})
			generated = append(generated, GeneratedFile{Path: f.Path, Content: string(body), Lang: inferLang(f.Path)})
			written = append(written, f.Path)
			decls[f.Path] = ExtractDeclarations(f.Path, string(body))
			continue
		}

		// Reuse when every input that produced this file is unchanged: the plan
		// entry, the blueprints it was sent, and the instructions. Regenerating
		// an identical file costs two minutes and produces the same bytes.
		// The prompt IS the key: rendered without accumulated declarations, so it
		// covers every input that shapes this file and nothing that merely
		// precedes it.
		invariant := filePrompt(f, plan, platform, blueprints, bpOrder, platBP, nil, nil, checklist, bindings, st)
		key := cacheKey(f, invariant, fileSystemPrompt)
		if cached := cache.Get(key); cached != "" {
			onProgress(Progress{Step: i + 1, Total: len(ordered), Path: f.Path, Status: "reused"})
			generated = append(generated, GeneratedFile{Path: f.Path, Content: cached, Lang: inferLang(f.Path)})
			written = append(written, f.Path)
			decls[f.Path] = ExtractDeclarations(f.Path, cached)
			continue
		}

		for attempt := 1; attempt <= maxFileAttempts; attempt++ {
			status := "generating"
			if attempt > 1 {
				status = "retrying"
			}
			onProgress(Progress{Step: i + 1, Total: len(ordered), Path: f.Path,
				Status: status, Attempt: attempt, Detail: lastViolation})

			prompt := filePrompt(f, plan, platform, blueprints, bpOrder, platBP, written, decls, checklist, bindings, st)
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

		cache.Put(key, content)
		generated = append(generated, GeneratedFile{Path: f.Path, Content: content, Lang: inferLang(f.Path)})
		written = append(written, f.Path)
		decls[f.Path] = ExtractDeclarations(f.Path, content)
		onProgress(Progress{Step: i + 1, Total: len(ordered), Path: f.Path, Status: "written"})
	}

	if sum := cache.Summary(); sum != "" {
		onProgress(Progress{Path: "cache", Status: "summary", Detail: sum})
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
