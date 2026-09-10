package dispatch

// The plan, the prompts and the checklist come from ONE blueprint set.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// graphFixture writes a small blueprint tree with a real requires: chain.
func graphFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"blueprints/platforms/go.md": `<!--
requires: [protocol/identity, architecture/orchestrator, architecture/agent]
-->
# Go
## Verification Checklist
- [ ] go vet passes
`,
		"blueprints/protocol/spec.md": `<!--
requires: []
-->
# Protocol
### POST /v1/event
### Orchestrator Endpoints
#### POST /v1/register
## Verification Checklist
- [ ] every endpoint answers
`,
		"blueprints/architecture/orchestrator.md": `<!--
requires: [protocol/identity, protocol/types, architecture/storage]
-->
# Orchestrator
## Verification Checklist
- [ ] the registry survives restart
`,
		"blueprints/architecture/storage.md": `<!--
requires: [protocol/types]
-->
# Storage
## Verification Checklist
- [ ] AgentEntry is persisted with its manifest
- [ ] writes are atomic
`,
		"blueprints/protocol/types.md": `<!--
requires: []
-->
# Types
### AgentEntry
## Verification Checklist
- [ ] every type round-trips
`,
		"blueprints/protocol/identity.md": `<!--
requires: []
-->
# Identity
## Verification Checklist
- [ ] keys are ML-DSA-65
`,
		// Present but NOT declared by the orchestrator — a starter hub is not the
		// whole framework, and its checklist must stay out.
		"blueprints/architecture/agent.md": `<!--
requires: []
-->
# Agent
## Verification Checklist
- [ ] the agent reconnects with backoff
`,
	}
	for name, body := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WL_BLUEPRINT_OFFLINE", "1")
	return root
}

func TestChecklistCoversEverySentBlueprint(t *testing.T) {
	// The fault: generation sent architecture/storage.md and the checklist came
	// from a different list that omitted it. Fourteen assertions defining the
	// store contract were sent to the model and excluded from what the loop
	// terminated on — the pipeline graded against a narrower specification than
	// the one it built from.
	root := graphFixture(t)
	g, err := ResolveGraph(root, Orchestrator(), "go")
	if err != nil {
		t.Fatal(err)
	}
	req := GatherRequirements(g, "orchestrator")

	sources := map[string]bool{}
	for _, c := range req.Checklist {
		sources[c.Source] = true
	}
	for _, name := range g.Order {
		if !sources[name] {
			t.Errorf("%s was sent to the model and its assertions are not in the checklist", name)
		}
	}
	if !sources["architecture/storage.md"] {
		t.Error("the store contract's assertions are missing — the exact omission this guards")
	}
}

func TestStarterHubDoesNotInheritTheWholeFramework(t *testing.T) {
	// Depth one from the TARGET, not the platform. The platform blueprint's
	// requires list covers every component type it can describe; following it
	// while building a starter hub pulls in agent, domain, gateway and lifecycle.
	root := graphFixture(t)
	g, err := ResolveGraph(root, Orchestrator(), "go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range g.Order {
		if name == "architecture/agent.md" {
			t.Error("architecture/agent.md is in a starter hub's graph — the platform's requires were followed")
		}
	}
	// And what it must contain: the store contract, and the type definitions.
	for _, want := range []string{"architecture/storage.md", "protocol/types.md"} {
		if _, ok := g.Map[want]; !ok {
			t.Errorf("%s missing from the graph", want)
		}
	}

	req := GatherRequirements(g, "orchestrator")
	for _, c := range req.Checklist {
		if strings.Contains(c.Text, "reconnects with backoff") {
			t.Error("an undeclared blueprint's assertion entered the checklist")
		}
	}
}

func TestGraphReportsWhichCopyItRead(t *testing.T) {
	root := graphFixture(t)
	g, err := ResolveGraph(root, Orchestrator(), "go")
	if err != nil {
		t.Fatal(err)
	}
	d := g.Describe()
	if !strings.Contains(d, "Read from:") {
		t.Fatal("the graph does not report its sources")
	}
	if !strings.Contains(d, filepath.Join(root, "blueprints")) {
		t.Fatalf("the serving directory is not named: %q", d)
	}
	for _, name := range g.Order {
		if !strings.Contains(d, name) {
			t.Errorf("%s is in the graph and not in its description", name)
		}
	}
}

func TestPlanAndPromptsShareOneCorpus(t *testing.T) {
	// Joined() is what the planner sees. If it is not the graph, the plan that
	// decides which files exist is made from a different specification than the
	// files are generated from.
	root := graphFixture(t)
	g, err := ResolveGraph(root, Orchestrator(), "go")
	if err != nil {
		t.Fatal(err)
	}
	joined := g.Joined()
	for _, name := range g.Order {
		body := g.Map[name]
		if !strings.Contains(joined, body) {
			t.Errorf("%s is sent per-file and absent from the planning corpus", name)
		}
	}
}

func TestMixedSourcesAreAttributedPerBlueprint(t *testing.T) {
	// The case a list of directories cannot express: a project overriding two
	// files while the cached repo supplies the rest. "Read from: A, B" is true
	// and useless — the question is always which copy answered for THIS file.
	root := graphFixture(t)

	// Move all but two blueprints into a second source, and point the resolver at
	// it, so the graph must be assembled from both.
	cache := t.TempDir()
	for _, name := range []string{"protocol/types.md", "protocol/identity.md", "architecture/storage.md", "protocol/spec.md"} {
		body, err := os.ReadFile(filepath.Join(root, "blueprints", name))
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(cache, name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(root, "blueprints", name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WL_BLUEPRINT_SOURCES", "file://"+cache)

	// Resolution reads custom sources through the git cache, which this fixture
	// has no remote for; construct the graph against both directories directly.
	srcs := []Source{
		{Dir: filepath.Join(root, "blueprints"), Kind: "project"},
		{Dir: cache, Kind: "custom", Origin: "file://" + cache},
	}
	g := &BlueprintGraph{Order: []string{"platforms/go.md", "protocol/types.md"}, ServedBy: map[string]Source{}, Sources: srcs}
	g.Map = map[string]string{"platforms/go.md": "x", "protocol/types.md": "y"}
	for _, name := range g.Order {
		for _, s := range g.Sources {
			if _, err := os.Stat(filepath.Join(s.Dir, name)); err == nil {
				g.ServedBy[name] = s
				break
			}
		}
	}

	if got := g.ServedBy["platforms/go.md"].Kind; got != "project" {
		t.Errorf("platforms/go.md attributed to %q, want project", got)
	}
	if got := g.ServedBy["protocol/types.md"].Kind; got != "custom" {
		t.Errorf("protocol/types.md attributed to %q, want custom", got)
	}

	d := g.Describe()
	// Every blueprint carries a source tag, and every source states its share —
	// a "0 of 6" is what makes a shadowed cache visible at a glance.
	if !strings.Contains(d, "[1] platforms/go.md") || !strings.Contains(d, "[2] protocol/types.md") {
		t.Fatalf("blueprints are not attributed to their source:\n%s", d)
	}
	if !strings.Contains(d, "1 of 2") {
		t.Fatalf("a source does not state how much of the graph it served:\n%s", d)
	}
}

func TestProseBeforeThePackageClauseIsNotGoSource(t *testing.T) {
	// The multiline anchor that lets a doc comment precede the package clause
	// also let a whole markdown answer precede it.
	bad := []string{
		"# Orchestrator\n\npackage main\n\nfunc main() {}\n",
		"Here is the file you asked for:\n\npackage main\n",
		"```go\npackage main\n```\n",
	}
	for _, src := range bad {
		if notSourceIn("main.go", src) == "" {
			t.Errorf("accepted as Go source:\n%s", src)
		}
	}

	// And everything Go actually permits must still pass — a pre-filter that
	// rejects valid source is worse than the failure it prevents.
	good := []string{
		"package main\n",
		"// Package main is the orchestrator.\npackage main\n",
		"//go:build linux\n\npackage main\n",
		"/*\nCopyright 2026.\n*/\n\npackage main\n",
		"/* one line */ package main\n",
		"\n\n\npackage main\n",
	}
	for _, src := range good {
		if why := notSourceIn("main.go", src); why != "" {
			t.Errorf("rejected valid Go (%s):\n%s", why, src)
		}
	}
}

func TestAnotherComponentsAssertionsAreExcludedAndReported(t *testing.T) {
	// protocol/spec.md describes both ends of the conversation. Its checklist
	// contains "Agent responds to POST /v1/describe", which an orchestrator does
	// not and must not serve. Handing those to an orchestrator's checklist makes
	// the route check report ten failures that are correct behaviour, and the
	// conformance loop then repairs toward implementing an agent.
	spec := `<!--
requires: []
-->
# Protocol
### Orchestrator Endpoints
#### POST /v1/register
## Verification Checklist

- [ ] An ungrouped assertion applies to everything

### Agent Protocol
- [ ] Agent responds to ` + "`POST /v1/describe`" + ` with a valid manifest
- [ ] Agent accepts ` + "`POST /v1/event`" + ` and dispatches to handlers

### Orchestrator Protocol
- [ ] Orchestrator ` + "`POST /v1/register`" + ` validates namespace ownership

### Event Publishing
- [ ] Framework retries failed deliveries with exponential backoff
`
	items := ExtractChecklist("protocol/spec.md", spec)
	if len(items) != 5 {
		t.Fatalf("extracted %d assertions, want 5", len(items))
	}
	// The group travels with the assertion. Only an item BEFORE the first
	// heading is ungrouped — one after a blank line is still inside its section,
	// which is what markdown means and what a reader would assume.
	if items[0].Group != "" {
		t.Errorf("an assertion before any heading carries group %q", items[0].Group)
	}
	if items[1].Group != "Agent Protocol" {
		t.Errorf("group = %q, want Agent Protocol", items[1].Group)
	}

	mine, others := ScopeChecklist(items, "orchestrator")
	if len(others) != 2 {
		t.Fatalf("excluded %d, want the 2 agent assertions: %+v", len(others), others)
	}
	if len(mine) != 3 {
		t.Fatalf("kept %d, want 3 — orchestrator, event publishing, ungrouped", len(mine))
	}
	// A shared group is NOT another component's: the orchestrator publishes
	// system.* events, so "Framework retries..." is its obligation too.
	var kept []string
	for _, m := range mine {
		kept = append(kept, m.Group)
	}
	if !containsString(kept, "Event Publishing") || !containsString(kept, "") {
		t.Errorf("a shared group was excluded: kept groups %v", kept)
	}

	// Excluded, never silent.
	if s := ExcludedSummary(others); !strings.Contains(s, "agent") || !strings.Contains(s, "2") {
		t.Errorf("exclusions are not reported with a count and an owner: %q", s)
	}

	// And from the other side: generating an agent keeps them.
	agentMine, agentOthers := ScopeChecklist(items, "agent")
	if len(agentOthers) != 1 || len(agentMine) != 4 {
		t.Errorf("for an agent: kept %d, excluded %d; want 4 and 1", len(agentMine), len(agentOthers))
	}
}

func TestTheRealSpecScopesToTheOrchestrator(t *testing.T) {
	// Against the actual blueprints, not a fixture agreeing with me.
	root := "/Users/lwilson/Projects/Avaropoint/weblisk-blueprints"
	if _, err := os.Stat(root); err != nil {
		t.Skip("blueprints not present")
	}
	spec, err := os.ReadFile(filepath.Join(root, "protocol/spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	items := ExtractChecklist("protocol/spec.md", string(spec))
	mine, others := ScopeChecklist(items, "orchestrator")
	if len(others) == 0 {
		t.Fatal("no agent assertions excluded from an orchestrator's checklist")
	}
	for _, it := range mine {
		if strings.HasPrefix(it.Text, "Agent ") {
			t.Errorf("an agent assertion survived scoping: %q (group %q)", it.Text, it.Group)
		}
	}
	// Every excluded one must be attributable, or the report is a bare number.
	for _, it := range others {
		if groupComponent(it.Group) == "" {
			t.Errorf("excluded assertion has no owning component: %q", it.Text)
		}
	}
}

func TestWhatIsDeclaredAndNotFollowedIsStated(t *testing.T) {
	// Depth one is a deliberate narrowing, and a deliberate narrowing has to be
	// visible. architecture/storage.md declares seven requirements because it
	// documents fourteen stores and names their consumers; following them would
	// pull the whole framework into a starter hub. Declining is right. Declining
	// silently is the fault that cost this pipeline protocol/types.md.
	root := graphFixture(t)
	g, err := ResolveGraph(root, Orchestrator(), "go")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's platforms/go.md declares architecture/agent, which the
	// starter hub does not follow.
	if got := g.Deferred["platforms/go.md"]; !containsString(got, "architecture/agent.md") {
		t.Errorf("platforms/go.md's unfollowed requirement is not recorded: %v", got)
	}
	// And what IS loaded is never listed as deferred.
	for name, deps := range g.Deferred {
		for _, d := range deps {
			if _, loaded := g.Map[d]; loaded {
				t.Errorf("%s lists %s as unfollowed, but it is in the graph", name, d)
			}
		}
	}
	if d := g.Describe(); !strings.Contains(d, "not followed") {
		t.Fatalf("the description does not state what was declined:\n%s", d)
	}
}
