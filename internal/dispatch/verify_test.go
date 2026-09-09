package dispatch

// The four outcomes, and the asymmetry that makes the third one honest.

import (
	"os"
	"path/filepath"
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
		{Path: "go.mod", Content: "module hub\n\ngo 1.27\n\nrequire github.com/cloudflare/circl v1.6.5\n"},
		{Path: "identity.go", Content: "package main\n\nimport \"github.com/cloudflare/circl/sign/mldsa/mldsa65\"\n"},
	}
	if r := EvaluateChecklist([]ChecklistItem{assertion}, clean)[0]; r.Outcome != OutcomeVerified {
		t.Errorf("a conforming module: outcome = %s (%s), want verified", r.Outcome, r.Detail)
	}

	// The exact fault the storage blueprint's SQLite assertion used to cause.
	sqlite := []GeneratedFile{
		{Path: "go.mod", Content: "module hub\n\ngo 1.27\n\nrequire (\n\tgithub.com/cloudflare/circl v1.6.5\n\tmodernc.org/sqlite v1.29.0\n)\n"},
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
		{Path: "go.mod", Content: "module hub\n\ngo 1.27\n"},
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
		{Path: "go.mod", Content: "module hub\n\ngo 1.27\n"},
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
		{Path: "go.mod", Content: "module hub\n\ngo 1.27.1\n\nrequire (\n\tgithub.com/cloudflare/circl v1.6.5\n\tgolang.org/x/crypto v0.57.0\n)\n\nrequire golang.org/x/sys v0.48.0 // indirect\n"},
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
		{Path: "go.mod", Content: "module hub\n\ngo 1.27\n"},
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
		{Path: "go.mod", Content: "module hub\n\ngo 1.27\n\nrequire modernc.org/sqlite v1.29.0\n"},
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

func TestUnregisteredErrorCodesAreCaught(t *testing.T) {
	// The real fault in the first hub that built. It transcribed the blueprint's
	// error table faithfully, then its handlers wrote codes that were not in it:
	// AUTH_FAILED, VALIDATION_FAILED, and SIGNATURE_INVALID where the protocol
	// says INVALID_SIGNATURE. StatusForCode falls through to 500 for an
	// unregistered code, so `GET /v1/services` without a token answered 500 with
	// a correct AUTH_FAILED body — an interoperability break no compiler can see.
	assertion := ChecklistItem{Source: "protocol/types.md",
		Text: "All protocol-level error codes are registered centrally; agent-local codes use domain-descriptive names and do not collide with registered codes"}

	registry := `package main

type ErrorCodeSpec struct {
	Code   string
	Status int
}

var ErrorCodes = map[string]ErrorCodeSpec{
	"INVALID_REQUEST":   {"INVALID_REQUEST", 400},
	"INVALID_SIGNATURE": {"INVALID_SIGNATURE", 401},
}
`
	handlers := `package main

import "os"

func handle() {
	writeErrorCode(w, "SIGNATURE_INVALID", "bad signature")
	writeErrorCode(w, "INVALID_REQUEST", "missing field")
	_ = os.Getenv("WL_DEV")
	_ = os.Getenv("MY_CONFIG_PATH")
}
`
	files := []GeneratedFile{{Path: "protocol.go", Content: registry}, {Path: "orchestrator.go", Content: handlers}}
	r := EvaluateChecklist([]ChecklistItem{assertion}, files)[0]
	if r.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %s, want failed", r.Outcome)
	}
	if !strings.Contains(r.Detail, "SIGNATURE_INVALID") {
		t.Errorf("the transposed code is not named: %s", r.Detail)
	}
	// Environment variable names are not error codes. The first version of this
	// check reported WL_DEV and WL_PORT as unregistered codes, because I
	// calibrated it on the thirty most frequent literals and never saw the rest.
	for _, env := range []string{"WL_DEV", "MY_CONFIG_PATH"} {
		if strings.Contains(r.Detail, env) {
			t.Errorf("%s was reported as an error code: %s", env, r.Detail)
		}
	}
	// A registered code must not be reported.
	if strings.Contains(r.Detail, "INVALID_REQUEST") {
		t.Errorf("a registered code was reported: %s", r.Detail)
	}
	if len(r.Files) != 1 || r.Files[0] != "orchestrator.go" {
		t.Errorf("blame = %v, want [orchestrator.go]", r.Files)
	}

	// Fix the transposition and it holds — as a necessary condition, since the
	// agent-local naming clause is not settled by this.
	fixed := strings.Replace(handlers, "SIGNATURE_INVALID", "INVALID_SIGNATURE", 1)
	files[1].Content = fixed
	if r := EvaluateChecklist([]ChecklistItem{assertion}, files)[0]; r.Outcome != OutcomeNecessary {
		t.Errorf("after the fix: outcome = %s (%s), want necessary", r.Outcome, r.Detail)
	}
}

func TestStatusesAreCheckedAgainstTheBlueprintsOwnTable(t *testing.T) {
	// Against protocol/types.md's table, not a copy of it in the tooling. A
	// second copy is a second thing to keep right, and the two would disagree the
	// moment either moved.
	spec := map[string]string{"protocol/types.md": "" +
		"| Code | HTTP | Category | Meaning |\n" +
		"|---|---|---|---|\n" +
		"| `INVALID_SIGNATURE` | 401 | permanent | signature failed |\n" +
		"| `NAMESPACE_CONFLICT` | 409 | permanent | already owned |\n"}
	assertion := ChecklistItem{Source: "protocol/types.md",
		Text: "Error categories are constrained to `transient`, `permanent`, or `partial` and each standard error code maps to the correct HTTP status"}

	right := "package main\n\nvar ErrorCodes = map[string]ErrorCodeSpec{\n" +
		"\t\"INVALID_SIGNATURE\": {\"INVALID_SIGNATURE\", 401, \"permanent\"},\n" +
		"\t\"NAMESPACE_CONFLICT\": {\"NAMESPACE_CONFLICT\", 409, \"permanent\"},\n}\n"
	if r := EvaluateChecklistAgainst([]ChecklistItem{assertion}, []GeneratedFile{{Path: "protocol.go", Content: right}}, spec)[0]; r.Outcome != OutcomeVerified {
		t.Errorf("a faithful registry: outcome = %s (%s), want verified", r.Outcome, r.Detail)
	}

	wrong := strings.Replace(right, "409", "500", 1)
	r := EvaluateChecklistAgainst([]ChecklistItem{assertion}, []GeneratedFile{{Path: "protocol.go", Content: wrong}}, spec)[0]
	if r.Outcome != OutcomeFailed {
		t.Fatalf("a wrong status: outcome = %s, want failed", r.Outcome)
	}
	if !strings.Contains(r.Detail, "NAMESPACE_CONFLICT") || !strings.Contains(r.Detail, "409") {
		t.Errorf("the detail does not say what was expected: %s", r.Detail)
	}

	// A keyed composite literal is equally correct Go, and the check must not
	// care which arrangement the model chose.
	keyed := "package main\n\nvar ErrorCodes = map[string]ErrorCodeSpec{\n" +
		"\t\"NAMESPACE_CONFLICT\": {Code: \"NAMESPACE_CONFLICT\", Status: 409, Category: \"permanent\"},\n}\n"
	if r := EvaluateChecklistAgainst([]ChecklistItem{assertion}, []GeneratedFile{{Path: "protocol.go", Content: keyed}}, spec)[0]; r.Outcome == OutcomeFailed {
		t.Errorf("a keyed literal was misread: %s", r.Detail)
	}
}

// The real generated hub's route registration, kept verbatim.
//
// The verifier accepted only a string literal as a mux pattern. This hub — like
// any hub anyone would write — registers from a table of named constants, with
// one local alias derived by strings.TrimSuffix and a loop that concatenates
// the method onto the pattern. The verifier saw ZERO routes and refuted
// twenty-one correct assertions with "no handler is registered for: GET
// /v1/health".
func TestRoutesResolveThroughConstantsAndConcatenation(t *testing.T) {
	load := func(name, path string) GeneratedFile {
		b, err := os.ReadFile(filepath.Join("testdata", "generated", name))
		if err != nil {
			t.Fatal(err)
		}
		return GeneratedFile{Path: path, Content: string(b)}
	}
	ctx := BuildCheckContext([]GeneratedFile{
		load("server.go.txt", "internal/orchestrator/server.go"),
		load("paths.go.txt", "internal/protocol/paths.go"),
	})

	// Every path the refuted assertions asked about.
	for _, want := range []string{
		"/v1/health", "/v1/register", "/v1/channel", "/v1/services",
		"/v1/audit", "/v1/rotate-key",
		"/v1/admin/overview", "/v1/admin/operators/token",
		"/v1/admin/operators/register",
		"/v1/admin/agents",                   // a local alias via strings.TrimSuffix
		"/v1/admin/agents/{name}/deregister", // that alias, concatenated
	} {
		if _, ok := ctx.Routes[want]; !ok {
			t.Errorf("route %s was not found — the checklist would refute it", want)
		}
	}
}

// The resolution must not invent routes: an unreadable registration is recorded
// as unread, so a report can say "cannot tell" instead of "absent".
func TestAnUnreadableRegistrationIsRecordedNotIgnored(t *testing.T) {
	ctx := BuildCheckContext([]GeneratedFile{{
		Path: "main.go",
		Content: `package main

import "net/http"

func routes(mux *http.ServeMux, patterns []string) {
	for _, p := range patterns {
		mux.HandleFunc(p, nil)
	}
}
`,
	}})
	if len(ctx.Routes) != 0 {
		t.Fatalf("invented %d route(s) from a pattern it cannot read", len(ctx.Routes))
	}
	if len(ctx.UnroutableCalls) == 0 {
		t.Fatal("an unreadable registration was silently dropped — the report would say 'no handler' about code it could not parse")
	}
}

// A check that examined nothing must not return the strongest positive verdict.
//
// "HTTP handlers do not panic" searched parsed ASTs for handler-shaped
// functions and reported verified whenever it found no panic — including when
// it found no handler, and when not one file had parsed. That is the fault
// `d51d9ab` fixed for route resolution ("I cannot read this" is not "this is
// wrong"), in its mirror image: I cannot read this is not this is right.
func TestThePanicCheckDoesNotVerifyWhatItCouldNotRead(t *testing.T) {
	check := findCheck(t, "HTTP handlers do not panic")
	a := assertion{Text: "HTTP handlers do not panic", Lower: "http handlers do not panic"}

	t.Run("nothing parsed", func(t *testing.T) {
		ok, detail, _ := check.test(a, &CheckContext{Files: []GeneratedFile{
			{Path: "broken.go", Content: "package ??? this is not go"},
		}})
		if ok {
			t.Fatal("reported verified although no file could be parsed")
		}
		if _, marked := splitInconclusive(detail); !marked {
			t.Errorf("detail %q is not marked inconclusive, so an unreadable tenant reads as a REFUTED assertion", detail)
		}
	})

	t.Run("no handlers", func(t *testing.T) {
		ok, detail, _ := check.test(a, &CheckContext{Files: []GeneratedFile{
			{Path: "x.go", Content: "package p\n\nfunc helper() int { return 1 }\n"},
		}})
		if ok {
			t.Fatal("reported verified although there was no handler to examine")
		}
		if _, marked := splitInconclusive(detail); !marked {
			t.Errorf("detail %q is not marked inconclusive", detail)
		}
	})

	t.Run("a real handler with no panic still verifies", func(t *testing.T) {
		ok, detail, _ := check.test(a, &CheckContext{Files: []GeneratedFile{
			{Path: "h.go", Content: "package p\n\nimport \"net/http\"\n\n" +
				"func Handle(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }\n"},
		}})
		if !ok {
			t.Errorf("a clean handler was not verified: %q", detail)
		}
	})

	t.Run("a handler that panics still fails", func(t *testing.T) {
		ok, detail, _ := check.test(a, &CheckContext{Files: []GeneratedFile{
			{Path: "h.go", Content: "package p\n\nimport \"net/http\"\n\n" +
				"func Handle(w http.ResponseWriter, r *http.Request) { panic(\"nope\") }\n"},
		}})
		if ok {
			t.Error("a panicking handler was verified")
		}
		if _, marked := splitInconclusive(detail); marked {
			t.Error("a real refutation was reported as inconclusive")
		}
	})
}

func findCheck(t *testing.T, name string) structuralCheck {
	t.Helper()
	for _, c := range structuralChecks {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("no structural check named %q", name)
	return structuralCheck{}
}
