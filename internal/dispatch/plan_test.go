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
	if gaps := ValidatePlan(goodPlan(), minimalRequirements(), nil); len(gaps) > 0 {
		t.Errorf("a complete plan was rejected: %v", gaps)
	}
}

func TestAMissingTypeIsNamed(t *testing.T) {
	// Rejection must say what is missing, so the re-plan is one call rather than
	// a guess.
	p := goodPlan()
	p.Files[0].Declares = []string{"AgentManifest"}
	gaps := ValidatePlan(p, minimalRequirements(), nil)
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
	gaps := ValidatePlan(p, minimalRequirements(), nil)
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
	gaps := ValidatePlan(p, minimalRequirements(), nil)
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
	gaps := ValidatePlan(p, minimalRequirements(), nil)
	if !strings.Contains(strings.Join(gaps, " "), "cycle") {
		t.Errorf("a dependency cycle was accepted: %v", gaps)
	}
}

func TestAnEscapingPathIsRejectedInThePlan(t *testing.T) {
	p := goodPlan()
	p.Files[0].Path = "../../etc/passwd"
	gaps := ValidatePlan(p, minimalRequirements(), nil)
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
		[]string{"x.md"}, "PLAT", nil, nil, nil, nil, nil)
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

// A plan must not claim a file another component owns.
//
// The content service's first plan named cmd/orchestrator/main.go, go.mod, and
// twelve files in packages the tenant already had. The prompt now says not to;
// this asserts the plan is REJECTED when it does anyway, because an instruction
// the model may decline is not a guard.
func TestPlanCannotClaimAnotherComponentsFiles(t *testing.T) {
	st := &TenantState{
		Module: "hubgen",
		Owned: map[string]string{
			"cmd/orchestrator/main.go":   "orchestrator",
			"go.mod":                     "orchestrator",
			"internal/protocol/types.go": "orchestrator",
		},
	}
	p := goodPlan()
	p.Target = "content"
	p.Files = append(p.Files,
		PlannedFile{Path: "cmd/orchestrator/main.go", Purpose: "entry point"},
		PlannedFile{Path: "go.mod", Purpose: "module"},
		PlannedFile{Path: "internal/protocol/types.go", Purpose: "wire types"},
	)

	gaps := ValidatePlan(p, minimalRequirements(), st)
	joined := strings.Join(gaps, "\n")
	for _, want := range []string{
		"cmd/orchestrator/main.go",
		"go.mod already exists",
		"internal/protocol/types.go",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("plan claiming another component's files was accepted for %s\ngaps:\n%s", want, joined)
		}
	}
}

// The same plan, in a tenant that does not have those files yet, is fine.
func TestPlanOwnershipGuardIsQuietOnAnEmptyTenant(t *testing.T) {
	p := goodPlan()
	p.Target = "orchestrator"
	if gaps := ValidatePlan(p, minimalRequirements(), &TenantState{Owned: map[string]string{}}); len(gaps) > 0 {
		t.Errorf("ownership guard fired on an empty tenant: %v", gaps)
	}
}

// The guard must judge against the CALLER's target, never the model's.
//
// plan.Target was set after MakePlan returned, so ValidatePlan read the model's
// JSON. A content build was told "your entry point is cmd/orchestrator/main.go"
// and then rejected for writing it — three attempts, no valid plan, and the
// contradiction came from one guard reading an untrusted value.
func TestPlanTargetComesFromTheCallerNotTheModel(t *testing.T) {
	provider := &fakeProvider{responses: []string{`{
      "target": "orchestrator",
      "root": ".",
      "prepare": "go mod tidy",
      "build": "go build ./...",
      "files": [
        {"path": "internal/content/types.go", "purpose": "types",
         "declares": ["AgentManifest", "ErrorResponse"]},
        {"path": "cmd/content/main.go", "purpose": "entry point",
         "serves": ["POST /v1/register", "GET /v1/health"],
         "depends_on": ["internal/content/types.go"]}
      ]
    }`}}

	st := &TenantState{Module: "hubgen", Owned: map[string]string{"cmd/orchestrator/main.go": "orchestrator"}}
	plan, err := MakePlan(provider, minimalRequirements(), "content", "go", "SPEC", "PLAT", st, nil)
	if err != nil {
		t.Fatalf("a plan writing its own cmd/content/main.go was rejected: %v", err)
	}
	if plan.Target != "content" {
		t.Errorf("plan.Target = %q, want the caller's target %q", plan.Target, "content")
	}
}

// A declared store operation must be planned under its declared name.
//
// The prompt asks for this; the check is what makes it binding. A plan that
// renames one is not a worse plan — it cannot be applied to the existing
// tenant, because every caller is written against the blueprint's name.
func TestAPlanMustDeclareTheOperationsItWasGiven(t *testing.T) {
	req := &Requirements{Operations: []string{"GetAgent", "PutAgent", "ListAgents"}}

	// Renamed: the exact failure that regenerated ten compliant files.
	renamed := &Plan{Target: "orchestrator", Root: ".", Files: []PlannedFile{
		{Path: "internal/orchestrator/registry.go", Purpose: "the registry",
			Declares: []string{"Registry", "(*Registry).Get", "(*Registry).Put", "(*Registry).List"}},
	}}
	gaps := ValidatePlan(renamed, req, nil)
	if !anyContains(gaps, "GetAgent") {
		t.Fatalf("a plan that renamed GetAgent to Get was accepted: %v", gaps)
	}

	// Declared correctly, in method notation, which is how a store is planned.
	correct := &Plan{Target: "orchestrator", Root: ".", Files: []PlannedFile{
		{Path: "internal/orchestrator/registry.go", Purpose: "the registry",
			Declares: []string{"Registry", "(*Registry).GetAgent", "(*Registry).PutAgent",
				"(*Registry).ListAgents"}},
	}}
	for _, g := range ValidatePlan(correct, req, nil) {
		if strings.Contains(g, "architecture/storage declares") {
			t.Fatalf("a correctly named plan was rejected: %s", g)
		}
	}
}

func anyContains(in []string, sub string) bool {
	for _, s := range in {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
