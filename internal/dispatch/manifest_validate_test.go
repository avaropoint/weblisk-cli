package dispatch

import (
	"os"
	"strings"
	"testing"
)

func loadSpec(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../../weblisk-blueprints/protocol/spec.md")
	if err != nil {
		t.Skipf("blueprints not checked out beside the CLI: %v", err)
	}
	return string(b)
}

// TestEndpointsAreReadFromTheSpec — the list must come from protocol/spec.md, not
// from a copy in the checker. A copy is the one nobody notices is stale.
func TestEndpointsAreReadFromTheSpec(t *testing.T) {
	eps := OrchestratorEndpoints(loadSpec(t))
	if len(eps) == 0 {
		t.Fatal("no orchestrator endpoints parsed from protocol/spec.md")
	}
	want := map[string]bool{
		"POST /v1/register": true, "DELETE /v1/register": true,
		"GET /v1/services": true, "POST /v1/channel": true,
		"POST /v1/rotate-key": true, "GET /v1/health": true, "GET /v1/audit": true,
	}
	got := map[string]bool{}
	for _, e := range eps {
		got[e] = true
	}
	for e := range want {
		if !got[e] {
			t.Errorf("spec defines %s but it was not parsed", e)
		}
	}
}

// TestTheShippedGoManifestPassesEveryRule is the end-to-end check: the artifact
// we ship satisfies the schema we wrote for it.
func TestTheShippedGoManifestPassesEveryRule(t *testing.T) {
	bp, err := os.ReadFile("../../../weblisk-blueprints/platforms/go.md")
	if err != nil {
		t.Skipf("blueprints not available: %v", err)
	}
	man, err := ExtractManifest(string(bp))
	if err != nil || man == nil {
		t.Fatalf("go manifest did not parse: %v", err)
	}
	issues, rule4 := ValidateManifest(man, loadSpec(t))
	if !rule4 {
		t.Error("rule 4 was not checked — an unrun check is not a pass")
	}
	for _, i := range issues {
		t.Errorf("shipped Go manifest violates %s", i)
	}
}

func TestMissingCoverageIsRule4(t *testing.T) {
	man, err := ParseManifest("generate:\n  orchestrator:\n    root: server\n    build: go build\n" +
		"    files:\n      - path: main.go\n        purpose: entry\n        must_serve: [GET /v1/health]\n" +
		"    conformance: [L1]\n")
	if err != nil {
		t.Fatal(err)
	}
	issues, checked := ValidateManifest(man, loadSpec(t))
	if !checked {
		t.Fatal("rule 4 not checked")
	}
	found := false
	for _, i := range issues {
		if i.Rule == 4 && strings.Contains(i.Detail, "/v1/register") {
			found = true
		}
	}
	if !found {
		t.Errorf("incomplete coverage was not reported as rule 4: %v", issues)
	}
}

func TestInventedEndpointIsRule3(t *testing.T) {
	// A manifest must not become a second source of truth for the wire contract.
	man, err := ParseManifest("generate:\n  orchestrator:\n    root: server\n    build: go build\n" +
		"    files:\n      - path: main.go\n        purpose: entry\n        must_serve: [POST /v1/invented]\n" +
		"    conformance: [L1]\n")
	if err != nil {
		t.Fatal(err)
	}
	issues, _ := ValidateManifest(man, loadSpec(t))
	found := false
	for _, i := range issues {
		if i.Rule == 3 && strings.Contains(i.Detail, "invented") {
			found = true
		}
	}
	if !found {
		t.Errorf("an invented endpoint was not reported as rule 3: %v", issues)
	}
}

func TestRule4IsReportedUncheckedWithoutTheSpec(t *testing.T) {
	// Reporting an unrun check as a pass is the failure this whole session keeps
	// finding. Absence of the spec must read as "not checked", never "fine".
	man, err := ParseManifest("generate:\n  orchestrator:\n    root: server\n    build: go build\n" +
		"    files:\n      - path: main.go\n        purpose: entry\n    conformance: [L1]\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, checked := ValidateManifest(man, ""); checked {
		t.Error("rule 4 reported as checked with no protocol spec supplied")
	}
}

func TestConformanceMustIncludeL1(t *testing.T) {
	man, err := ParseManifest("generate:\n  orchestrator:\n    root: server\n    build: go build\n" +
		"    files:\n      - path: main.go\n        purpose: entry\n    conformance: [L2]\n")
	if err != nil {
		t.Fatal(err)
	}
	issues, _ := ValidateManifest(man, "")
	found := false
	for _, i := range issues {
		if i.Rule == 6 {
			found = true
		}
	}
	if !found {
		t.Error("conformance without L1 was accepted")
	}
}
