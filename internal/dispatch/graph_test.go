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
	g, err := ResolveGraph(root, "orchestrator", "go")
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
	g, err := ResolveGraph(root, "orchestrator", "go")
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
	g, err := ResolveGraph(root, "orchestrator", "go")
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
	g, err := ResolveGraph(root, "orchestrator", "go")
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
