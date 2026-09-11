package dispatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProvider replays scripted responses, so contract enforcement can be tested
// without spending model calls — and deterministically, which a real model
// cannot be.
type fakeProvider struct {
	responses []string
	calls     int
	prompts   []string
}

func (f *fakeProvider) Chat(msgs []Message) (string, error) {
	for _, m := range msgs {
		if m.Role == "user" {
			f.prompts = append(f.prompts, m.Content)
		}
	}
	if f.calls >= len(f.responses) {
		return "", fmt.Errorf("no scripted response for call %d", f.calls+1)
	}
	r := f.responses[f.calls]
	f.calls++
	return r, nil
}

func oneFilePlan() *Plan {
	return &Plan{
		Target: "orchestrator", Root: "server", Build: "go build ./...",
		Files: []PlannedFile{{
			Path: "main.go", Purpose: "Entry point",
			Declares: []string{"func main"},
			Serves:   []string{"GET /v1/health"},
		}},
	}
}

func TestProseIsRejectedAndRetried(t *testing.T) {
	// The failure that produced "AI returned no code files" after 45 minutes,
	// now caught on the first response and named.
	good := "package main\n\nfunc main() { http.HandleFunc(\"/v1/health\", nil) }\n"
	p := &fakeProvider{responses: []string{
		"Here is the main.go file you requested:\n\n" + good,
		good,
	}}
	root := t.TempDir()
	var retried string
	_, err := GenerateTarget(p, oneFilePlan(), "go", nil, nil, "platform", root, func(pr Progress) {
		if pr.Status == "retrying" {
			retried = pr.Detail
		}
	}, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("generation failed: %v", err)
	}
	if p.calls != 2 {
		t.Errorf("made %d calls, want 2 (one rejected, one accepted)", p.calls)
	}
	if !strings.Contains(retried, "prose") {
		t.Errorf("retry did not name the reason: %q", retried)
	}
	got, _ := os.ReadFile(filepath.Join(root, "server", "main.go"))
	if !strings.Contains(string(got), "func main") {
		t.Errorf("wrong content written: %q", got)
	}
}

func TestMissingRequiredSymbolIsRejected(t *testing.T) {
	// Structural checking before build: a file that compiles but omits what the
	// manifest requires would otherwise surface as a conformance failure much
	// later.
	p := &fakeProvider{responses: []string{
		"package main\n// no main function, no health route\n",
		"package main\n// still wrong\n",
		"package main\n// wrong a third time\n",
	}}
	_, err := GenerateTarget(p, oneFilePlan(), "go", nil, nil, "platform", t.TempDir(), nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("a file missing its required symbol was accepted")
	}
	if !strings.Contains(err.Error(), "main.go") {
		t.Errorf("failure does not name the file: %v", err)
	}
	if p.calls != maxFileAttempts {
		t.Errorf("made %d attempts, want %d", p.calls, maxFileAttempts)
	}
}

func TestFencedOutputIsAccepted(t *testing.T) {
	// Models fence code by reflex. Rejecting an otherwise-correct response over
	// formatting would fail for the wrong reason.
	body := "package main\n\nfunc main() { _ = \"/v1/health\" }\n"
	p := &fakeProvider{responses: []string{"```go\n" + body + "```"}}
	root := t.TempDir()
	if _, err := GenerateTarget(p, oneFilePlan(), "go", nil, nil, "platform", root, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("fenced output was rejected: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "server", "main.go"))
	if strings.Contains(string(got), "```") {
		t.Error("the fence was written into the file")
	}
}

func TestNothingIsWrittenUntilEveryFileSucceeds(t *testing.T) {
	// A half-written target looks like a build to fix rather than a run to
	// repeat.
	target := &Plan{
		Target: "orchestrator", Root: "server", Build: "go build",
		Files: []PlannedFile{
			{Path: "a.go", Purpose: "first", Declares: []string{"AAA"}},
			{Path: "b.go", Purpose: "second", Declares: []string{"BBB"}},
		},
	}
	p := &fakeProvider{responses: []string{
		"package main\n// AAA\n",
		"package main\n// wrong\n", "package main\n// wrong\n", "package main\n// wrong\n",
	}}
	root := t.TempDir()
	if _, err := GenerateTarget(p, target, "go", nil, nil, "platform", root, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("generation reported success despite a failed file", nil)
	}
	if _, err := os.Stat(filepath.Join(root, "server", "a.go")); err == nil {
		t.Error("the successful file was written even though the target failed")
	}
}

func TestEachFileIsToldWhatAlreadyExists(t *testing.T) {
	// Without this, every file redeclares the shared types and nothing compiles.
	target := &Plan{
		Target: "orchestrator", Root: "server", Build: "go build",
		Files: []PlannedFile{
			{Path: "protocol.go", Purpose: "types"},
			{Path: "main.go", Purpose: "entry", DependsOn: []string{"protocol.go"}},
		},
	}
	p := &fakeProvider{responses: []string{"package main\n", "package main\n"}}
	if _, err := GenerateTarget(p, target, "go", nil, nil, "platform", t.TempDir(), nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err, nil)
	}
	if len(p.prompts) != 2 {
		t.Fatalf("got %d prompts", len(p.prompts))
	}
	if strings.Contains(p.prompts[0], "Already generated") {
		t.Error("the first file was told about files that did not exist yet")
	}
	if !strings.Contains(p.prompts[1], "protocol.go") {
		t.Error("the second file was not told protocol.go already exists")
	}
}

func TestProgressReportsEveryFile(t *testing.T) {
	p := &fakeProvider{responses: []string{"package main\n\nfunc main() { _ = \"/v1/health\" }\n"}}
	var steps []string
	if _, err := GenerateTarget(p, oneFilePlan(), "go", nil, nil, "platform", t.TempDir(), func(pr Progress) {
		steps = append(steps, pr.Status)
	}, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	// Order matters between generating and written; what follows them does not —
	// a cache summary is emitted after the last file.
	var gen, wrote int = -1, -1
	for i, s := range steps {
		if s == "generating" && gen < 0 {
			gen = i
		}
		if s == "written" {
			wrote = i
		}
	}
	if gen < 0 || wrote < 0 || wrote < gen {
		t.Errorf("progress did not report generating then written: %v", steps)
	}
}

// TestFrontmatterIsRejectedAsNotSource reproduces a real failure: one file came
// back as a YAML document whose prose happened to contain the required symbol
// names, so every other check passed and the file was written.
func TestFrontmatterIsRejectedAsNotSource(t *testing.T) {
	frontmatter := "---\nname: weblisk-events-file\ndescription: Notes on generating events. " +
		"It declares PublishSystemEvent and BroadcastDirectory.\n---\n"
	good := "package main\n\nfunc PublishSystemEvent() {}\nfunc BroadcastDirectory() {}\n"
	p := &fakeProvider{responses: []string{frontmatter, good}}
	plan := &Plan{Root: "server", Files: []PlannedFile{{
		Path: "events.go", Purpose: "events",
		Declares: []string{"PublishSystemEvent", "BroadcastDirectory"},
	}}}
	var retried string
	root := t.TempDir()
	if _, err := GenerateTarget(p, plan, "go", nil, nil, "platform", root, func(pr Progress) {
		if pr.Status == "retrying" {
			retried = pr.Detail
		}
	}, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("generation failed: %v", err)
	}
	if !strings.Contains(retried, "frontmatter") {
		t.Errorf("the rejection did not name the reason: %q", retried)
	}
	got, _ := os.ReadFile(filepath.Join(root, "server", "events.go"))
	if strings.HasPrefix(string(got), "---") {
		t.Error("a YAML document was written as Go source")
	}
}

func TestNonGoContentIsRejectedForAGoPath(t *testing.T) {
	p := &fakeProvider{responses: []string{
		"// just a comment, no package clause\nfunc main() {}\n",
		"package main\n\nfunc main() {}\n",
	}}
	plan := &Plan{Root: "server", Files: []PlannedFile{{Path: "main.go", Purpose: "entry"}}}
	if _, err := GenerateTarget(p, plan, "go", nil, nil, "platform", t.TempDir(), nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("valid Go on retry was rejected: %v", err)
	}
	if p.calls != 2 {
		t.Errorf("made %d calls, want 2 — the packageless response should be rejected", p.calls)
	}
}

func TestAGoFileMayOpenWithADocComment(t *testing.T) {
	// The shape check must not reject a file that legitimately opens with a
	// comment block, which generated files routinely do.
	body := "// Package main implements the orchestrator.\n//\n// Long notes.\npackage main\n\nfunc main() {}\n"
	p := &fakeProvider{responses: []string{body}}
	plan := &Plan{Root: "server", Files: []PlannedFile{{Path: "main.go", Purpose: "entry"}}}
	if _, err := GenerateTarget(p, plan, "go", nil, nil, "platform", t.TempDir(), nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("a doc-commented file was rejected: %v", err)
	}
	if p.calls != 1 {
		t.Errorf("made %d calls; a valid file should be accepted first time", p.calls)
	}
}

// TestMethodNotationIsMatchedByParsing reproduces the run that failed three
// times on correct code: a plan declares "(ScopeLevel).Valid" and Go source
// writes "func (s ScopeLevel) Valid() bool", which shares no substring with it.
func TestMethodNotationIsMatchedByParsing(t *testing.T) {
	src := `package main

type ScopeLevel int

func (s ScopeLevel) Valid() bool { return true }

func NewThing() *ScopeLevel { return nil }
`
	f := PlannedFile{Path: "protocol.go", Declares: []string{"(ScopeLevel).Valid", "ScopeLevel", "NewThing"}}
	if v := contractViolation(src, f, nil); v != "" {
		t.Errorf("correct source was rejected: %s", v)
	}
}

func TestAMethodOnTheWrongTypeIsNotAccepted(t *testing.T) {
	// A bare-name match would accept this, and it is a different promise.
	src := "package main\n\ntype Other int\n\nfunc (o Other) Valid() bool { return true }\n"
	f := PlannedFile{Path: "protocol.go", Declares: []string{"(ScopeLevel).Valid"}}
	if v := contractViolation(src, f, nil); v == "" {
		t.Error("a method declared on the wrong type satisfied the contract")
	}
}

func TestAGenuinelyMissingSymbolIsStillCaught(t *testing.T) {
	src := "package main\n\ntype ScopeLevel int\n"
	f := PlannedFile{Path: "protocol.go", Declares: []string{"(ScopeLevel).Valid"}}
	v := contractViolation(src, f, nil)
	if v == "" {
		t.Fatal("a missing method was accepted")
	}
	if !strings.Contains(v, "ScopeLevel") {
		t.Errorf("the complaint does not name the symbol: %s", v)
	}
}

func TestFuncPrefixNotationIsTolerated(t *testing.T) {
	// Plans sometimes write "func main" rather than "main".
	src := "package main\n\nfunc main() {}\n"
	if v := contractViolation(src, PlannedFile{Path: "main.go", Declares: []string{"func main"}}, nil); v != "" {
		t.Errorf("correct source was rejected: %s", v)
	}
}

// TestAStructFieldSatisfiesADeclaration reproduces the run that died on correct
// code: the plan named startedAt, which the model wrote as a field of the
// Orchestrator struct, and a parse-only check cannot see fields.
func TestAStructFieldSatisfiesADeclaration(t *testing.T) {
	src := `package main

import "time"

type Orchestrator struct {
	startedAt time.Time
	port      int
}

func NewOrchestrator() *Orchestrator { return &Orchestrator{startedAt: time.Now()} }
`
	f := PlannedFile{Path: "orchestrator.go", Declares: []string{"Orchestrator", "startedAt", "NewOrchestrator"}}
	if v := contractViolation(src, f, nil); v != "" {
		t.Errorf("correct source was rejected: %s", v)
	}
}

func TestAGenuinelyAbsentSymbolIsStillRejected(t *testing.T) {
	// Permissive is not absent: a symbol nowhere in the file must still fail.
	src := "package main\n\ntype Orchestrator struct{ port int }\n"
	f := PlannedFile{Path: "orchestrator.go", Declares: []string{"Orchestrator", "startedAt"}}
	v := contractViolation(src, f, nil)
	if v == "" {
		t.Fatal("a symbol absent from the file was accepted")
	}
	if !strings.Contains(v, "startedAt") {
		t.Errorf("the complaint does not name the symbol: %s", v)
	}
}

func TestMethodNotationStillRequiresTheRightReceiver(t *testing.T) {
	// The permissive fallback must not undo the receiver check for methods that
	// ARE top-level declarations.
	src := "package main\n\ntype Other int\n\nfunc (o Other) Valid() bool { return true }\n"
	f := PlannedFile{Path: "x.go", Declares: []string{"(ScopeLevel).Valid"}}
	if v := contractViolation(src, f, nil); v == "" {
		t.Error("a method on the wrong type was accepted")
	}
}

func TestTheInvariantPrefixIsIdenticalAcrossFiles(t *testing.T) {
	// Prompt caching works on a PREFIX. The per-file ask used to come first, so
	// the very first bytes differed on every call and nothing was cacheable —
	// twenty-seven files each reprocessed the same ~52,000 tokens of
	// specification.
	//
	// This asserts the shape that makes caching possible: everything invariant
	// comes first, byte for byte, and only the tail varies.
	plan := &Plan{Root: "server", Files: []PlannedFile{
		{Path: "a.go", Purpose: "first", Declares: []string{"Alpha"}},
		{Path: "b.go", Purpose: "second", Declares: []string{"Beta"}},
	}}
	bps := map[string]string{"protocol/types.md": strings.Repeat("TYPE SPECIFICATION\n", 200)}
	order := []string{"protocol/types.md"}
	checklist := []ChecklistItem{{Source: "protocol/types.md", Text: "every type round-trips"}}
	bindings := []Binding{{From: "protocol/types", Type: "Alpha", FieldsUsed: []string{"id"}}}

	a := filePrompt(plan.Files[0], plan, "go", bps, order, "PLATFORM GUIDE", nil, nil, checklist, bindings, nil)
	b := filePrompt(plan.Files[1], plan, "go", bps, order, "PLATFORM GUIDE", nil, nil, checklist, bindings, nil)

	// Find how much of the two prompts is byte-identical from the start.
	shared := 0
	for shared < len(a) && shared < len(b) && a[shared] == b[shared] {
		shared++
	}
	// The specification is the bulk of the prompt; nearly all of it must be
	// shared, or the ordering has regressed.
	//
	// 0.9 is deliberately loose HERE, because this fixture's corpus is a few
	// kilobytes and its ownership block is a large fraction of it. Against the
	// real corpus the measured figure is 99.81% — see
	// TestTheRealPromptIsAlmostEntirelyASharedPrefix, which is the one that
	// would notice a regression. A threshold that a real prompt beats by 10
	// points is not a guard.
	if ratio := float64(shared) / float64(len(a)); ratio < 0.9 {
		t.Errorf("only %.0f%% of the prompt is a shared prefix (%d of %d bytes) — "+
			"the invariant block is no longer first and caching is defeated",
			ratio*100, shared, len(a))
	}
	// And the ask must still be present, at the end, or the model has no task.
	if !strings.Contains(a, "Generate exactly one file: a.go") {
		t.Error("the file-specific ask is missing")
	}
	if strings.Index(a, "--- YOUR TASK ---") < strings.Index(a, "--- BLUEPRINTS ---") {
		t.Error("the ask precedes the specification — the prefix is not invariant")
	}
	// Nothing may be lost by reordering: every element still appears.
	for _, want := range []string{"TYPE SPECIFICATION", "PLATFORM GUIDE", "every type round-trips", "Alpha", "It MUST define"} {
		if !strings.Contains(a, want) {
			t.Errorf("reordering dropped %q from the prompt", want)
		}
	}
}

func TestAccumulatedDeclarationsStayInTheVariableTail(t *testing.T) {
	// The already-generated block grows with each file, so it cannot sit in the
	// cacheable prefix — putting it there would break the prefix on file two.
	plan := &Plan{Root: "server", Files: []PlannedFile{{Path: "b.go", Purpose: "second"}}}
	bps := map[string]string{"x.md": "SPEC"}
	decls := map[string][]Declaration{"a.go": {{Name: "Alpha", Signature: "type Alpha struct{}", Package: "main"}}}

	p := filePrompt(plan.Files[0], plan, "go", bps, []string{"x.md"}, "PLAT",
		[]string{"a.go"}, decls, nil, nil, nil)
	if strings.Index(p, "ALREADY EXIST") < strings.Index(p, "--- BLUEPRINTS ---") {
		t.Error("accumulated declarations precede the specification — the prefix breaks on every file")
	}
	if !strings.Contains(p, "Alpha") {
		t.Error("accumulated declarations were dropped")
	}
}

// The exact sequence that discarded a clean 31-file build.
//
// The plan assigned EncodePublicKey to sign.go. keys.go was generated first and
// declared it anyway; nothing objected. sign.go was then generated correctly
// WITHOUT it and was REJECTED, because its plan entry says it must define
// EncodePublicKey. The retry complied, both files declared it, and the
// coherence check failed after all 31 files were written.
//
// The per-file guard produced the collision the final check could not see until
// half an hour later.
func TestAFileIsRejectedForDeclaringAnotherFilesSymbol(t *testing.T) {
	plan := &Plan{Target: "orchestrator", Root: ".", Files: []PlannedFile{
		{Path: "internal/identity/keys.go", Declares: []string{"GenerateKeyPair"}},
		{Path: "internal/identity/sign.go", Declares: []string{"EncodePublicKey", "Sign"}},
	}}
	owner := claimedSymbols(plan, map[string][]Declaration{})

	// keys.go reaching for a symbol the plan gave to sign.go.
	overreaching := `package identity

func GenerateKeyPair() error { return nil }

func EncodePublicKey(b []byte) string { return "" }
`
	v := redeclaresElsewhere(overreaching, plan.Files[0], owner)
	if v == "" {
		t.Fatal("keys.go declared sign.go's symbol and was accepted")
	}
	if !strings.Contains(v, "sign.go") {
		t.Errorf("the complaint does not name the owning file: %s", v)
	}

	// The same file staying inside its contract is accepted.
	correct := `package identity

func GenerateKeyPair() error { return nil }
`
	if v := redeclaresElsewhere(correct, plan.Files[0], owner); v != "" {
		t.Errorf("a compliant file was rejected: %s", v)
	}

	// And sign.go declaring its OWN planned symbol is fine — the check must not
	// reject the file the plan assigned it to.
	signFile := `package identity

func EncodePublicKey(b []byte) string { return "" }

func Sign(b []byte) []byte { return nil }
`
	if v := redeclaresElsewhere(signFile, plan.Files[1], owner); v != "" {
		t.Errorf("the owning file was rejected for declaring its own symbol: %s", v)
	}
}

// Two methods with the same name on different types are not a collision, and
// rejecting them would refuse correct Go.
func TestSameMethodNameOnDifferentTypesIsNotACollision(t *testing.T) {
	plan := &Plan{Target: "orchestrator", Root: ".", Files: []PlannedFile{
		{Path: "internal/orchestrator/a.go", Declares: []string{"Alpha", "(*Alpha).String"}},
		{Path: "internal/orchestrator/b.go", Declares: []string{"Beta", "(*Beta).String"}},
	}}
	owner := claimedSymbols(plan, map[string][]Declaration{})
	body := `package orchestrator

type Beta struct{}

func (b *Beta) String() string { return "" }
`
	if v := redeclaresElsewhere(body, plan.Files[1], owner); v != "" {
		t.Fatalf("String on a second type was called a collision: %s", v)
	}
}

// A symbol nobody planned still cannot be declared twice — Go forbids it.
func TestAnUnplannedHelperIsOwnedByWhoeverWroteItFirst(t *testing.T) {
	plan := &Plan{Target: "orchestrator", Root: ".", Files: []PlannedFile{
		{Path: "internal/orchestrator/a.go"},
		{Path: "internal/orchestrator/b.go"},
	}}
	written := map[string][]Declaration{
		"internal/orchestrator/a.go": ExtractDeclarations("internal/orchestrator/a.go",
			"package orchestrator\n\nfunc helper() {}\n"),
	}
	owner := claimedSymbols(plan, written)
	body := "package orchestrator\n\nfunc helper() {}\n"
	if v := redeclaresElsewhere(body, plan.Files[1], owner); v == "" {
		t.Fatal("an unplanned helper was declared in two files and accepted")
	}
}

// A file satisfies its serves contract through the DECLARED OPERATION, not only
// through a literal path.
//
// The check required the literal, and platforms/go forbids exactly that: "a
// path literal MUST NOT appear at a registration site". So a correct handler
// referencing protocol.PathOperatorRegister was rejected —
//
//	must serve POST /v1/admin/operators/register, and
//	/v1/admin/operators/register does not appear
//
// — costing a model call on every handler file, and pushing the retry toward
// satisfying the check by breaking the convention.
func TestTheDeclaredOperationSatisfiesTheServesContract(t *testing.T) {
	f := PlannedFile{
		Path:   "internal/orchestrator/handlers_admin_operators.go",
		Serves: []string{"POST /v1/admin/operators/register"},
	}
	ops := map[string]string{"POST /v1/admin/operators/register": "OperatorRegister"}

	// The convention-compliant file: constants, no literal anywhere.
	compliant := `package orchestrator

func (s *Server) handleOperatorRegister(w http.ResponseWriter, r *http.Request) {}

var _ = protocol.PathOperatorRegister
`
	if v := contractViolation(compliant, f, ops); v != "" {
		t.Fatalf("a convention-compliant handler was rejected: %s", v)
	}

	// A literal still satisfies it — the check asks whether the endpoint was
	// implemented, not how. Whether the convention was followed is a separate
	// conformance assertion.
	literal := "package orchestrator\n\n// serves /v1/admin/operators/register\n"
	if v := contractViolation(literal, f, ops); v != "" {
		t.Errorf("a literal path was rejected: %s", v)
	}

	// A file that implements neither is still caught, and the complaint names
	// both spellings it looked for.
	neither := "package orchestrator\n\nfunc unrelated() {}\n"
	v := contractViolation(neither, f, ops)
	if v == "" {
		t.Fatal("a file serving nothing was accepted")
	}
	if !strings.Contains(v, "OperatorRegister") {
		t.Errorf("the complaint does not name the declared operation: %s", v)
	}
}

// A file is told which symbols its siblings own, BEFORE any of them is written.
//
// The prompt carried what previously-written files declared, which is
// order-dependent and incomplete: a file generated before registry.go cannot be
// told registry.go owns DomainDegraded, so it declares it and costs a model
// call to repair. It is also what makes generating a dependency level
// concurrently safe — with this, no file depends on the order its siblings were
// produced in.
func TestAFileIsToldWhichSymbolsItsSiblingsOwn(t *testing.T) {
	plan := &Plan{Target: "orchestrator", Root: ".", Files: []PlannedFile{
		{Path: "internal/orchestrator/registry.go", Declares: []string{"Registry", "DomainDegraded"}},
		{Path: "internal/orchestrator/events.go", Declares: []string{"Publisher"}},
		{Path: "internal/protocol/types.go", Declares: []string{"Agent"}}, // another package
	}}
	self := plan.Files[1]

	ow := formatOwnership(plan, self)
	if !strings.Contains(ow, "DomainDegraded") {
		t.Fatalf("the sibling's symbols were not stated:\n%s", ow)
	}
	if !strings.Contains(ow, "internal/orchestrator/registry.go") {
		t.Error("the owning FILE is not named, so the model can only avoid the symbol, not import it")
	}
	// Its own symbols must not be listed as somebody else's.
	if strings.Contains(ow, "Publisher") {
		t.Error("the file's own symbols were listed as owned elsewhere")
	}
	// Another package cannot collide, so listing it would be noise in a prompt
	// that is already ~94k tokens.
	if strings.Contains(ow, "internal/protocol/types.go") {
		t.Error("a different package was listed; it is reached by import and cannot collide")
	}
	// And it must say what to do about an unplanned helper, which is the case
	// that produced the second retry.
	if !strings.Contains(ow, "unexported") {
		t.Error("the prompt does not say how to name a helper the plan did not assign")
	}
}

// A single-file package has no siblings, and an empty section must not be
// added to the prompt.
func TestOwnershipIsAbsentWhenThereAreNoSiblings(t *testing.T) {
	plan := &Plan{Files: []PlannedFile{{Path: "cmd/orchestrator/main.go", Declares: []string{"main"}}}}
	if ow := formatOwnership(plan, plan.Files[0]); ow != "" {
		t.Errorf("an empty ownership section was added: %q", ow)
	}
}

// The prompt must tell the model how a blueprint is READ, not only send it.
//
// architecture/generation's prompt contract element 10: a generator that sends
// a specification without saying how it is structured has asked the model to
// infer the document's own rules from the document, and it infers differently
// each time.
//
// The machine-readable parts are extracted and elevated — types as bindings,
// endpoints as obligations, the checklist as acceptance criteria. A normative
// sentence in a body paragraph reaches the model as one line in seventy
// thousand tokens of context, and "the audit log MUST be chained" appears in no
// table at all.
func TestTheSystemPromptSaysHowToReadABlueprint(t *testing.T) {
	for _, required := range []string{
		"yaml block with a root key IS THE CONTRACT",
		"HEADER NAME",
		"MUST, MUST NOT OR SHALL IS BINDING WHEREVER IT APPEARS",
		"IN FULL before writing",
		"the more specific wins",
	} {
		if !strings.Contains(fileSystemPrompt, required) {
			t.Errorf("the system prompt does not convey %q", required)
		}
	}
	// And what is NOT binding, or an illustration is implemented as a
	// requirement.
	for _, required := range []string{"is an example", "illustrative unless"} {
		if !strings.Contains(fileSystemPrompt, required) {
			t.Errorf("the system prompt does not say what is non-binding: %q", required)
		}
	}
}
