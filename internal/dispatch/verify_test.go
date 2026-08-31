package dispatch

// The four outcomes, and the asymmetry that makes the third one honest.

import (
	"strings"
	"testing"
)

const protoFile = `package main

type ErrorResponse struct {
	Error     string ` + "`json:\"error\"`" + `
	Code      string ` + "`json:\"code,omitempty\"`" + `
	Category  string ` + "`json:\"category,omitempty\"`" + `
	Retryable bool   ` + "`json:\"retryable,omitempty\"`" + `
}

type ScopeLevel string

const (
	ScopePublic       ScopeLevel = "public"
	ScopeInternal     ScopeLevel = "internal"
	ScopeConfidential ScopeLevel = "confidential"
)
`

const serverFile = `package main

import "net/http"

func routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/register", handleRegister)
	mux.HandleFunc("GET /v1/health", handleHealth)
	mux.HandleFunc("/v1/channel", handleChannel)
}
`

func TestMissingJSONKeyIsAFailure(t *testing.T) {
	// The assertion names five keys; the struct has four. This is the fault class
	// that produced AgentEntry.Name used six times against a type that has no
	// such field — and the definition was in a blueprint all along.
	items := []ChecklistItem{{Source: "protocol/types.md",
		Text: "ErrorResponse includes `error` (required), `code`, `category`, `retryable`, and `detail` fields with exact JSON keys"}}
	r := EvaluateChecklist(items, []GeneratedFile{{Path: "protocol.go", Content: protoFile}})[0]
	if r.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %s, want failed: %+v", r.Outcome, r)
	}
	if !strings.Contains(r.Detail, "detail") {
		t.Errorf("the detail does not name the missing key: %q", r.Detail)
	}
	// And it must be attributable, or the repair loop has nothing to ask.
	if len(r.Files) != 1 || r.Files[0] != "protocol.go" {
		t.Errorf("blame = %v, want [protocol.go]", r.Files)
	}
}

func TestPresentFieldsAreNecessaryNotVerified(t *testing.T) {
	// The keys existing does not establish required-ness, forward compatibility,
	// or what a signature covers. Reporting it as a pass is the confident wrong
	// answer this whole layer exists to prevent.
	items := []ChecklistItem{{Source: "protocol/types.md",
		Text: "ErrorResponse includes `error` (required), `code`, `category`, and `retryable` fields with exact JSON keys"}}
	r := EvaluateChecklist(items, []GeneratedFile{{Path: "protocol.go", Content: protoFile}})[0]
	if r.Outcome != OutcomeNecessary {
		t.Fatalf("outcome = %s, want necessary", r.Outcome)
	}
	if r.Passed() {
		t.Error("a necessary condition holding was reported as passed")
	}
	if r.Checked() {
		t.Error("a necessary condition was counted as checked — it would be added to verified")
	}
}

func TestEnumValuesAreCheckedAgainstConstants(t *testing.T) {
	items := []ChecklistItem{{Source: "protocol/types.md",
		Text: "ScopeLevel enum is constrained to `public`, `internal`, `confidential`, `restricted`, `critical`"}}
	r := EvaluateChecklist(items, []GeneratedFile{{Path: "protocol.go", Content: protoFile}})[0]
	if r.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %s, want failed", r.Outcome)
	}
	for _, want := range []string{"restricted", "critical"} {
		if !strings.Contains(r.Detail, want) {
			t.Errorf("detail does not name the missing value %q: %s", want, r.Detail)
		}
	}
}

func TestRoutesComeFromTheASTNotTheText(t *testing.T) {
	// A path in a comment or an error message is not a route. A regex over source
	// cannot tell the difference, and a wrong pass here would hide a missing
	// endpoint.
	decoy := `package main

// POST /v1/audit is documented here but registered nowhere.
const msg = "POST /v1/services failed"
`
	files := []GeneratedFile{{Path: "protocol.go", Content: protoFile}, {Path: "server.go", Content: serverFile}, {Path: "doc.go", Content: decoy}}

	present := EvaluateChecklist([]ChecklistItem{{Source: "s", Text: "Orchestrator `POST /v1/register` validates namespace ownership (409 on conflict)"}}, files)[0]
	if present.Outcome != OutcomeNecessary {
		t.Errorf("a routed endpoint: outcome = %s, want necessary (route exists; the 409 is behaviour)", present.Outcome)
	}

	absent := EvaluateChecklist([]ChecklistItem{{Source: "s", Text: "Orchestrator `GET /v1/audit` returns audit entries with pagination"}}, files)[0]
	if absent.Outcome != OutcomeFailed {
		t.Errorf("an unrouted endpoint: outcome = %s, want failed", absent.Outcome)
	}
	if len(absent.Files) == 0 {
		t.Error("a missing route is blamed on no file — the repair loop cannot act")
	}

	// A path-only registration satisfies a method-qualified assertion: the mux
	// registers the path and the handler switches on method, which is legitimate.
	pathOnly := EvaluateChecklist([]ChecklistItem{{Source: "s", Text: "Orchestrator `POST /v1/channel` verifies `agent:message` capability"}}, files)[0]
	if pathOnly.Outcome == OutcomeFailed {
		t.Errorf("a path-only mux registration was reported as a missing route: %s", pathOnly.Detail)
	}
}

func TestDependencyPolicyIsSettledByGoMod(t *testing.T) {
	// Not one-way: go.mod is the complete statement of a Go module's
	// dependencies, so this can be verified outright.
	assertion := ChecklistItem{Source: "platforms/go.md",
		Text: "No dependency beyond `github.com/cloudflare/circl`, plus a storage driver only if a backend other than the JSONL default was chosen; every dependency declared in go.mod"}

	clean := []GeneratedFile{
		{Path: "go.mod", Content: "module hub\n\ngo 1.22\n\nrequire github.com/cloudflare/circl v1.3.7\n"},
		{Path: "identity.go", Content: "package main\n\nimport \"github.com/cloudflare/circl/sign/mldsa/mldsa65\"\n"},
	}
	if r := EvaluateChecklist([]ChecklistItem{assertion}, clean)[0]; r.Outcome != OutcomeVerified {
		t.Errorf("a conforming module: outcome = %s (%s), want verified", r.Outcome, r.Detail)
	}

	// The exact fault the storage blueprint's SQLite assertion used to cause.
	sqlite := []GeneratedFile{
		{Path: "go.mod", Content: "module hub\n\ngo 1.22\n\nrequire (\n\tgithub.com/cloudflare/circl v1.3.7\n\tmodernc.org/sqlite v1.29.0\n)\n"},
		{Path: "storage.go", Content: "package main\n\nimport _ \"modernc.org/sqlite\"\n"},
	}
	r := EvaluateChecklist([]ChecklistItem{assertion}, sqlite)[0]
	if r.Outcome != OutcomeFailed {
		t.Fatalf("an unpermitted dependency: outcome = %s, want failed", r.Outcome)
	}
	if !strings.Contains(r.Detail, "modernc.org/sqlite") {
		t.Errorf("detail does not name the dependency: %s", r.Detail)
	}
	// go.mod AND the importing file must both change, or the build breaks.
	if !containsString(r.Files, "go.mod") || !containsString(r.Files, "storage.go") {
		t.Errorf("blame = %v, want both go.mod and storage.go", r.Files)
	}

	// An import nobody declared is the other half of the same assertion.
	undeclared := []GeneratedFile{
		{Path: "go.mod", Content: "module hub\n\ngo 1.22\n"},
		{Path: "identity.go", Content: "package main\n\nimport \"github.com/cloudflare/circl/sign/mldsa/mldsa65\"\n"},
	}
	if r := EvaluateChecklist([]ChecklistItem{assertion}, undeclared)[0]; r.Outcome != OutcomeFailed {
		t.Errorf("an undeclared import: outcome = %s, want failed", r.Outcome)
	}
}

func TestStdlibImportsAreNotDependencies(t *testing.T) {
	// A check that called net/http an undeclared dependency would fail every
	// correct implementation — the false-positive class that rejected three
	// correct runs already.
	assertion := ChecklistItem{Source: "platforms/go.md",
		Text: "No dependency beyond `github.com/cloudflare/circl`; every dependency declared in go.mod"}
	files := []GeneratedFile{
		{Path: "go.mod", Content: "module hub\n\ngo 1.22\n"},
		{Path: "server.go", Content: "package main\n\nimport (\n\t\"net/http\"\n\t\"encoding/json\"\n\t\"crypto/sha256\"\n)\n"},
	}
	if r := EvaluateChecklist([]ChecklistItem{assertion}, files)[0]; r.Outcome != OutcomeVerified {
		t.Errorf("stdlib-only module: outcome = %s (%s), want verified", r.Outcome, r.Detail)
	}
}

func TestForbiddenAlgorithmsAreDetectedByImport(t *testing.T) {
	assertion := ChecklistItem{Source: "protocol/identity.md",
		Text: "No quantum-vulnerable algorithms (Ed25519, ECDSA, RSA) are used anywhere"}
	bad := []GeneratedFile{{Path: "identity.go", Content: "package main\n\nimport \"crypto/ed25519\"\n"}}
	if r := EvaluateChecklist([]ChecklistItem{assertion}, bad)[0]; r.Outcome != OutcomeFailed {
		t.Errorf("ed25519 imported: outcome = %s, want failed", r.Outcome)
	}
	// Naming the algorithms in a comment is not using them — the blueprint's own
	// assertion text mentions all three.
	good := []GeneratedFile{{Path: "identity.go", Content: "package main\n\n// No Ed25519, ECDSA or RSA is used here.\nimport \"crypto/sha256\"\n"}}
	if r := EvaluateChecklist([]ChecklistItem{assertion}, good)[0]; r.Outcome != OutcomeVerified {
		t.Errorf("algorithms named in a comment: outcome = %s (%s), want verified", r.Outcome, r.Detail)
	}
}

func TestAnUnparsableFileContributesNoStructure(t *testing.T) {
	// Structure invented from broken source would make the checklist disagree
	// with the compiler, which is the one thing it must never do.
	ctx := BuildCheckContext([]GeneratedFile{{Path: "broken.go", Content: "package main\nfunc broken( {"}})
	if len(ctx.Fields) != 0 || len(ctx.Routes) != 0 || len(ctx.OwnerOf) != 0 {
		t.Errorf("structure extracted from unparsable source: %+v", ctx)
	}
}

func TestTypeNamesMustBeDeclaredToBeChecked(t *testing.T) {
	// An assertion about a type the source never declares is unchecked, not
	// failed: the plan may legitimately put that type in a file not yet
	// generated, and a check firing on a word that merely looks like a type name
	// would invent failures.
	items := []ChecklistItem{{Source: "protocol/types.md",
		Text: "Finding includes all required fields: `rule_id`, `severity`, `element`"}}
	r := EvaluateChecklist(items, []GeneratedFile{{Path: "protocol.go", Content: protoFile}})[0]
	if r.Outcome != OutcomeUnchecked {
		t.Errorf("outcome = %s, want unchecked — Finding is not declared here", r.Outcome)
	}
}
