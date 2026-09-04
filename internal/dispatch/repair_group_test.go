package dispatch

import (
	"strings"
	"testing"
)

// The error that stalled a real tenant build: an interface in one file, its
// implementer in another.
const (
	storeGo = `package storage

type persister interface {
	load(name string) ([]byte, error)
	save(name string, b []byte) error
}

func open() { var p persister; p = &jsonlPersister{} ; _ = p }
`
	jsonlGo = `package storage

type jsonlPersister struct{ dir string }

func (p *jsonlPersister) load(name string) ([]byte, string, error) { return nil, "", nil }
func (p *jsonlPersister) save(name string, b []byte) error         { return nil }
`
)

func interfaceMismatchFixture() (map[string]string, *Plan) {
	content := map[string]string{
		"internal/storage/store.go": storeGo,
		"internal/storage/jsonl.go": jsonlGo,
	}
	plan := &Plan{Target: "orchestrator", Root: ".", Files: []PlannedFile{
		{Path: "internal/storage/store.go", Purpose: "store contract", Declares: []string{"persister"}},
		{Path: "internal/storage/jsonl.go", Purpose: "jsonl backend", Declares: []string{"jsonlPersister"}},
		{Path: "cmd/orchestrator/main.go", Purpose: "entry point", Declares: []string{"main"}},
	}}
	return content, plan
}

// An unsatisfied interface implicates the interface's file AND the
// implementer's, even though the compiler names only one.
func TestAnUnsatisfiedInterfaceImplicatesBothSides(t *testing.T) {
	content, plan := interfaceMismatchFixture()
	errs := []string{
		"internal/storage/store.go:8:9: cannot use p (variable of type *jsonlPersister) as persister value in assignment: *jsonlPersister does not implement persister (wrong type for method load)",
	}
	got := ImplicatedFiles(errs, content, plan)
	if len(got) != 2 {
		t.Fatalf("implicated %v, want both store.go and jsonl.go", got)
	}
	joined := strings.Join(got, ",")
	for _, want := range []string{"internal/storage/store.go", "internal/storage/jsonl.go"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s was not implicated: %v", want, got)
		}
	}
	if strings.Contains(joined, "main.go") {
		t.Errorf("an unrelated file was dragged in: %v", got)
	}
}

// A redeclaration names both positions and both must change together.
func TestARedeclarationImplicatesBothDeclaringFiles(t *testing.T) {
	content := map[string]string{
		"registry.go": "package p\n\ntype DomainStatus int\n",
		"routing.go":  "package p\n\ntype DomainStatus int\n",
		"main.go":     "package main\n\nfunc main() {}\n",
	}
	plan := &Plan{Root: ".", Files: []PlannedFile{
		{Path: "registry.go"}, {Path: "routing.go"}, {Path: "main.go"},
	}}
	errs := []string{
		"./routing.go:24:6: DomainStatus redeclared in this block",
		"\t./registry.go:16:6: other declaration of DomainStatus",
	}
	got := ImplicatedFiles(errs, content, plan)
	if len(got) != 2 {
		t.Fatalf("implicated %v, want routing.go and registry.go", got)
	}
}

// An undefined symbol implicates the file that owes it, not only the caller.
func TestAnUndefinedSymbolImplicatesItsDefiner(t *testing.T) {
	content := map[string]string{
		"events.go": "package p\n\nfunc publish() { storageAgentFilter() }\n",
		"store.go":  "package p\n",
	}
	plan := &Plan{Root: ".", Files: []PlannedFile{
		{Path: "events.go", Declares: []string{"publish"}},
		{Path: "store.go", Declares: []string{"storageAgentFilter"}},
	}}
	got := ImplicatedFiles([]string{"events.go:3:18: undefined: storageAgentFilter"}, content, plan)
	if len(got) != 2 {
		t.Fatalf("implicated %v — the caller and the file that owes the symbol", got)
	}
}

// A file outside the plan is never implicated: generation may only rewrite what
// it wrote.
func TestAFileOutsideThePlanIsNotImplicated(t *testing.T) {
	content, plan := interfaceMismatchFixture()
	plan.Files = plan.Files[:1] // only store.go is ours now
	got := ImplicatedFiles([]string{
		"internal/storage/store.go:8:9: *jsonlPersister does not implement persister (wrong type for method load)",
	}, content, plan)
	for _, p := range got {
		if p == "internal/storage/jsonl.go" {
			t.Error("a file the plan does not own was scheduled for rewriting")
		}
	}
}

// A single-file error must NOT become a group: grouping everything would send
// the whole target every round.
func TestAnOrdinarySingleFileErrorIsNotGrouped(t *testing.T) {
	content, plan := interfaceMismatchFixture()
	got := ImplicatedFiles([]string{
		"cmd/orchestrator/main.go:9:2: declared and not used: x",
	}, content, plan)
	if len(got) != 1 || got[0] != "cmd/orchestrator/main.go" {
		t.Errorf("implicated %v, want only main.go", got)
	}
}

// The group prompt must ask for every file, in the multi-file format, and carry
// both bodies — knowing what the other file declares was already possible and
// was not enough, because it conferred no authority to change it.
func TestTheGroupPromptAsksForEveryFileAndCarriesBothBodies(t *testing.T) {
	content, plan := interfaceMismatchFixture()
	group := []PlannedFile{plan.Files[0], plan.Files[1]}
	prompt := repairGroupPrompt(group, plan, content,
		[]string{"store.go:8:9: *jsonlPersister does not implement persister"}, nil,
		map[string][]Declaration{}, []string{"internal/storage/store.go", "internal/storage/jsonl.go"}, "PLATFORM")

	for _, want := range []string{
		"must change TOGETHER",
		"// filename: internal/storage/store.go",
		"type persister interface",      // store.go's body
		"func (p *jsonlPersister) load", // jsonl.go's body
		"PLATFORM",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the group prompt is missing %q", want)
		}
	}
}

// A group repair that returns only some of the files is rejected. Applying half
// of it leaves exactly the disagreement it was meant to fix.
func TestAPartialGroupRepairIsRejected(t *testing.T) {
	_, plan := interfaceMismatchFixture()
	want := []PlannedFile{plan.Files[0], plan.Files[1]}

	full := &fakeProvider{responses: []string{
		"// filename: internal/storage/store.go\npackage storage\n\ntype persister interface{ load(string) ([]byte, error) }\n" +
			"// filename: internal/storage/jsonl.go\npackage storage\n\ntype jsonlPersister struct{}\n",
	}}
	if _, err := askForFiles(full, "p", want); err != nil {
		t.Fatalf("a complete group repair was rejected: %v", err)
	}

	partial := &fakeProvider{responses: []string{
		"// filename: internal/storage/store.go\npackage storage\n\ntype persister interface{}\n",
	}}
	_, err := askForFiles(partial, "p", want)
	if err == nil {
		t.Fatal("a partial group repair was accepted")
	}
	if !strings.Contains(err.Error(), "jsonl.go") {
		t.Errorf("the rejection does not name what was missing: %v", err)
	}
}
