package dispatch

import "testing"

func TestTokenEndpointIsRequiredOfTheOrchestrator(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol", "patterns", "platforms"})
	g := &BlueprintGraph{Map: bps, Order: []string{"architecture/orchestrator.md", "protocol/spec.md"}}
	req := GatherRequirements(g, "orchestrator")
	var found bool
	for _, e := range req.Endpoints {
		if e == "POST /v1/admin/operators/token" {
			found = true
		}
	}
	if !found {
		t.Errorf("the token endpoint is not required of the orchestrator; endpoints=%v", req.Endpoints)
	}
	// It must NOT be read as token-protected: it is what issues tokens.
	for _, p := range ProtectedGETsFor("orchestrator", bps) {
		if p == "/v1/admin/operators/token" {
			t.Error("the token endpoint was read as protected")
		}
	}
}
