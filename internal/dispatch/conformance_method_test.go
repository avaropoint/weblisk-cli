package dispatch

// A conformance test must ask the question the specification asks.
//
// These pin the fault a real run surfaced: a conformant agent was reported
// non-conformant twice, because the harness issued GET against endpoints the
// blueprints declare as POST and got 405 — neither 200 nor 401 — and read that
// as the component misbehaving.

import (
	"strings"
	"testing"
)

// /v1/health is POST on an agent and GET on the orchestrator.
// architecture/agent.md states it in a row and again in prose: "POST /v1/health
// rather than GET is deliberate and is the protocol's choice."
func TestHealthIsProbedWithTheMethodTheBlueprintDeclares(t *testing.T) {
	bps := map[string]string{
		"architecture/agent.md": "" +
			"| Method | Path | Operation | Auth | Description |\n" +
			"|---|---|---|---|---|\n" +
			"| POST | /v1/health | Health | no | Health check |\n",
		"architecture/orchestrator.md": "" +
			"| Method | Path | Operation | Auth | Description |\n" +
			"|---|---|---|---|---|\n" +
			"| GET | /v1/health | Health | no | Health check |\n",
	}
	if got := declaredMethodFor("agent", "/v1/health", bps); got != "POST" {
		t.Errorf("agent health method = %q, want POST — GET gets 405 from every conformant agent", got)
	}
	if got := declaredMethodFor("orchestrator", "/v1/health", bps); got != "GET" {
		t.Errorf("orchestrator health method = %q, want GET", got)
	}
	// A corpus that says nothing yields nothing, so the caller can choose a
	// default rather than be handed a guess.
	if got := declaredMethodFor("agent", "/v1/nothing", bps); got != "" {
		t.Errorf("an undeclared path reported method %q", got)
	}
	if got := declaredMethodFor("content", "/v1/health", nil); got != "" {
		t.Errorf("an absent corpus reported method %q", got)
	}
}

// The protected-endpoint probe keeps each endpoint's own method.
//
// ProtectedGETsFor discarded every non-GET entry, so an agent — whose
// /v1/services is POST — produced an empty list and fell through to a
// hardcoded fallback.
func TestProtectedEndpointsKeepTheirMethod(t *testing.T) {
	bps := map[string]string{
		"architecture/agent.md": "" +
			"| Method | Path | Operation | Auth | Description |\n" +
			"|---|---|---|---|---|\n" +
			"| POST | /v1/services | Services | yes | Service directory |\n" +
			"| POST | /v1/health | Health | no | Health check |\n",
	}
	got := protectedEndpoints("agent", bps)
	if len(got) != 1 {
		t.Fatalf("protected endpoints = %+v, want exactly /v1/services", got)
	}
	if got[0].Method != "POST" || got[0].Path != "/v1/services" {
		t.Errorf("got %s %s, want POST /v1/services", got[0].Method, got[0].Path)
	}
}

// One component is never probed with another's surface.
//
// The fallback was the orchestrator's list — /v1/services, /v1/audit,
// /v1/admin/overview — handed to whatever was under test and probed with GET.
// This function's own comment already stated the rule that broke.
func TestNoComponentIsProbedWithAnothersSurface(t *testing.T) {
	if got := protectedEndpoints("agent", nil); len(got) != 0 {
		t.Errorf("with no corpus, an agent was handed %d endpoints to probe: %+v", len(got), got)
	}
	empty := map[string]string{"architecture/agent.md": "no tables here\n"}
	if got := protectedEndpoints("agent", empty); len(got) != 0 {
		t.Errorf("with no declared table, an agent was handed %+v", got)
	}
}

// A test that could not ask its question is unrun, not failed.
func TestAProbeOfNothingIsUnrunNotFailed(t *testing.T) {
	var l17 conformanceTest
	for _, tc := range l1Tests {
		if tc.id == "L1-07" {
			l17 = tc
		}
	}
	if l17.run == nil {
		t.Fatal("L1-07 has no harness")
	}
	// No corpus: nothing declares a protected endpoint.
	ok, detail, _ := l17.run("http://127.0.0.1:1", "agent", nil)
	if ok {
		t.Error("a probe of nothing reported a pass")
	}
	stripped, unresolved := splitInconclusive(detail)
	if !unresolved {
		t.Errorf("a probe of nothing was reported as a failure, which fails a correct "+
			"implementation for want of a list: %q", detail)
	}
	if !strings.Contains(stripped, "declares no protected endpoint") {
		t.Errorf("the reason does not say why nothing was probed: %q", stripped)
	}
}

// A required capability is an auth requirement.
//
// architecture/orchestrator.md declares its admin surface in a second table
// headed Capability rather than Auth, with cells like `admin:read`. Reading only
// Auth-headed tables left every admin endpoint invisible to the protection
// probe on the real corpus.
func TestACapabilityColumnMarksAnEndpointProtected(t *testing.T) {
	got := ProtectedEndpointsFor("orchestrator", fixtureCorpus())
	have := map[string]bool{}
	for _, e := range got {
		have[e.Method+" "+e.Path] = true
	}
	for _, want := range []string{"GET /v1/services", "GET /v1/audit", "GET /v1/admin/overview"} {
		if !have[want] {
			t.Errorf("%s is not reported as protected: %+v", want, got)
		}
	}
	if have["GET /v1/health"] {
		t.Error("/v1/health, declared Auth=no, was reported as protected")
	}
}
