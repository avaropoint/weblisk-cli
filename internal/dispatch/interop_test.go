package dispatch

import "testing"

// The capability vocabulary must come from protocol/types.md, not from a list
// in the tooling. A hardcoded copy is the tooling deciding what a component may
// ask for, which is the blueprint's decision.
func TestStandardCapabilitiesAreReadFromTheBlueprint(t *testing.T) {
	bps := readBlueprints(t, []string{"protocol"})
	caps := standardCapabilitiesFrom(bps)
	if len(caps) < 10 {
		t.Fatalf("only %d capabilities read from protocol/types.md — the extractor is not matching", len(caps))
	}
	for _, want := range []string{"agent:message", "file:read", "content:read", "content:write", "admin:read"} {
		if !caps[want] {
			t.Errorf("%q is declared in protocol/types.md and was not read", want)
		}
	}
	if caps["content.read"] {
		t.Error("a dotted name was accepted; capability names are family:verb")
	}
}

// An empty corpus must not silently accept everything.
func TestNoBlueprintYieldsNoVocabulary(t *testing.T) {
	if got := standardCapabilitiesFrom(nil); len(got) != 0 {
		t.Errorf("got %d capabilities from no blueprints", len(got))
	}
}

// The orchestrator is the registry and does not register with itself.
func TestOrchestratorHasNoInteropTests(t *testing.T) {
	if got := l4Tests("orchestrator"); len(got) != 0 {
		t.Errorf("orchestrator got %d L4 tests; it is the registry", len(got))
	}
	if got := l4Tests("content"); len(got) != 4 {
		t.Errorf("content got %d L4 tests, want 4", len(got))
	}
}

// A ScopeLevel is never an audience.
func TestScopeLevelIsNotAnAudience(t *testing.T) {
	for _, lvl := range []string{"public", "internal", "confidential", "restricted", "critical"} {
		if !isScopeLevel(lvl) {
			t.Errorf("%q is a ScopeLevel and was not recognised as one", lvl)
		}
	}
	for _, aud := range []string{"self", "*", "lifecycle-agent"} {
		if isScopeLevel(aud) {
			t.Errorf("%q is an audience and was misread as a ScopeLevel", aud)
		}
	}
}
