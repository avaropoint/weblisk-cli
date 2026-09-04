package dispatch

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// promptFor renders the invariant prompt a file would be generated from, which
// is what the cache is keyed on.
func promptFor(f PlannedFile, bps map[string]string, platBP, module string) string {
	plan := &Plan{Root: ".", Module: module, Files: []PlannedFile{f}}
	order := make([]string, 0, len(bps))
	for k := range bps {
		order = append(order, k)
	}
	sort.Strings(order)
	return filePrompt(f, plan, "go", bps, order, platBP, nil, nil, nil, nil, nil)
}

func TestIdenticalInputsReuseTheFile(t *testing.T) {
	root := t.TempDir()
	c := NewGenerationCache(root)
	f := PlannedFile{Path: "identity.go", Purpose: "keys", Declares: []string{"Sign"}}
	bps := map[string]string{"protocol/identity.md": "IDENTITY"}

	k1 := cacheKey(f, promptFor(f, bps, "PLATFORM", "t"), fileSystemPrompt)
	c.Put(k1, "package main\n")
	if got := c.Get(k1); got != "package main\n" {
		t.Fatalf("cached content not returned: %q", got)
	}
	if k2 := cacheKey(f, promptFor(f, bps, "PLATFORM", "t"), fileSystemPrompt); k2 != k1 {
		t.Error("identical inputs produced different keys")
	}
}

func TestChangingABlueprintInvalidatesTheFile(t *testing.T) {
	// Derived staleness: the file is stale exactly when what it was built from
	// changed.
	f := PlannedFile{Path: "identity.go", Purpose: "keys"}
	before := cacheKey(f, promptFor(f, map[string]string{"protocol/identity.md": "V1"}, "P", "t"), fileSystemPrompt)
	after := cacheKey(f, promptFor(f, map[string]string{"protocol/identity.md": "V2"}, "P", "t"), fileSystemPrompt)
	if before == after {
		t.Error("editing a blueprint did not invalidate the file generated from it")
	}
}

func TestChangingTheModulePathInvalidates(t *testing.T) {
	// The fault this key shape exists for. The module path was added to the
	// prompt — the fact that makes every file agree on its import paths — and the
	// old key, a hand-listed set of ingredients, did not include it. A later run
	// served forty-three files generated before the fix, importing a module name
	// that no longer existed, and the build failed in a file nobody had touched.
	f := PlannedFile{Path: "internal/orchestrator/handlers.go", Purpose: "handlers"}
	bps := map[string]string{"x.md": "SPEC"}
	if cacheKey(f, promptFor(f, bps, "P", "weblisk-server"), fileSystemPrompt) ==
		cacheKey(f, promptFor(f, bps, "P", "hubgen"), fileSystemPrompt) {
		t.Error("changing the module path did not invalidate files whose imports depend on it")
	}
}

func TestChangingTheInstructionsInvalidates(t *testing.T) {
	// Reusing a file generated under different instructions would silently ship
	// output nobody asked for.
	f := PlannedFile{Path: "a.go", Purpose: "x"}
	p := promptFor(f, map[string]string{"b.md": "B"}, "P", "t")
	if cacheKey(f, p, "prompt one") == cacheKey(f, p, "prompt two") {
		t.Error("changing the system prompt did not invalidate the cache")
	}
}

func TestChangingThePlanEntryInvalidates(t *testing.T) {
	// A file asked to declare something different is a different file.
	bps := map[string]string{"b.md": "B"}
	a := PlannedFile{Path: "a.go", Purpose: "x", Declares: []string{"Alpha"}}
	b := PlannedFile{Path: "a.go", Purpose: "x", Declares: []string{"Beta"}}
	if cacheKey(a, promptFor(a, bps, "P", "t"), fileSystemPrompt) ==
		cacheKey(b, promptFor(b, bps, "P", "t"), fileSystemPrompt) {
		t.Error("changing what a file must declare did not invalidate it")
	}
}

func TestPruneRemovesOnlyUnreferencedEntries(t *testing.T) {
	root := t.TempDir()
	c := NewGenerationCache(root)
	live := cacheKey(PlannedFile{Path: "a.go"}, "live", fileSystemPrompt)
	dead := cacheKey(PlannedFile{Path: "b.go"}, "dead", fileSystemPrompt)
	c.Put(live, "A")
	c.Put(dead, "B")
	n, err := c.Prune(map[string]bool{live: true})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	if c.Get(live) != "A" {
		t.Error("a referenced entry was pruned")
	}
}

func TestACacheThatCannotOpenDisablesItself(t *testing.T) {
	// Losing reuse is an inconvenience; refusing to generate over it is not.
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewGenerationCache(f)
	if c.Enabled {
		t.Error("a cache that cannot create its directory reported itself enabled")
	}
	c.Put("k", "v")
	if c.Get("k") != "" {
		t.Error("a disabled cache returned content")
	}
}

// Every requirement that shapes a plan must be in the plan's key.
//
// Declared operations shape it more than anything else — they are the symbols
// every generated file is written against. A cached plan served after a
// blueprint renamed one would generate against a contract nobody holds.
func TestRenamingADeclaredNameReDerivesThePlan(t *testing.T) {
	base := &Requirements{
		Types:     []string{"Agent"},
		Endpoints: []string{"POST /v1/register"},
		EndpointOps: []EndpointOperation{
			{Method: "POST", Path: "/v1/register", Operation: "Register"},
		},
		Operations: []string{"GetAgent", "PutAgent"},
	}
	key := func(r *Requirements) string { return planKey(r, "orchestrator", "go", "plat", "sys") }
	original := key(base)

	renamedOp := *base
	renamedOp.Operations = []string{"Get", "PutAgent"}
	if key(&renamedOp) == original {
		t.Error("renaming a store operation did not re-derive the plan")
	}

	renamedEndpoint := *base
	renamedEndpoint.EndpointOps = []EndpointOperation{
		{Method: "POST", Path: "/v1/register", Operation: "AgentRegister"},
	}
	if key(&renamedEndpoint) == original {
		t.Error("renaming an endpoint operation did not re-derive the plan")
	}

	// And an unchanged specification must still hit, or nothing is incremental.
	same := *base
	if key(&same) != original {
		t.Error("identical requirements produced a different key")
	}
}
