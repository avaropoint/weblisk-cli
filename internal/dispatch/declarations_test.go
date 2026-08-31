package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCatchesTheRealDuplicatesFromTheFirstRun replays the hub that produced 73
// build errors. Layer 2 must name the collisions the compiler found.
func TestCatchesTheRealDuplicatesFromTheFirstRun(t *testing.T) {
	dir := os.Getenv("WL_TEST_GENERATED_HUB")
	if dir == "" {
		t.Skip("set WL_TEST_GENERATED_HUB to a generated server/ directory")
	}
	byFile := map[string][]Declaration{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skip(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			continue
		}
		byFile[e.Name()] = ExtractDeclarations(e.Name(), string(b))
	}
	dupes := DuplicateDeclarations(byFile)
	if len(dupes) == 0 {
		t.Fatal("no duplicates found in a hub the compiler rejected for redeclaration")
	}
	for _, known := range []string{"writeCanonical", "TokenTTL", "ProtocolVersion"} {
		if _, found := dupes[known]; !found {
			t.Errorf("%s was declared twice in that build and was not detected", known)
		}
	}
	t.Logf("detected %d duplicate symbols", len(dupes))
}

func TestGoSignaturesAreExtractedNotJustNames(t *testing.T) {
	// Names alone leave the 19 signature mismatches unfixable: main.go called
	// NewOrchestrator(OrchestratorConfig) while orchestrator.go defined
	// NewOrchestrator(int, *SigningKeyPair, string).
	src := `package main

type Orchestrator struct{ port int }

const TokenTTL = 3600

func NewOrchestrator(port int, key *SigningKeyPair, dir string) *Orchestrator { return nil }

func (o *Orchestrator) Start() error { return nil }
`
	decls := ExtractDeclarations("orchestrator.go", src)
	byName := map[string]string{}
	for _, d := range decls {
		byName[d.Name] = d.Signature
	}
	if !strings.Contains(byName["NewOrchestrator"], "port int") ||
		!strings.Contains(byName["NewOrchestrator"], "*SigningKeyPair") {
		t.Errorf("signature not reproduced: %q", byName["NewOrchestrator"])
	}
	if !strings.Contains(byName["NewOrchestrator"], "*Orchestrator") {
		t.Errorf("result type missing: %q", byName["NewOrchestrator"])
	}
	// The receiver TYPE is what a caller needs; its variable name carries none.
	if !strings.Contains(byName["Start"], "*Orchestrator") || !strings.Contains(byName["Start"], "Start()") {
		t.Errorf("method receiver type missing: %q", byName["Start"])
	}
	if byName["TokenTTL"] == "" {
		t.Error("const not reported — consts collide exactly as funcs do")
	}
}

func TestUnparseableSourceYieldsNothing(t *testing.T) {
	// A partial list from a broken parse tells the model something false, and it
	// will believe it.
	if d := ExtractDeclarations("x.go", "package main\nfunc broken( {"); len(d) != 0 {
		t.Errorf("declarations were reported from unparseable source: %v", d)
	}
}

func TestUnexportedSymbolsAreIncluded(t *testing.T) {
	// The redeclarations that broke the build were lower-case helpers.
	d := ExtractDeclarations("helpers.go", "package main\n\nfunc writeCanonical() {}\n")
	if len(d) != 1 || d[0].Name != "writeCanonical" {
		t.Errorf("unexported declaration missed: %v", d)
	}
}

func TestChecklistIsExtractedAndCounted(t *testing.T) {
	bp, err := os.ReadFile("../../../weblisk-blueprints/platforms/go.md")
	if err != nil {
		t.Skipf("blueprints unavailable: %v", err)
	}
	items := ExtractChecklist("platforms/go.md", string(bp))
	if len(items) < 5 {
		t.Fatalf("got %d assertions, the schema requires at least 5", len(items))
	}
	found := false
	for _, i := range items {
		if strings.Contains(strings.ToLower(i.Text), "retry-after") {
			found = true
		}
	}
	if !found {
		t.Error("the Retry-After assertion — the one that silently failed — was not extracted")
	}
}

func TestUncheckedIsNeverReportedAsPassed(t *testing.T) {
	items := []ChecklistItem{
		{Source: "x", Text: "io.LimitReader is applied on all request body reads"},
		{Source: "x", Text: "Something no mechanical check can evaluate at all"},
	}
	results := EvaluateChecklist(items, []GeneratedFile{{Content: "io.LimitReader(r.Body, 1024)"}})
	passed, failed, unchecked := ChecklistSummary(results)
	if passed != 1 || failed != 0 || unchecked != 1 {
		t.Errorf("summary = %d passed, %d failed, %d unchecked; want 1/0/1", passed, failed, unchecked)
	}
	for _, r := range results {
		if !r.Checked && r.Passed {
			t.Error("an unchecked assertion was marked passed")
		}
	}
}

func TestTheRetryAfterFailureIsDetected(t *testing.T) {
	// The specific assertion that failed silently on the first run.
	items := []ChecklistItem{{Source: "platforms/go.md",
		Text: "Concurrency limiter returns 429 with Retry-After header when agent is at capacity"}}
	results := EvaluateChecklist(items, []GeneratedFile{{Content: "package main\n// no such header\n"}})
	if len(results) != 1 || !results[0].Checked || results[0].Passed {
		t.Errorf("the Retry-After assertion was not evaluated as a failure: %+v", results)
	}
}

// TestMethodsOnDifferentTypesDoNotCollide is the false positive that blocked a
// run in which all ten files had generated correctly: two stores implementing
// the same interface, and two types implementing error.
func TestMethodsOnDifferentTypesDoNotCollide(t *testing.T) {
	store := `package main

type SQLiteStore struct{}
type MemStore struct{}

func (s *SQLiteStore) LoadAgents() error { return nil }
func (m *MemStore) LoadAgents() error    { return nil }
`
	events := "package main\n\ntype EventError struct{}\n\nfunc (e *EventError) Error() string { return \"\" }\n"
	registry := "package main\n\ntype RegistryError struct{}\n\nfunc (r *RegistryError) Error() string { return \"\" }\n"

	byFile := map[string][]Declaration{
		"store.go":    ExtractDeclarations("store.go", store),
		"events.go":   ExtractDeclarations("events.go", events),
		"registry.go": ExtractDeclarations("registry.go", registry),
	}
	if dupes := DuplicateDeclarations(byFile); len(dupes) > 0 {
		t.Errorf("legal Go was reported as redeclaration: %v", dupes)
	}
}

func TestARealRedeclarationIsStillCaught(t *testing.T) {
	// The check must not have been loosened into uselessness: the original fault
	// was a plain function declared in two files.
	a := "package main\n\nfunc writeCanonical() {}\n"
	b := "package main\n\nfunc writeCanonical() {}\n"
	dupes := DuplicateDeclarations(map[string][]Declaration{
		"identity.go": ExtractDeclarations("identity.go", a),
		"helpers.go":  ExtractDeclarations("helpers.go", b),
	})
	if _, found := dupes["writeCanonical"]; !found {
		t.Errorf("a genuine redeclaration was missed: %v", dupes)
	}
}

func TestTheSameMethodOnTheSameTypeInTwoFilesIsCaught(t *testing.T) {
	// Receiver-qualifying must not hide a real collision.
	a := "package main\n\ntype S struct{}\n\nfunc (s *S) Run() {}\n"
	b := "package main\n\nfunc (s *S) Run() {}\n"
	dupes := DuplicateDeclarations(map[string][]Declaration{
		"a.go": ExtractDeclarations("a.go", a),
		"b.go": ExtractDeclarations("b.go", b),
	})
	if _, found := dupes["S.Run"]; !found {
		t.Errorf("the same method on the same type in two files was missed: %v", dupes)
	}
}

// TestStructFieldsReachOtherFiles is the fifty-error run: channel.go used
// target.Name six times against an AgentEntry that has no Name field, because it
// was told the type existed and never what was in it.
func TestStructFieldsReachOtherFiles(t *testing.T) {
	src := `package main

import "time"

type AgentEntry struct {
	AgentID   string
	URL       string
	PublicKey string
	LastSeen  time.Time
}

type ChannelEntry struct {
	ChannelID string
	TTL       int
}
`
	decls := ExtractDeclarations("registry.go", src)
	byName := map[string]string{}
	for _, d := range decls {
		byName[d.Name] = d.Signature
	}
	for _, field := range []string{"AgentID", "URL", "PublicKey", "LastSeen"} {
		if !strings.Contains(byName["AgentEntry"], field) {
			t.Errorf("field %s is not visible to other files: %q", field, byName["AgentEntry"])
		}
	}
	// And the absence is visible too — which is what stops a guess at .Name.
	if strings.Contains(byName["AgentEntry"], "Name") {
		t.Errorf("a field that does not exist appeared: %q", byName["AgentEntry"])
	}
	if !strings.Contains(byName["ChannelEntry"], "ChannelID") {
		t.Errorf("ChannelEntry fields missing: %q", byName["ChannelEntry"])
	}
}

func TestInterfaceMethodsAreVisible(t *testing.T) {
	src := "package main\n\ntype Store interface {\n\tLoad(id string) error\n\tSave(id string) error\n}\n"
	decls := ExtractDeclarations("store.go", src)
	sig := ""
	for _, d := range decls {
		if d.Name == "Store" {
			sig = d.Signature
		}
	}
	if !strings.Contains(sig, "Load") || !strings.Contains(sig, "Save") {
		t.Errorf("interface method set not visible: %q", sig)
	}
}

func TestNestedTypesStayCompact(t *testing.T) {
	// A caller needs a map's key and value types, not a recursive expansion.
	src := "package main\n\ntype Registry struct {\n\tagents map[string]*AgentEntry\n}\n"
	decls := ExtractDeclarations("r.go", src)
	sig := decls[0].Signature
	if !strings.Contains(sig, "map[string]*AgentEntry") {
		t.Errorf("field type lost: %q", sig)
	}
}
