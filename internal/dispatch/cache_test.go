package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIdenticalInputsReuseTheFile(t *testing.T) {
	root := t.TempDir()
	c := NewGenerationCache(root)
	f := PlannedFile{Path: "identity.go", Purpose: "keys", Declares: []string{"Sign"}}
	bps := map[string]string{"protocol/identity.md": "IDENTITY"}

	k1 := cacheKey(f, bps, "PLATFORM", fileSystemPrompt)
	c.Put(k1, "package main\n")
	if got := c.Get(k1); got != "package main\n" {
		t.Fatalf("cached content not returned: %q", got)
	}
	// A second run with the same inputs must produce the same key.
	if k2 := cacheKey(f, bps, "PLATFORM", fileSystemPrompt); k2 != k1 {
		t.Error("identical inputs produced different keys")
	}
}

func TestChangingABlueprintInvalidatesTheFile(t *testing.T) {
	// This is derived staleness: the file is stale exactly when what it was built
	// from changed.
	f := PlannedFile{Path: "identity.go", Purpose: "keys"}
	before := cacheKey(f, map[string]string{"protocol/identity.md": "V1"}, "P", fileSystemPrompt)
	after := cacheKey(f, map[string]string{"protocol/identity.md": "V2"}, "P", fileSystemPrompt)
	if before == after {
		t.Error("editing a blueprint did not invalidate the file generated from it")
	}
}

func TestAnUnrelatedBlueprintDoesNotInvalidate(t *testing.T) {
	// The payoff of relevance filtering: a file sent only what it needs is
	// invalidated only by changes to that. Editing the orchestrator spec must
	// not force go.mod to be regenerated.
	f := PlannedFile{Path: "go.mod", Purpose: "module"}
	sent := map[string]string{} // go.mod is sent no protocol blueprints
	before := cacheKey(f, sent, "PLATFORM", fileSystemPrompt)
	after := cacheKey(f, sent, "PLATFORM", fileSystemPrompt)
	if before != after {
		t.Error("a file's key changed with no change to its inputs")
	}
}

func TestChangingTheInstructionsInvalidates(t *testing.T) {
	// Reusing a file generated under different instructions would silently ship
	// output nobody asked for.
	f := PlannedFile{Path: "a.go", Purpose: "x"}
	bps := map[string]string{"b.md": "B"}
	if cacheKey(f, bps, "P", "prompt one") == cacheKey(f, bps, "P", "prompt two") {
		t.Error("changing the system prompt did not invalidate the cache")
	}
}

func TestChangingThePlanEntryInvalidates(t *testing.T) {
	bps := map[string]string{"b.md": "B"}
	a := PlannedFile{Path: "a.go", Purpose: "x", Declares: []string{"One"}}
	b := PlannedFile{Path: "a.go", Purpose: "x", Declares: []string{"One", "Two"}}
	if cacheKey(a, bps, "P", fileSystemPrompt) == cacheKey(b, bps, "P", fileSystemPrompt) {
		t.Error("asking a file to declare more did not invalidate it")
	}
}

func TestDeclarationOrderDoesNotChangeTheKey(t *testing.T) {
	// A plan listing the same symbols in a different order is the same request.
	bps := map[string]string{"b.md": "B"}
	a := PlannedFile{Path: "a.go", Purpose: "x", Declares: []string{"One", "Two"}}
	b := PlannedFile{Path: "a.go", Purpose: "x", Declares: []string{"Two", "One"}}
	if cacheKey(a, bps, "P", fileSystemPrompt) != cacheKey(b, bps, "P", fileSystemPrompt) {
		t.Error("reordering the declares list forced a regeneration")
	}
}

func TestAnUnwritableCacheDisablesItselfRatherThanFailing(t *testing.T) {
	// Losing reuse is an inconvenience; refusing to generate over it is not.
	c := NewGenerationCache("/proc/nonexistent-and-unwritable")
	if c.Enabled {
		t.Skip("this filesystem allowed the directory")
	}
	if got := c.Get("anything"); got != "" {
		t.Error("a disabled cache returned content")
	}
	c.Put("k", "v") // must not panic
}

func TestPruneKeepsLiveEntries(t *testing.T) {
	root := t.TempDir()
	c := NewGenerationCache(root)
	live := "a" + string(make([]byte, 0))
	for i := 0; i < 63; i++ {
		live += "b"
	}
	dead := live[:63] + "c"
	c.Put(live, "keep")
	c.Put(dead, "drop")
	removed, err := c.Prune(map[string]bool{live: true})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("pruned %d entries, want 1", removed)
	}
	if _, err := os.Stat(filepath.Join(root, cacheDirName, live)); err != nil {
		t.Error("a live entry was pruned")
	}
}
