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
	"sort"
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
// claimedSymbols maps a package-qualified symbol to the file that owns it.
//
// From the PLAN first, so a symbol is owned before its file is written, then
// from what has actually been written for anything the plan did not name —
// unexported helpers, mostly, which two files in one Go package still cannot
// both declare.
func claimedSymbols(plan *Plan, decls map[string][]Declaration) map[string]string {
	owner := map[string]string{}
	for _, f := range plan.Files {
		pkg := planPackage(f.Path)
		for _, d := range f.Declares {
			owner[pkg+"."+strings.TrimSpace(d)] = f.Path
			// Plans write a method as "(*Registry).Load"; a parsed declaration
			// keys it as "orchestrator.Registry.Load". Both spellings are
			// recorded so the two meet.
			if m := reMethodNotation.FindStringSubmatch(strings.TrimSpace(d)); m != nil {
				owner[pkg+"."+m[1]+"."+m[2]] = f.Path
			}
		}
	}
	for path, ds := range decls {
		for _, d := range ds {
			if _, planned := owner[d.Key()]; !planned {
				owner[d.Key()] = path
			}
		}
	}
	return owner
}

// redeclaresElsewhere reports a symbol this file declares that belongs to
// another file in the same package.
//
// # Why this is a per-file check and not a final one
//
// It was final: every file was generated, then DuplicateDeclarations reported
// the collision and the whole run was discarded. A clean 31-file build died
// that way after generating all 31 — the last file completed, and then nothing
// was usable.
//
// Worse, the per-file check CAUSED the collision it could not see. The plan
// assigned EncodePublicKey to sign.go. keys.go was generated first and declared
// it anyway. sign.go was then generated correctly WITHOUT it — and
// contractViolation rejected sign.go, because its plan entry says it must
// define EncodePublicKey. The retry complied, and both files declared it.
//
// The guard produced the fault, thirty minutes before the check that could see
// it ran. Asked at the point the file is written, keys.go is rejected instead,
// with the name of the file that owns the symbol, and the run continues.
func redeclaresElsewhere(content string, f PlannedFile, owner map[string]string) string {
	if len(owner) == 0 {
		return ""
	}
	for _, d := range ExtractDeclarations(f.Path, content) {
		who, ok := owner[d.Key()]
		if !ok || who == f.Path {
			continue
		}
		return fmt.Sprintf("declares %s, which belongs to %s — import it, do not re-declare it. "+
			"Two files in one package cannot declare the same symbol", d.Name, who)
	}
	return ""
}

// operations maps "METHOD /path" to the endpoint's declared Operation, so a
// file may satisfy its contract through the declared name rather than a literal
// the platform convention forbids.
func contractViolation(content string, f PlannedFile, operations map[string]string) string {
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
		// Match on the path OR on the endpoint's declared Operation.
		//
		// This required the literal path string, and platforms/go now forbids
		// exactly that: "a path literal MUST NOT appear at a registration
		// site". So a correct handler references protocol.PathOperatorRegister
		// and this rejected it —
		//
		//   must serve POST /v1/admin/operators/register, and
		//   /v1/admin/operators/register does not appear
		//
		// costing a model call on every handler file, and pushing the retry
		// toward satisfying the check by breaking the convention.
		//
		// The Operation is the right thing to look for: it is the declared
		// name, and every spelling the platform mandates contains it —
		// PathOperatorRegister, handleOperatorRegister,
		// OperatorRegisterRequest. Checking for it needs no per-platform
		// knowledge here.
		//
		// Whether the convention was FOLLOWED is a different question, asked by
		// the conformance assertion that forbids path literals. This one asks
		// only whether the endpoint was implemented at all.
		parts := strings.Fields(ep)
		route := parts[len(parts)-1]
		if strings.Contains(content, route) {
			continue
		}
		if op := operations[ep]; op != "" && strings.Contains(content, op) {
			continue
		}
		if operations[ep] != "" {
			return fmt.Sprintf("%q must serve %s — neither %s nor its declared operation %q appears",
				f.Path, ep, route, operations[ep])
		}
		{
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
	//
	// The platform blueprint is written ONCE, here, and excluded from the corpus
	// join below.
	//
	// GenerationRoots makes it a root, so it was in bpOrder AND passed
	// separately as platBP — the same file resolved by the same LoadBlueprint
	// from the same sources, so byte-identical. Every per-file prompt carried it
	// twice: 36,467 bytes of platforms/go.md duplicated across 28 files is a
	// megabyte a run, for nothing.
	//
	// It is the separate section that survives, not the join's copy, because
	// relevantBlueprints can return an empty map for a file that declares and
	// serves nothing (see relevance.go) — the labelled section is the only
	// carrier guaranteed to reach every file. Its header names the file so the
	// source attribution joinBlueprints provides is not lost; checklist items
	// are keyed by that path.
	//
	// Written FIRST so the order the model reads is unchanged. Dropping the
	// join's copy alone would have moved the platform blueprint from first
	// document to last, and relevance.go's own standard for a change like that
	// is to measure its effect on build errors rather than assume it. This way
	// there is nothing to measure: the same documents arrive in the same order,
	// one of them no longer twice.
	platName := PlatformBlueprint(platform)
	fmt.Fprintf(&b, "--- PLATFORM BLUEPRINT (%s) ---\n", platName)
	b.WriteString(platBP)
	b.WriteString("\n\n--- BLUEPRINTS ---\n")
	b.WriteString(joinBlueprints(relevantBlueprints(f, blueprints), without(bpOrder, platName)))
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
	// WHO OWNS WHICH SYMBOL, across the whole plan — in the invariant prefix,
	// because it is the same for every file in the run.
	//
	// The prompt already carries what previously-written files declared. That
	// is order-dependent and incomplete: a file generated before registry.go
	// cannot be told registry.go owns DomainDegraded, so it declares it, gets
	// rejected by the coherence check, and costs a model call to repair. Two
	// such retries appeared in one run.
	//
	// The plan knows every owner before a byte is written. Stating it up front
	// turns a retry into a non-event — and it is what makes generating a
	// dependency level CONCURRENTLY safe, because no file then depends on the
	// order its siblings were produced in.
	if ow := formatOwnership(plan, f); ow != "" {
		b.WriteString("\n\n" + ow)
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
are silent, they are silent — this prompt adds no requirements of its own.

HOW TO READ A BLUEPRINT

Every section has a form, and the form says how it binds:

- A fenced yaml block with a root key IS THE CONTRACT. Every key in it is
  required output. The prose around it explains the block; it does not replace
  or soften it.
- A markdown table IS THE CONTRACT. Every row is required output, and columns
  are identified by their HEADER NAME — a column's position means nothing, and
  a table may carry a column you have not seen before.
- Prose is context, with one exception:
  A SENTENCE CONTAINING MUST, MUST NOT OR SHALL IS BINDING WHEREVER IT APPEARS
  — in a paragraph, a note, a parenthesis or an aside. Many rules are stated
  only that way and appear in no table.

What is NOT binding, so you do not implement an illustration:

- A fenced block with no root key, in a section whose contract is a rooted
  block, is an example.
- A path, name or value inside a narrative example is illustrative unless a
  table or a yaml block also declares it.

Read each blueprint you are given IN FULL before writing. A requirement stated
once, in a sentence, is as binding as one stated in a table — and it is the
kind most often missed.

Where two blueprints disagree, the more specific wins: a component's own
blueprint over a pattern it adopts. If neither is more specific, say so in a
comment rather than choosing — a disagreement resolved silently becomes the
specification.`

// GenerateTarget generates every file in a manifest target.
//
// Files are written only after ALL of them generate successfully. A half-written
// target is worse than none: it looks like a build to fix rather than a run to
// repeat.
func GenerateTarget(provider Provider, plan *Plan, platform string, blueprints map[string]string,
	bpOrder []string, platBP, root string, onProgress ProgressFunc,
	checklist []ChecklistItem, bindings []Binding, st *TenantState,
	keep map[string]bool, endpoints []EndpointOperation) ([]GeneratedFile, error) {
	cache := NewGenerationCache(root)
	if onProgress == nil {
		onProgress = func(Progress) {}
	}
	ordered := plan.Order()
	generated := make([]GeneratedFile, 0, len(ordered))
	written := make([]string, 0, len(ordered))
	decls := map[string][]Declaration{}
	// Who owns which symbol, from the plan. Rebuilt as files are written so an
	// unplanned helper also gets an owner.
	owner := claimedSymbols(plan, decls)
	// "METHOD /path" -> declared Operation, so a file may satisfy its serves
	// contract through the declared name instead of a literal the platform
	// convention forbids.
	endpointOps := map[string]string{}
	for _, e := range endpoints {
		if e.Operation != "" {
			endpointOps[e.Wire()] = e.Operation
		}
	}

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
			owner = claimedSymbols(plan, decls)
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
			owner = claimedSymbols(plan, decls)
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
			if v := contractViolation(candidate, f, endpointOps); v != "" {
				lastViolation = v
				continue
			}
			// Coherence, asked here rather than after all files are written.
			if v := redeclaresElsewhere(candidate, f, owner); v != "" {
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
		owner = claimedSymbols(plan, decls)
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

// formatOwnership lists the symbols other files in this plan will declare.
//
// Grouped by file so the instruction is actionable — a model told "X is taken"
// can only avoid it, while one told "X belongs to internal/orchestrator/registry.go"
// can import it.
//
// Only symbols in the SAME package are listed. A symbol in another package is
// reached by import path and cannot collide, so naming it here would be noise
// in a prompt that is already large.
func formatOwnership(plan *Plan, self PlannedFile) string {
	mine := planPackage(self.Path)
	byFile := map[string][]string{}
	var order []string
	for _, f := range plan.Files {
		if f.Path == self.Path || planPackage(f.Path) != mine || len(f.Declares) == 0 {
			continue
		}
		syms := append([]string(nil), f.Declares...)
		sort.Strings(syms)
		byFile[f.Path] = syms
		order = append(order, f.Path)
	}
	if len(order) == 0 {
		return ""
	}
	sort.Strings(order)
	var b strings.Builder
	b.WriteString("--- SYMBOLS OWNED BY OTHER FILES IN THIS PACKAGE ---\n\n")
	b.WriteString("These are declared by the files listed. Do NOT declare them here —\n")
	b.WriteString("two files in one Go package cannot declare the same symbol. Use them\n")
	b.WriteString("directly; they are in your package and need no import.\n\n")
	for _, path := range order {
		fmt.Fprintf(&b, "  %s\n      %s\n", path, wrapList(byFile[path], 68, "      "))
	}
	b.WriteString("\nIf you need a helper that none of these provides and your own plan entry\n")
	b.WriteString("does not name, declare it — but keep it unexported and specific to this\n")
	b.WriteString("file, so a sibling generated at the same time cannot pick the same name.\n")
	return b.String()
}
