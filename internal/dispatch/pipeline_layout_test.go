package dispatch

// The pipeline reading a component's layout rather than assuming one.
//
// Every test here corresponds to a failure a real generation produced when the
// three component commands were first switched onto this pipeline and the
// switch had to be reverted the same day. Compiling and passing the suite is
// what the first attempt also did.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// LayoutOf and PlatformBlueprint must recognise the same platform strings.
//
// Two functions answering "which platform is this" differently is how a
// component gets generated against one blueprint and laid out per another —
// and both fall through to go, so a divergence is silent.
func TestLayoutAndBlueprintAgreeOnEveryPlatform(t *testing.T) {
	// Every key either switch names, plus strings neither does.
	for _, p := range []string{"go", "node", "rust", "cloudflare", "", "Go", "nodejs", "wasm"} {
		bp := PlatformBlueprint(p)
		l := LayoutOf(Agent("billing"), p)
		wantGo := bp == "platforms/go.md"
		isGo := l.Entry == "cmd/billing/main.go"
		if wantGo != isGo {
			t.Errorf("platform %q: blueprint %s but layout entry %s — the two switches disagree",
				p, bp, l.Entry)
		}
	}
}

// A named component is told its own directories, not its kind's.
//
// `weblisk agent create billing` was told its entry point was
// cmd/agent/main.go. It planned exactly that, and the ownership check then
// rejected the file the instruction had just demanded.
func TestANamedComponentIsToldItsOwnDirectories(t *testing.T) {
	got := LayoutOf(Agent("billing"), "go").FormatLayout()
	for _, want := range []string{"cmd/billing/main.go", "internal/agents/billing/"} {
		if !strings.Contains(got, want) {
			t.Errorf("the planner is never told about %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "cmd/agent/") {
		t.Errorf("the planner is told its directory is its KIND's:\n%s", got)
	}
}

// The layout is stated even when there is no tenant yet.
//
// These sentences used to live inside FormatTenantState, which returns nothing
// for an empty directory — so the one generation with no sibling to collide
// with was also the only one told nothing about where its files belonged.
func TestTheLayoutIsStatedIntoAnEmptyTenant(t *testing.T) {
	st := ReadTenantState(t.TempDir(), "agent:billing", LayoutOf(Agent("billing"), "go"))
	if st.FormatTenantState() != "" {
		t.Fatal("an empty tenant reported existing state; this test is not measuring what it says")
	}
	prompt := planPrompt(minimalRequirements(), LayoutOf(Agent("billing"), "go"),
		"go", "SPEC", "PLAT", st)
	if !strings.Contains(prompt, "cmd/billing/main.go") {
		t.Errorf("a first generation is not told its entry point:\n%s", prompt)
	}
	if !strings.Contains(prompt, "agent named billing") {
		t.Error("two agents are asked for byte-identical plans — the name never reaches the prompt")
	}
}

// A first build has no manifest. The path is what identifies self.
//
// Derived from the KEY, the directory read `internal/agent:billing`, matched
// nothing, and a re-run of an interrupted `agent create billing` was offered
// its own half-written package as somebody else's to import.
func TestAnInterruptedFirstBuildStillRecognisesItsOwnFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module acme\n\ngo 1.27\n")
	write(t, root, "internal/agents/billing/store.go", "package billing\n\ntype Invoice struct{}\n")
	write(t, root, "internal/protocol/types.go", "package protocol\n\ntype ErrorResponse struct{}\n")

	st := ReadTenantState(root, Agent("billing").Key(), LayoutOf(Agent("billing"), "go"))

	if !containsStr(st.SelfNames, "Invoice") {
		t.Errorf("the agent was not shown the names it declared last time: %v", st.SelfNames)
	}
	for _, p := range st.Packages {
		if p.Dir == "internal/agents/billing" {
			t.Fatal("the agent's own half-written package was offered to it as one to import")
		}
	}
}

// A sibling's directory is refused; shared code is not.
func TestAPlanMayNotWriteIntoASiblingsDirectory(t *testing.T) {
	billing := LayoutOf(Agent("billing"), "go")
	for _, tc := range []struct {
		path     string
		rejected bool
		why      string
	}{
		{"cmd/billing/main.go", false, "its own entry point"},
		{"internal/agents/billing/store.go", false, "its own package"},
		{"internal/protocol/types.go", false, "shared code a first component must plan"},
		{"cmd/shipping/main.go", true, "a sibling agent's entry point"},
		{"internal/agents/shipping/store.go", true, "a sibling agent's package"},
		{"cmd/agent/main.go", true, "the KIND's directory, which the old rule permitted"},
		{"internal/domains/seo/x.go", true, "a domain controller's package"},
	} {
		p := &Plan{Target: "agent", Root: ".", Build: "go build ./cmd/billing",
			Files: []PlannedFile{{Path: tc.path, Purpose: "p"}}}
		var found bool
		for _, g := range ValidatePlan(p, minimalRequirements(), nil, billing) {
			if strings.Contains(g, "another component's directory") {
				found = true
			}
		}
		if found != tc.rejected {
			t.Errorf("%s (%s): rejected=%v, want %v", tc.path, tc.why, found, tc.rejected)
		}
	}
}

// A build command that builds a sibling is refused; one that names no component
// directory is left alone.
func TestTheBuildCommandMustBuildThisComponent(t *testing.T) {
	billing := LayoutOf(Agent("billing"), "go")
	for _, tc := range []struct {
		build    string
		rejected bool
	}{
		{"go build -o bin/billing ./cmd/billing", false},
		{"go build ./...", false},
		{"npm run build", false},
		{"cargo build --workspace", false},
		{"go build -o bin/billing ./cmd/agent", true},
		{"go build ./cmd/shipping && go vet ./...", true},
	} {
		p := &Plan{Target: "agent", Root: ".", Build: tc.build,
			Files: []PlannedFile{{Path: "cmd/billing/main.go", Purpose: "p"}}}
		var found bool
		for _, g := range ValidatePlan(p, minimalRequirements(), nil, billing) {
			if strings.Contains(g, "the build command builds") {
				found = true
			}
		}
		if found != tc.rejected {
			t.Errorf("build %q: rejected=%v, want %v", tc.build, found, tc.rejected)
		}
	}
}

// A gateway's directory is this CLI's convention, not a blueprint's. No rule
// enforces a path nothing specified — and a named component's home is out of
// bounds anyway.
func TestAGatewayIsNotHeldToAnUnspecifiedLayout(t *testing.T) {
	gw := LayoutOf(Gateway(), "go")
	// Its own placement is not enforced: no platform blueprint gives a gateway
	// a row, and Locate reads the manifest so one placed elsewhere is still
	// found by `gateway start`.
	p := &Plan{Target: "gateway", Root: ".", Build: "go build ./cmd/gw",
		Files: []PlannedFile{{Path: "cmd/gw/main.go", Purpose: "p"}}}
	for _, g := range ValidatePlan(p, minimalRequirements(), nil, gw) {
		if strings.Contains(g, "another component's directory") {
			t.Errorf("a convention was enforced as if a blueprint stated it: %s", g)
		}
	}
	// A named component's home still is. Writing into internal/orchestrator is
	// wrong under any reading of any blueprint, so nothing gates it — and
	// without this the gateway was the one component free to claim it.
	intrusion := &Plan{Target: "gateway", Root: ".", Build: "go build ./cmd/gateway",
		Files: []PlannedFile{{Path: "internal/orchestrator/registry.go", Purpose: "p"}}}
	var caught bool
	for _, g := range ValidatePlan(intrusion, minimalRequirements(), nil, gw) {
		if strings.Contains(g, "another component's directory") {
			caught = true
		}
	}
	if !caught {
		t.Error("a gateway planned the orchestrator's library and nothing refused it")
	}
	// And a build command naming another component is caught for a gateway too.
	badBuild := &Plan{Target: "gateway", Root: ".", Build: "go build ./cmd/orchestrator",
		Files: []PlannedFile{{Path: "cmd/gateway/main.go", Purpose: "p"}}}
	caught = false
	for _, g := range ValidatePlan(badBuild, minimalRequirements(), nil, gw) {
		if strings.Contains(g, "the build command builds") {
			caught = true
		}
	}
	if !caught {
		t.Error("a gateway was allowed to build the orchestrator")
	}
}

// Where the platform gives each component its own build manifest, planning one
// inside the component's directory is required — and the tenant's own is still
// refused at the root.
func TestAContainedComponentPlansItsOwnBuildManifest(t *testing.T) {
	cf := LayoutOf(Agent("billing"), "cloudflare")
	if !cf.Contained {
		t.Fatal("a cloudflare Worker is not marked as carrying its own build manifest")
	}
	p := &Plan{Target: "agent", Root: ".", Build: "npx wrangler deploy",
		Files: []PlannedFile{
			{Path: "agents/billing/package.json", Purpose: "manifest"},
			{Path: "agents/billing/wrangler.toml", Purpose: "worker config"},
			{Path: "agents/billing/src/index.js", Purpose: "entry"},
		}}
	st := &TenantState{Module: "acme", Owned: map[string]string{}}
	for _, g := range ValidatePlan(p, minimalRequirements(), st, cf) {
		if strings.Contains(g, "do not plan it") || strings.Contains(g, "another component's directory") {
			t.Errorf("a Worker was refused its own build manifest: %s", g)
		}
	}
	if !strings.Contains(cf.FormatLayout(), "inside agents/billing/") {
		t.Errorf("a Worker is not told to put its manifest in its own directory:\n%s", cf.FormatLayout())
	}

	// The tenant's own module is still not the component's to plan.
	goAgent := LayoutOf(Agent("billing"), "go")
	gp := &Plan{Target: "agent", Root: ".", Build: "go build ./cmd/billing",
		Files: []PlannedFile{{Path: "go.mod", Purpose: "module"}}}
	var refused bool
	for _, g := range ValidatePlan(gp, minimalRequirements(), st, goAgent) {
		if strings.Contains(g, "do not plan it") {
			refused = true
		}
	}
	if !refused {
		t.Error("a component planned the tenant's own go.mod and nothing refused it")
	}
}

// Two agents in one tenant own different files, and neither rebuild reaches the
// other's. This is the case the first switch could never reach: the second
// agent's plan was rejected by the first agent's manifest.
func TestTwoAgentsInOneTenantDoNotCollide(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module acme\n\ngo 1.27\n")
	write(t, root, "cmd/billing/main.go", "package main\n\nfunc main() {}\n")
	write(t, root, "internal/agents/billing/store.go", "package billing\n\ntype Invoice struct{}\n")
	write(t, root, "cmd/shipping/main.go", "package main\n\nfunc main() {}\n")

	writeManifest(t, root, Agent("billing").Key(),
		"cmd/billing/main.go", "internal/agents/billing/store.go")

	// Shipping's view: billing's files are owned, shipping's are its own.
	st := ReadTenantState(root, Agent("shipping").Key(), LayoutOf(Agent("shipping"), "go"))
	if st.Owned["cmd/billing/main.go"] != Agent("billing").Key() {
		t.Errorf("billing's entry point is not attributed to billing: %v", st.Owned)
	}
	if _, taken := st.Owned["cmd/shipping/main.go"]; taken {
		t.Error("shipping's own entry point was reported as another component's")
	}

	// And shipping's plan for its own files is accepted, while billing's are refused.
	p := &Plan{Target: "agent", Root: ".", Build: "go build ./cmd/shipping",
		Files: []PlannedFile{
			{Path: "cmd/shipping/main.go", Purpose: "entry"},
			{Path: "internal/agents/shipping/store.go", Purpose: "store"},
		}}
	for _, g := range ValidatePlan(p, minimalRequirements(), st, LayoutOf(Agent("shipping"), "go")) {
		if strings.Contains(g, "another component") || strings.Contains(g, "belongs to the") {
			t.Errorf("shipping was refused its own files: %s", g)
		}
	}
	stolen := &Plan{Target: "agent", Root: ".", Build: "go build ./cmd/shipping",
		Files: []PlannedFile{{Path: "cmd/billing/main.go", Purpose: "entry"}}}
	if len(ValidatePlan(stolen, minimalRequirements(), st, LayoutOf(Agent("shipping"), "go"))) == 0 {
		t.Error("shipping planned billing's entry point and nothing refused it")
	}
}

// Reconcile removes THIS component's stale files and leaves a sibling's alone.
func TestReconcileDoesNotReachASiblingsFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module acme\n\ngo 1.27\n")
	write(t, root, "cmd/billing/main.go", "package main\n\nfunc main() {}\n")
	write(t, root, "cmd/shipping/main.go", "package main\n\nfunc main() {}\n")
	write(t, root, "cmd/shipping/old.go", "package main\n")

	writeManifest(t, root, Agent("billing").Key(), "cmd/billing/main.go")
	writeManifest(t, root, Agent("shipping").Key(), "cmd/shipping/main.go", "cmd/shipping/old.go")

	st := ReadTenantState(root, Agent("shipping").Key(), LayoutOf(Agent("shipping"), "go"))
	plan := &Plan{Target: "agent", Owner: Agent("shipping").Key(), Root: ".",
		Files: []PlannedFile{{Path: "cmd/shipping/main.go", Purpose: "entry"}}}
	rec, err := ReconcileTarget(root, plan, st)
	if err != nil {
		t.Fatal(err)
	}
	if !containsStr(rec.Stale, "cmd/shipping/old.go") {
		t.Errorf("shipping's own stale file was not removed: %+v", rec)
	}
	if _, err := os.Stat(filepath.Join(root, "cmd/billing/main.go")); err != nil {
		t.Error("shipping's rebuild deleted billing's entry point")
	}
}

func writeManifest(t *testing.T, root, owner string, files ...string) {
	t.Helper()
	writeManifestOn(t, root, owner, "", files...)
}

func writeManifestOn(t *testing.T, root, owner, platform string, files ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, cacheDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := json.Marshal(writtenManifest{
		Target: owner, Root: ".", Platform: platform, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestName(root, owner), m, 0o644); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The node orchestrator's entry point sits above its directory, and widening
// its directories to cover it hands it every agent in the tenant.
func TestTheNodeOrchestratorDoesNotOwnEveryAgent(t *testing.T) {
	l := LayoutOf(Orchestrator(), "node")
	if !l.Owns("src/server.ts") {
		t.Error("the orchestrator does not own its own entry point")
	}
	for _, foreign := range []string{"src/agents/billing/index.ts", "src/domains/seo/index.ts"} {
		if l.Owns(foreign) {
			t.Errorf("the orchestrator owns %s", foreign)
		}
		if !l.Foreign(foreign) {
			t.Errorf("%s is not foreign to the orchestrator — it could plan over a running agent", foreign)
		}
	}
	// And shared code is still shared.
	if l.Foreign("src/protocol/types.ts") {
		t.Error("shared protocol code was treated as a sibling's")
	}
}

// A singleton component's library directory is nobody else's to plan.
//
// Families cover kinds that come in multiples: any path under cmd/ or
// internal/agents/ belongs to whichever instance is named there. A singleton
// has no family — architecture/orchestrator's library is internal/orchestrator
// exactly, and internal/ also holds internal/protocol, which is shared.
//
// Measured before the fix: Foreign answered false for
// internal/orchestrator/registry.go asked of an agent. The harm is not the
// write — it is that the agent's manifest RECORDS the file, so a later
// `weblisk server init` is refused its own directory by the ownership rule.
func TestASingletonsLibraryIsNotOpenToEveryComponent(t *testing.T) {
	cron := LayoutOf(Agent("cron"), "go")
	for _, foreign := range []string{
		"internal/orchestrator/registry.go",
		"internal/gateway/routes.go",
		"internal/content/store.go",
	} {
		if !cron.Foreign(foreign) {
			t.Errorf("%s is not foreign to an agent — it could claim a singleton's library", foreign)
		}
	}
	// Shared code stays shared. The first component into a tenant plans all of
	// these, and internal/agent is the FRAMEWORK every agent imports — not an
	// instance of one.
	for _, shared := range []string{
		"internal/protocol/types.go",
		"internal/identity/keys.go",
		"internal/observability/log.go",
		"internal/agent/server.go",
		"internal/domain/controller.go",
		"internal/storage/jsonl.go",
	} {
		if cron.Foreign(shared) {
			t.Errorf("%s was treated as a sibling's — the first component into a tenant must be able to plan it", shared)
		}
	}
	// And a singleton owns its own while still being kept out of another's.
	orch := LayoutOf(Orchestrator(), "go")
	if orch.Foreign("internal/orchestrator/registry.go") {
		t.Error("the orchestrator is foreign to its own library")
	}
	if !orch.Foreign("internal/gateway/routes.go") {
		t.Error("the orchestrator may plan the gateway's library")
	}
}

// The orchestrator serves the admin surface, so internal/admin is not another
// component's home.
//
// Fourteen of the orchestrator's twenty-one required endpoints are /v1/admin/*
// and architecture/admin.md is in its graph. platforms/go.md gives admin a row
// in its mapping table, and reading that row as "another component owns
// internal/admin" would have rejected a correct `weblisk server init` plan.
// The admin BINARY is still protected, because cmd is a family.
func TestTheOrchestratorMayPlanTheAdminSurfaceItServes(t *testing.T) {
	orch := LayoutOf(Orchestrator(), "go")
	if orch.Foreign("internal/admin/operators.go") {
		t.Error("the orchestrator was refused the admin library whose endpoints it serves")
	}
	agent := LayoutOf(Agent("billing"), "go")
	if agent.Foreign("internal/admin/operators.go") {
		t.Error("internal/admin is shared, so an agent is not refused it either")
	}
	// The binary is another matter, and the family covers it.
	if !agent.Foreign("cmd/admin/main.go") {
		t.Error("an agent may plan the admin binary's entry point")
	}
	if !orch.Foreign("cmd/admin/main.go") {
		t.Error("the orchestrator may plan the admin binary's entry point")
	}
}

// The same holds on every platform, since Others is derived from the same
// per-platform directories the component itself is placed by.
func TestSingletonHomesAreProtectedOnEveryPlatform(t *testing.T) {
	for _, tc := range []struct{ platform, orchestratorFile string }{
		{"go", "internal/orchestrator/registry.go"},
		{"node", "src/orchestrator/registry.ts"},
		{"cloudflare", "server/src/index.js"},
		{"rust", "server/src/main.rs"},
	} {
		l := LayoutOf(Agent("cron"), tc.platform)
		if !l.Foreign(tc.orchestratorFile) {
			t.Errorf("%s: %s is not foreign to an agent (others=%v)",
				tc.platform, tc.orchestratorFile, l.Others)
		}
	}
}
