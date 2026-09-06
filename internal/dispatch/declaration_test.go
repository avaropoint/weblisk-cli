package dispatch

// The phase-1 exit condition: a declaration block reproduces exactly what the ten
// scattered parsers produced.
//
// If it does not, the idea is wrong and it has cost one file. That is the whole
// reason the prototype is one blueprint and not ninety.

import (
	"sort"
	"strings"
	"testing"
)

func TestADeclarationReproducesTheScatteredParsers(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol"})
	body, ok := bps["architecture/orchestrator.md"]
	if !ok {
		t.Skip("architecture/orchestrator.md not present")
	}
	c, err := ExtractDeclaration("architecture/orchestrator.md", body)
	if err != nil {
		t.Fatalf("the declaration does not parse: %v", err)
	}
	if c == nil {
		t.Fatal("architecture/orchestrator.md has no declaration block")
	}

	// The declaration is COMPLETE, rather than compared against a section it
	// has replaced. That section is gone: a migrated blueprint removes what the
	// declaration supersedes, in the same edit. The phase-1 comparison served
	// its purpose and cannot be run twice.
	if len(c.Bindings()) == 0 {
		t.Error("the declaration binds nothing")
	}
	if len(c.Serves) == 0 {
		t.Error("the declaration serves nothing")
	}
	for _, b := range c.Bindings() {
		if b.From == "" || b.Type == "" {
			t.Errorf("incomplete binding: %+v", b)
		}
	}

	// ENDPOINTS — declaration vs the table reader.
	oldEps := ExtractEndpointOperations(body)
	var gotEps, wantEps []string
	for _, e := range c.EndpointOperations() {
		gotEps = append(gotEps, e.Wire()+" "+e.Operation)
	}
	for _, e := range oldEps {
		wantEps = append(wantEps, e.Wire()+" "+e.Operation)
	}
	sort.Strings(gotEps)
	sort.Strings(wantEps)
	if !equal(gotEps, wantEps) {
		t.Errorf("endpoints differ.\n declaration: %d\n parsers:  %d", len(gotEps), len(wantEps))
		for i := range wantEps {
			if i >= len(gotEps) || gotEps[i] != wantEps[i] {
				t.Errorf("  first divergence: declaration=%q parsers=%q", at(gotEps, i), wantEps[i])
				break
			}
		}
	}

	// The checklist is NOT compared. It stays in prose, in every blueprint
	// including this one — a declaration carries what was SCATTERED, and the
	// Verification Checklist never was.
	t.Logf("declaration reproduces: %d bindings, %d endpoints",
		len(c.Bindings()), len(c.Serves))
}

// A declaration carries auth at a precision the tables could not.
//
// The Auth column conflated "no token because the manifest signature IS the
// credential" with "no authentication at all", and the administrative table
// used a different column entirely. One declared field distinguishes them.
func TestTheDeclarationDistinguishesIdentityFromNoAuth(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture"})
	c, err := ExtractDeclaration("architecture/orchestrator.md", bps["architecture/orchestrator.md"])
	if err != nil || c == nil {
		t.Skip("no declaration")
	}
	want := map[string]string{
		"POST /v1/register":              "identity",
		"DELETE /v1/register":            "token",
		"GET /v1/admin/overview":         "admin:read",
		"POST /v1/admin/operators/token": "none",
	}
	got := map[string]string{}
	for _, s := range c.Serves {
		got[s.Wire()] = s.Auth
	}
	for wire, auth := range want {
		if got[wire] != auth {
			t.Errorf("%s: auth = %q, want %q", wire, got[wire], auth)
		}
	}
}

// A blueprint without a declaration is not an error — it has not migrated, and the
// old parsers answer for it. That is what makes the migration reversible.
func TestAnUnmigratedBlueprintIsNotAFault(t *testing.T) {
	c, err := ExtractDeclaration("x.md", "# Something\n\nNo declaration here.\n")
	if err != nil {
		t.Fatalf("an absent declaration was reported as an error: %v", err)
	}
	if c != nil {
		t.Fatal("a declaration was invented from a blueprint that has none")
	}
	if HasDeclaration("# Something\n") {
		t.Error("HasDeclaration is true for a blueprint with none")
	}
}

func names(bs []Binding) []string {
	var out []string
	for _, b := range bs {
		out = append(out, b.From+"/"+b.Type+"["+strings.Join(b.FieldsUsed, ",")+"]")
	}
	sort.Strings(out)
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "(missing)"
}

// A declaration SEEDS the endpoint set; it does not add to it.
//
// The first real build on a declaration reported 24 endpoints where there are 17.
// The declaration carries every endpoint the component serves — including the
// seven protocol/spec names — and appending both counted those seven twice.
func TestADeclarationDoesNotDoubleCountProtocolEndpoints(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol", "platforms"})
	g := &BlueprintGraph{Map: bps, Order: sortedKeys(bps)}
	req := GatherRequirements(g, "orchestrator")
	if !req.FromDeclaration {
		t.Skip("architecture/orchestrator has no declaration")
	}
	seen := map[string]int{}
	for _, e := range req.Endpoints {
		seen[e]++
	}
	for e, n := range seen {
		if n > 1 {
			t.Errorf("%s counted %d times", e, n)
		}
	}
	if len(req.Endpoints) != len(req.EndpointOps) {
		t.Errorf("%d endpoints from %d declaration entries — the two must agree",
			len(req.Endpoints), len(req.EndpointOps))
	}
	t.Logf("%d endpoints, no duplicates", len(req.Endpoints))
}

// An endpoint protocol/spec requires that a declaration omits is a HOLE in the
// declaration, reported rather than quietly filled in from the sections the
// declaration replaced. A declaration completed by what it replaced is not a
// declaration.
func TestADeclarationOmissionIsReportedNotFilled(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol", "platforms"})
	// Remove an endpoint from the declaration and confirm it is reported.
	body := bps["architecture/orchestrator.md"]
	stripped := strings.Replace(body,
		`    - method: GET
      path: "/v1/health"`,
		`    - method: GET
      path: "/v1/health-REMOVED"`, 1)
	if stripped == body {
		t.Skip("the declaration's health entry was not found in the expected form")
	}
	bps["architecture/orchestrator.md"] = stripped
	g := &BlueprintGraph{Map: bps, Order: sortedKeys(bps)}
	req := GatherRequirements(g, "orchestrator")
	if len(req.DeclarationOmissions) == 0 {
		t.Fatal("an endpoint the protocol requires was missing from the declaration and was not reported")
	}
	t.Logf("reported: %v", req.DeclarationOmissions)
}
