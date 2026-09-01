package dispatch

import (
	"os"
	"strings"
	"testing"
)

func realBlueprint(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile("../../../weblisk-blueprints/" + rel)
	if err != nil {
		t.Skipf("blueprints not checked out beside the CLI: %v", err)
	}
	return string(b)
}

// TestRequirementsComeFromTheBlueprints is the point of removing the manifest:
// the specification already enumerates what must exist, and a hand-authored list
// was a smaller copy of it.
func TestRequirementsComeFromTheBlueprints(t *testing.T) {
	types := ExtractTypes(realBlueprint(t, "protocol/types.md"))
	if len(types) < 40 {
		t.Errorf("got %d types; protocol/types.md enumerates far more", len(types))
	}
	// The manifest named four of these. The blueprint names them all.
	for _, want := range []string{"AgentManifest", "ErrorResponse", "Capability", "WorkflowExecution"} {
		found := false
		for _, tp := range types {
			if tp == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is defined in protocol/types.md and was not extracted", want)
		}
	}

	eps := ExtractEndpoints(realBlueprint(t, "protocol/spec.md"), "Orchestrator Endpoints")
	if len(eps) != 7 {
		t.Errorf("got %d orchestrator endpoints, want 7: %v", len(eps), eps)
	}
	// Agent endpoints must not leak in — an orchestrator serving them would be a
	// different component.
	for _, e := range eps {
		if strings.Contains(e, "/v1/execute") || strings.Contains(e, "/v1/describe") {
			t.Errorf("agent endpoint %s appeared in the orchestrator set", e)
		}
	}
}

func minimalRequirements() *Requirements {
	return &Requirements{
		Types:     []string{"AgentManifest", "ErrorResponse"},
		Endpoints: []string{"POST /v1/register", "GET /v1/health"},
	}
}

func goodPlan() *Plan {
	return &Plan{
		Target: "orchestrator", Root: "server", Build: "go build ./...",
		Files: []PlannedFile{
			{Path: "protocol.go", Purpose: "types", Declares: []string{"AgentManifest", "ErrorResponse"}},
			{Path: "orchestrator.go", Purpose: "server", Serves: []string{"POST /v1/register", "GET /v1/health"},
				DependsOn: []string{"protocol.go"}},
		},
	}
}

func TestAValidPlanPasses(t *testing.T) {
	if gaps := ValidatePlan(goodPlan(), minimalRequirements()); len(gaps) > 0 {
		t.Errorf("a complete plan was rejected: %v", gaps)
	}
}

func TestAMissingTypeIsNamed(t *testing.T) {
	// Rejection must say what is missing, so the re-plan is one call rather than
	// a guess.
	p := goodPlan()
	p.Files[0].Declares = []string{"AgentManifest"}
	gaps := ValidatePlan(p, minimalRequirements())
	if len(gaps) == 0 {
		t.Fatal("a plan omitting a required type was accepted")
	}
	if !strings.Contains(strings.Join(gaps, " "), "ErrorResponse") {
		t.Errorf("the gap does not name the missing type: %v", gaps)
	}
}

func TestAMissingEndpointIsNamed(t *testing.T) {
	p := goodPlan()
	p.Files[1].Serves = []string{"GET /v1/health"}
	gaps := ValidatePlan(p, minimalRequirements())
	if !strings.Contains(strings.Join(gaps, " "), "/v1/register") {
		t.Errorf("the gap does not name the unserved endpoint: %v", gaps)
	}
}

// TestASymbolInTwoFilesIsRejectedBeforeGenerating is the coherence failure that
// produced 7 redeclaration errors — now caught at planning, before any file is
// generated at all.
func TestASymbolInTwoFilesIsRejectedBeforeGenerating(t *testing.T) {
	p := goodPlan()
	p.Files[1].Declares = []string{"AgentManifest"}
	gaps := ValidatePlan(p, minimalRequirements())
	if !strings.Contains(strings.Join(gaps, " "), "more than one file") {
		t.Errorf("a duplicate declaration was not caught in the plan: %v", gaps)
	}
}

// TestOrderPutsDependenciesFirst is the failure declaration-passing could not
// fix: an entry point generated before what it constructs.
func TestOrderPutsDependenciesFirst(t *testing.T) {
	p := &Plan{
		Root: "server",
		Files: []PlannedFile{
			{Path: "main.go", Purpose: "entry", DependsOn: []string{"orchestrator.go"}},
			{Path: "orchestrator.go", Purpose: "server", DependsOn: []string{"protocol.go"}},
			{Path: "protocol.go", Purpose: "types"},
		},
	}
	order := p.Order()
	var paths []string
	for _, f := range order {
		paths = append(paths, f.Path)
	}
	want := []string{"protocol.go", "orchestrator.go", "main.go"}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("generation order = %v, want %v", paths, want)
		}
	}
}

func TestACycleIsRejected(t *testing.T) {
	p := goodPlan()
	p.Files[0].DependsOn = []string{"orchestrator.go"}
	gaps := ValidatePlan(p, minimalRequirements())
	if !strings.Contains(strings.Join(gaps, " "), "cycle") {
		t.Errorf("a dependency cycle was accepted: %v", gaps)
	}
}

func TestAnEscapingPathIsRejectedInThePlan(t *testing.T) {
	p := goodPlan()
	p.Files[0].Path = "../../etc/passwd"
	gaps := ValidatePlan(p, minimalRequirements())
	if len(gaps) == 0 {
		t.Error("a traversing path was accepted in a plan")
	}
}

func TestPlanParsingToleratesAFenceAndPreamble(t *testing.T) {
	raw := "Here is the plan:\n```json\n{\"target\":\"orchestrator\",\"root\":\"server\"," +
		"\"build\":\"go build\",\"files\":[{\"path\":\"a.go\",\"purpose\":\"x\"}]}\n```"
	p, err := ParsePlan(raw)
	if err != nil {
		t.Fatalf("a fenced plan with a preamble was rejected: %v", err)
	}
	if len(p.Files) != 1 || p.Files[0].Path != "a.go" {
		t.Errorf("parsed wrongly: %+v", p)
	}
}

func TestAnEmptyPlanIsRejected(t *testing.T) {
	if _, err := ParsePlan(`{"target":"orchestrator","files":[]}`); err == nil {
		t.Error("a plan with no files was accepted")
	}
}

func TestTheModulePathIsStatedOncePerRun(t *testing.T) {
	// go.mod declared "module weblisk" and all fifty-two other files imported
	// "weblisk-server/internal/…" — consistent with each other and wrong. With
	// one flat package there were no import paths and this could not happen; the
	// multi-package layout created the requirement and nothing carried it.
	for _, tc := range []struct{ root, want string }{
		{"/tmp/avaropoint", "avaropoint"},
		{"/tmp/Acme Corp", "acme-corp"},
		{"/tmp/weblisk_hub", "weblisk_hub"},
	} {
		if got := moduleNameFor(tc.root); got != tc.want {
			t.Errorf("moduleNameFor(%q) = %q, want %q", tc.root, got, tc.want)
		}
	}

	plan := &Plan{Root: ".", Module: "avaropoint",
		Files: []PlannedFile{{Path: "cmd/orchestrator/main.go", Purpose: "entry"}}}
	p := filePrompt(plan.Files[0], plan, "go", map[string]string{"x.md": "SPEC"},
		[]string{"x.md"}, "PLAT", nil, nil, nil, nil)
	if !strings.Contains(p, "Module path: avaropoint") {
		t.Error("the module path is not stated in the prompt")
	}
	if !strings.Contains(p, "go.mod declares exactly this module") {
		t.Error("nothing ties go.mod to the imports, which is the fault this prevents")
	}
	// It is invariant, so it must sit in the cacheable prefix.
	if strings.Index(p, "Module path:") > strings.Index(p, "--- YOUR TASK ---") {
		t.Error("the module path is in the variable tail — it is the same for every file")
	}
}
