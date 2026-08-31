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

func TestAValueListIsNotAFieldList(t *testing.T) {
	// protocol/types.md mixes required fields and permitted values in one
	// sentence. Reading every backticked token as a field reported three correct
	// types as missing keys, and the third one told me the pattern rather than
	// the instance.
	src := `package main

type OperationIntent struct {
	ID            string ` + "`json:\"id\"`" + `
	Agent         string ` + "`json:\"agent\"`" + `
	Operation     string ` + "`json:\"operation\"`" + `
	Resource      string ` + "`json:\"resource\"`" + `
	ResourceClass string ` + "`json:\"resource_class\"`" + `
	Scope         string ` + "`json:\"scope\"`" + `
	Environment   string ` + "`json:\"environment\"`" + `
	Timestamp     int64  ` + "`json:\"timestamp\"`" + `
}
`
	items := []ChecklistItem{{Source: "protocol/types.md",
		Text: "OperationIntent requires `id`, `agent`, `operation`, `resource`, `resource_class`, `scope`, `environment`, and `timestamp`; operation includes `list` and `query`"}}
	r := EvaluateChecklist(items, []GeneratedFile{{Path: "protocol.go", Content: src}})[0]
	if r.Outcome == OutcomeFailed {
		t.Fatalf("a complete type was failed for not having its field's VALUES as fields: %s", r.Detail)
	}
	if r.Outcome != OutcomeNecessary {
		t.Errorf("outcome = %s, want necessary (the eight fields are present)", r.Outcome)
	}

	// A genuinely missing field in the first clause must still fail.
	missing := strings.Replace(src, "\tScope         string `json:\"scope\"`\n", "", 1)
	r2 := EvaluateChecklist(items, []GeneratedFile{{Path: "protocol.go", Content: missing}})[0]
	if r2.Outcome != OutcomeFailed || !strings.Contains(r2.Detail, "scope") {
		t.Errorf("a missing field was not caught: %s / %s", r2.Outcome, r2.Detail)
	}

	// And an assertion whose first clause is not about fields at all is left
	// alone rather than guessed at.
	supports := []ChecklistItem{{Source: "protocol/types.md",
		Text: "WorkflowPhase `on_error` supports `fail`, `skip`, and `retry`; `max_retries` applies only when `on_error` = `\"retry\"`"}}
	wf := "package main\n\ntype WorkflowPhase struct {\n\tOnError string `json:\"on_error\"`\n}\n"
	if r := EvaluateChecklist(supports, []GeneratedFile{{Path: "protocol.go", Content: wf}})[0]; r.Outcome == OutcomeFailed {
		t.Errorf("a 'supports' clause was read as a field list: %s", r.Detail)
	}
}

func TestIndirectDependenciesAreNotDeclarations(t *testing.T) {
	// The generated hub declared golang.org/x/crypto for Argon2id and `go mod
	// tidy` wrote golang.org/x/sys beneath it as indirect. Counting that against
	// the dependency policy reports a violation nobody committed and cannot fix
	// without removing the permitted dependency above it.
	assertion := ChecklistItem{Source: "platforms/go.md",
		Text: "No dependency beyond `github.com/cloudflare/circl` and `golang.org/x/crypto`; every dependency declared in go.mod"}
	files := []GeneratedFile{
		{Path: "go.mod", Content: "module hub\n\ngo 1.22.0\n\nrequire (\n\tgithub.com/cloudflare/circl v1.6.1\n\tgolang.org/x/crypto v0.11.1\n)\n\nrequire golang.org/x/sys v0.10.0 // indirect\n"},
		{Path: "identity.go", Content: "package main\n\nimport \"golang.org/x/crypto/argon2\"\n"},
	}
	if r := EvaluateChecklist([]ChecklistItem{assertion}, files)[0]; r.Outcome != OutcomeVerified {
		t.Errorf("outcome = %s (%s), want verified", r.Outcome, r.Detail)
	}
	// A direct dependency outside the policy must still fail.
	files[0].Content += "\nrequire github.com/gin-gonic/gin v1.9.0\n"
	if r := EvaluateChecklist([]ChecklistItem{assertion}, files)[0]; r.Outcome != OutcomeFailed {
		t.Errorf("a direct unpermitted dependency was not caught: %s", r.Outcome)
	}
}

func TestAConditionalAssertionBindsOnlyWhenItsPremiseHolds(t *testing.T) {
	// "IF SQLite was chosen: WAL journal mode, `user_version` pragma…" failed
	// every implementation that took the JSONL default the blueprint recommends.
	assertion := ChecklistItem{Source: "platforms/go.md",
		Text: "IF SQLite was chosen: WAL journal mode, `user_version` pragma for migrations, tables created with `CREATE TABLE IF NOT EXISTS`"}

	jsonl := []GeneratedFile{
		{Path: "go.mod", Content: "module hub\n\ngo 1.22\n"},
		{Path: "storage.go", Content: "package main\n\n// append-only JSONL\n"},
	}
	r := EvaluateChecklist([]ChecklistItem{assertion}, jsonl)[0]
	if r.Outcome != OutcomeNotApplicable {
		t.Errorf("JSONL backend: outcome = %s (%s), want not-applicable", r.Outcome, r.Detail)
	}
	// Not-applicable is not unchecked: nobody needs to review it.
	_, _, _, na, unchecked := ChecklistCounts([]ChecklistResult{r})
	if na != 1 || unchecked != 0 {
		t.Errorf("counts: %d not-applicable, %d unchecked; want 1 and 0", na, unchecked)
	}

	// Choose SQLite and the obligation binds.
	chosen := []GeneratedFile{
		{Path: "go.mod", Content: "module hub\n\ngo 1.22\n\nrequire modernc.org/sqlite v1.29.0\n"},
		{Path: "storage.go", Content: "package main\n\n// no pragmas here\n"},
	}
	if r := EvaluateChecklist([]ChecklistItem{assertion}, chosen)[0]; r.Outcome != OutcomeFailed {
		t.Errorf("SQLite chosen and pragma absent: outcome = %s, want failed", r.Outcome)
	}
	withPragma := chosen
	withPragma[1].Content = "package main\n\nconst pragmas = \"PRAGMA journal_mode=WAL; PRAGMA user_version=1;\"\n"
	if r := EvaluateChecklist([]ChecklistItem{assertion}, withPragma)[0]; r.Outcome == OutcomeFailed {
		t.Errorf("SQLite chosen and pragma present: %s", r.Detail)
	}
}

func TestAnUnrecognisedPremiseIsNotExcused(t *testing.T) {
	// Guessing that a premise is false is how a checking layer starts silently
	// forgiving requirements — worse than the false failure it would be fixing.
	assertion := ChecklistItem{Source: "x",
		Text: "IF the deployment is clustered: sessions are replicated across nodes"}
	r := EvaluateChecklist([]ChecklistItem{assertion}, []GeneratedFile{{Path: "a.go", Content: "package main\n"}})[0]
	if r.Outcome != OutcomeUnchecked {
		t.Errorf("outcome = %s, want unchecked", r.Outcome)
	}
	if !strings.Contains(r.Detail, "clustered") {
		t.Errorf("the premise is not quoted for the human who must judge it: %q", r.Detail)
	}
}
