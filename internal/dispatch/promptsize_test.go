package dispatch

// What the model is actually sent, measured rather than assumed.
//
// The prompts here are large — a per-file prompt runs to ~380 KB, near 100k
// tokens — and every byte is paid for on every one of a run's files. A
// duplicated document is not a style problem at that scale.

import (
	"strings"
	"testing"
)

// A blueprint sent as its own labelled section is not also sent in the corpus.
//
// GenerationRoots makes the platform blueprint a ROOT, so it is in the graph;
// ComponentInit also loads it separately as platBP. Both resolve the same path
// through the same sources, so they are byte-identical — and both prompts wrote
// both copies. 36,467 bytes of platforms/go.md, on every file of every run.
func TestThePlatformBlueprintIsSentOnce(t *testing.T) {
	g, err := ResolveGraph("", Agent("alerting"), "go")
	if err != nil {
		t.Skipf("no blueprint corpus on this machine: %v", err)
	}
	platName := PlatformBlueprint("go")
	platBP := g.Map[platName]
	if platBP == "" {
		t.Fatalf("%s is not in the graph; this test is not measuring what it says", platName)
	}
	// The document's own bytes are the marker. A sentence from it can repeat
	// inside it — the first attempt at this test used one that appears twice —
	// and a shorter marker could occur in another blueprint. The whole body
	// occurs exactly once per copy, by definition.
	marker := platBP

	req := GatherRequirements(g, "agent")
	self := LayoutOf(Agent("alerting"), "go")
	specs := g.JoinedExcept(platName)

	plan := planPrompt(req, self, "go", specs, platBP, &TenantState{})
	if n := strings.Count(plan, marker); n != 1 {
		t.Errorf("the plan prompt carries the platform blueprint %d times, want 1 "+
			"(%d bytes each)", n, len(platBP))
	}

	f := PlannedFile{Path: "internal/agents/alerting/logic.go", Purpose: "logic"}
	p := &Plan{Target: "agent", Root: ".", Module: "acme", Files: []PlannedFile{f}}
	file := filePrompt(f, p, "go", g.Map, g.Order, platBP, nil, nil, req.Checklist, req.Bindings, &TenantState{})
	if n := strings.Count(file, marker); n != 1 {
		t.Errorf("the file prompt carries the platform blueprint %d times, want 1 "+
			"(%d bytes each, on every file of the run)", n, len(platBP))
	}

	// And it is still FIRST, which is the order it was read in before the
	// duplicate was removed. relevance.go's standard for changing what the model
	// reads first is to measure the effect on build errors; keeping the order
	// means there is nothing to measure.
	if i := strings.Index(file, marker); i < 0 || i > strings.Index(file, "\n--- BLUEPRINTS ---") {
		t.Error("the platform blueprint no longer precedes the rest of the corpus")
	}
	// The attribution joinBlueprints would have given it is not lost.
	if !strings.Contains(file, "PLATFORM BLUEPRINT ("+platName+")") {
		t.Error("the platform blueprint's section does not name the file it came from")
	}
}

// The corpus reaching the planner is part of what the plan was made from, so a
// change to it must re-derive the plan.
//
// planKey hashed the requirements, the platform blueprint, the system prompt and
// the layout — and never `specs`, which is the whole joined corpus the planner
// reads. A blueprint edit that changed what the planner was told served the plan
// made from the old text. cache.go records what that class of drift cost the
// last time: forty-three files generated against a module path that no longer
// existed.
func TestTheCorpusReachesThePlanCacheKey(t *testing.T) {
	req := minimalRequirements()
	base := planKey(req, "agent:alerting", "go", "plat", "sys", "layout"+"CORPUS A")
	moved := planKey(req, "agent:alerting", "go", "plat", "sys", "layout"+"CORPUS B")
	if base == moved {
		t.Error("the plan cache key is unchanged when the corpus the planner reads changes")
	}
}
