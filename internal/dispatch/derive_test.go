package dispatch

import (
	"sort"
	"strings"
	"testing"
)

func TestProtectedPathsComeFromTheAuthColumn(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol", "patterns", "schemas"})

	content := ProtectedGETsFor("content", bps)
	if len(content) < 4 {
		t.Errorf("content: read %d protected paths from its Auth column, want its whole protected surface: %v", len(content), content)
	}
	for _, p := range content {
		if !strings.HasPrefix(p, "/v1/content") {
			t.Errorf("content: %q is not part of the content surface", p)
		}
	}
	// Health must answer without a token, so it is never protected.
	for _, p := range content {
		if p == "/v1/health" {
			t.Error("/v1/health was read as protected; it is the one endpoint that must answer unauthenticated")
		}
	}

	orch := ProtectedGETsFor("orchestrator", bps)
	if len(orch) == 0 {
		t.Error("orchestrator: no protected paths read from its Endpoints table")
	}
	// POST /v1/register is identity-verified, not token-protected: its Auth cell
	// is "no*". DELETE on the same path IS protected, so the pair matters.
	for _, p := range orch {
		if p == "/v1/register" {
			t.Error("/v1/register was offered to a GET prober; no GET row for it is protected")
		}
	}
	pairs := ProtectedEndpointsFor("orchestrator", bps)
	var sawDeleteRegister, sawPostRegister bool
	for _, e := range pairs {
		if e.Path == "/v1/register" && e.Method == "DELETE" {
			sawDeleteRegister = true
		}
		if e.Path == "/v1/register" && e.Method == "POST" {
			sawPostRegister = true
		}
	}
	if !sawDeleteRegister {
		t.Error("DELETE /v1/register is marked yes and was not read as protected")
	}
	if sawPostRegister {
		t.Error("POST /v1/register is marked no* and was read as protected")
	}
}

// Buildable is DERIVED — a component states an HTTP surface and a binding
// contract, or it is not one. Asserted as the rule rather than as a roster.
//
// It was a roster: {orchestrator, content}. Then architecture/agent gained the
// Endpoints table it had always been required to have, the agent became
// buildable, and the test failed against a corpus that had improved. A list of
// expected answers has to be edited every time the corpus is right, which is
// how a guard gets relaxed to make it pass.
func TestBuildableComponentsComeFromTheCorpus(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol"})
	got := map[string]bool{}
	for _, g := range BuildableComponents(bps) {
		got[g] = true
	}
	if len(got) == 0 {
		t.Fatal("no component was read as buildable; the derivation is not running")
	}

	for path, body := range bps {
		if !strings.HasPrefix(path, "architecture/") || strings.HasSuffix(path, "README.md") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(path, "architecture/"), ".md")
		qualifies := strings.Contains(body, "\n## Endpoints") && len(ExtractBindings(body)) > 0
		if qualifies && !got[name] {
			t.Errorf("%q states an Endpoints table and bindings and was not read as buildable", name)
		}
		if !qualifies && got[name] {
			t.Errorf("%q was read as buildable; it states no HTTP surface or no bindings", name)
		}
	}

	// The orchestrator must always qualify — if it does not, the derivation is
	// broken rather than the corpus.
	if !got["orchestrator"] {
		t.Error("the orchestrator is not buildable; the derivation is wrong")
	}
	t.Logf("buildable: %v", BuildableComponents(bps))
}

// The Go list must not drift from the declaration it mirrors.
func TestComponentGroupsMatchTheSchema(t *testing.T) {
	bps := readBlueprints(t, []string{"schemas"})
	declared := DeclaredComponentGroups(bps)
	if len(declared) == 0 {
		t.Fatal("schemas/common.md declares no component-group list, or the sentence was reworded")
	}
	have := append([]string(nil), componentGroups...)
	sort.Strings(have)
	if strings.Join(have, ",") != strings.Join(declared, ",") {
		t.Errorf("componentGroups drifted from schemas/common.md\n  Go:     %v\n  schema: %v", have, declared)
	}
}
