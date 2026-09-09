package dispatch

// The one test that asks the opposite question.
//
// Every other test here asks "does the check catch the fault". Nine false
// positives in this session came from checks that caught their fault and also
// rejected correct work — and each was found by running against real generated
// code, not by reasoning about the check.
//
// This asks: given an artifact that satisfies an assertion by a route the check's
// author did not have in mind, does the check still pass it? Each case below is a
// false positive that actually happened, or the same class one level over.

import (
	"strings"
	"testing"
)

// conformantHub is a small artifact that satisfies every assertion exercised
// below, written in shapes a checker might not expect.
func conformantHub() []GeneratedFile {
	return []GeneratedFile{
		{Path: "go.mod", Content: "module hub\n\ngo 1.27.1\n\nrequire (\n\tgithub.com/cloudflare/circl v1.6.5\n\tgolang.org/x/crypto v0.57.0\n)\n\nrequire golang.org/x/sys v0.48.0 // indirect\n"},
		{Path: "protocol.go", Content: `// Package main carries the wire protocol.
//
// Deliberately opens with a doc comment: a file that legitimately does so must
// not be mistaken for prose.
package main

import "fmt"

type ErrorCodeSpec struct {
	Code     string
	Status   int
	Category string
}

var ErrorCodes = map[string]ErrorCodeSpec{
	"INVALID_REQUEST":   {Code: "INVALID_REQUEST", Status: 400, Category: "permanent"},
	"INVALID_SIGNATURE": {Code: "INVALID_SIGNATURE", Status: 401, Category: "permanent"},
}

// An init assertion, not a handler. panic here is correct code.
func init() {
	for key, spec := range ErrorCodes {
		if key != spec.Code {
			panic(fmt.Sprintf("registry key %q disagrees with %q", key, spec.Code))
		}
	}
}

type ErrorResponse struct {
	Error     string ` + "`json:\"error\"`" + `
	Code      string ` + "`json:\"code,omitempty\"`" + `
	Category  string ` + "`json:\"category,omitempty\"`" + `
	Retryable bool   ` + "`json:\"retryable,omitempty\"`" + `
	Detail    any    ` + "`json:\"detail,omitempty\"`" + `
}
`},
		{Path: "orchestrator.go", Content: `//go:build !test

// A build constraint before the package clause is legal Go.
package main

import (
	"net/http"
	"os"
)

func routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/register", handleRegister)
	// Registered without a method: the handler switches internally, which is
	// legitimate and must not read as a missing route.
	mux.HandleFunc("/v1/services", handleServices)
}

func handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" {
		writeErrorCode(w, "INVALID_SIGNATURE", "unsigned")
		return
	}
	_ = os.Getenv("WL_DEV")
	_ = os.Getenv("DEPLOY_ENVIRONMENT")
}

func handleServices(w http.ResponseWriter, r *http.Request) {}

func writeErrorCode(w http.ResponseWriter, code, msg string) {}
`},
	}
}

func TestCorrectWorkIsNotRejected(t *testing.T) {
	spec := map[string]string{"protocol/types.md": "" +
		"| `INVALID_REQUEST` | 400 | permanent | malformed |\n" +
		"| `INVALID_SIGNATURE` | 401 | permanent | bad signature |\n"}

	// Each assertion, paired with the shape that used to break its check.
	cases := []struct {
		why  string
		text string
	}{
		{"an init() panic is not a handler panic",
			"Functions return errors — HTTP handlers write error JSON responses and do not panic"},
		{"an indirect go.mod entry is not a declared dependency",
			"No dependency beyond `github.com/cloudflare/circl` and `golang.org/x/crypto`; every dependency declared in go.mod"},
		{"a path-only mux registration satisfies a method-qualified assertion",
			"Orchestrator `GET /v1/services` returns service directory with routing table and namespace map"},
		{"a value list after a semicolon is not a field list",
			"ErrorResponse includes `error` (required), `code`, `category`, `retryable`, and `detail` fields with exact JSON keys; category includes `permanent` and `transient`"},
		{"an environment variable is not an error code",
			"All protocol-level error codes are registered centrally; agent-local codes use domain-descriptive names and do not collide with registered codes"},
		{"a keyed composite literal carries the status as plainly as a positional one",
			"Error categories are constrained to `transient`, `permanent`, or `partial` and each standard error code maps to the correct HTTP status"},
		{"a conditional whose premise is false does not bind",
			"IF SQLite was chosen: WAL journal mode, `user_version` pragma for migrations"},
		{"a doc comment and a build constraint may precede the package clause",
			"All source files are in `package main`; shared code is copied between orchestrator and agents"},
		{"every routed path is version-prefixed even when one is registered without a method",
			"Protocol paths are all prefixed with `/v1` and method/auth requirements match the Protocol Paths table"},
		{"naming forbidden algorithms in prose is not using them",
			"No quantum-vulnerable algorithms (Ed25519, ECDSA, RSA) are used anywhere"},
	}

	files := conformantHub()
	for _, c := range cases {
		r := EvaluateChecklistAgainst([]ChecklistItem{{Source: "t", Text: c.text}}, files, spec)[0]
		if r.Outcome == OutcomeFailed {
			t.Errorf("FALSE POSITIVE — %s\n  assertion: %s\n  check: %s\n  said: %s",
				c.why, c.text, r.Check, r.Detail)
		}
	}
}

func TestTheSameChecksStillCatchTheirFaults(t *testing.T) {
	// A false-positive fix that quietly disabled a check would pass the test
	// above and be worse than the false positive. Every check must still refuse
	// the thing it exists to refuse.
	spec := map[string]string{"protocol/types.md": "| `NAMESPACE_CONFLICT` | 409 | permanent | owned |\n"}

	cases := []struct {
		name  string
		text  string
		files []GeneratedFile
	}{
		{"a panic inside a handler",
			"HTTP handlers write error JSON responses and do not panic",
			[]GeneratedFile{{Path: "a.go", Content: "package main\n\nimport \"net/http\"\n\nfunc h(w http.ResponseWriter, r *http.Request) { panic(\"no\") }\n"}}},
		{"a direct dependency outside the policy",
			"No dependency beyond `github.com/cloudflare/circl`; every dependency declared in go.mod",
			[]GeneratedFile{{Path: "go.mod", Content: "module hub\n\nrequire github.com/gin-gonic/gin v1.9.0\n"}}},
		{"an endpoint nobody routed",
			"Orchestrator `GET /v1/audit` returns audit entries with pagination",
			[]GeneratedFile{{Path: "a.go", Content: "package main\n\nimport \"net/http\"\n\nfunc r(m *http.ServeMux) { m.HandleFunc(\"GET /v1/health\", nil) }\n"}}},
		{"a field the first clause requires and the type lacks",
			"ErrorResponse requires `error`, `code`, and `category` fields with exact JSON keys",
			[]GeneratedFile{{Path: "a.go", Content: "package main\n\ntype ErrorResponse struct {\n\tError string `json:\"error\"`\n}\n"}}},
		{"an error code that is not registered",
			"All protocol-level error codes are registered centrally; codes do not collide",
			[]GeneratedFile{{Path: "a.go", Content: "package main\n\nvar ErrorCodes = map[string]int{\"INVALID_REQUEST\": 400}\n\nfunc h() { write(\"AUTH_FAILED\") }\n"}}},
		{"a status that disagrees with the protocol",
			"each standard error code maps to the correct HTTP status",
			[]GeneratedFile{{Path: "a.go", Content: "package main\n\nvar ErrorCodes = map[string]Spec{\"NAMESPACE_CONFLICT\": {\"NAMESPACE_CONFLICT\", 500}}\n"}}},
		{"a conditional whose premise holds and whose obligation is unmet",
			"IF SQLite was chosen: WAL journal mode, `user_version` pragma for migrations",
			[]GeneratedFile{{Path: "go.mod", Content: "module hub\n\nrequire modernc.org/sqlite v1.29.0\n"}, {Path: "a.go", Content: "package main\n"}}},
		{"a file not in package main",
			"All source files are in `package main`",
			[]GeneratedFile{{Path: "a.go", Content: "package storage\n"}}},
		{"a forbidden algorithm actually imported",
			"No quantum-vulnerable algorithms (Ed25519, ECDSA, RSA) are used anywhere",
			[]GeneratedFile{{Path: "a.go", Content: "package main\n\nimport \"crypto/ecdsa\"\n"}}},
	}

	for _, c := range cases {
		r := EvaluateChecklistAgainst([]ChecklistItem{{Source: "t", Text: c.text}}, c.files, spec)[0]
		if r.Outcome != OutcomeFailed {
			t.Errorf("CHECK WENT BLIND — %s was not caught\n  assertion: %s\n  outcome: %s (%s)",
				c.name, c.text, r.Outcome, r.Check)
		}
	}
}

func TestNoCheckClaimsMoreThanItEstablished(t *testing.T) {
	// The structural discipline: a check that settles only a necessary condition
	// must never report `verified`. If one does, a compound assertion is being
	// reported as proven by a route existing — the failure the whole four-outcome
	// design was added to prevent.
	for _, sc := range structuralChecks {
		if !sc.oneWay {
			continue
		}
		// A one-way check that holds must produce `necessary`, never `verified`.
		// Exercised through the real evaluator rather than by inspection.
		if sc.name == "" {
			t.Error("a structural check has no name; its outcome cannot be attributed")
		}
	}
	files := conformantHub()
	r := EvaluateChecklist([]ChecklistItem{{Source: "t",
		Text: "Orchestrator `POST /v1/register` validates namespace ownership (409 on conflict)"}}, files)[0]
	if r.Outcome == OutcomeVerified {
		t.Error("a routed endpoint was reported as proving what the endpoint enforces")
	}
	if r.Outcome != OutcomeNecessary {
		t.Errorf("outcome = %s, want necessary", r.Outcome)
	}
	if !strings.Contains(r.Check, "routed") {
		t.Errorf("the check is not named in the result: %q", r.Check)
	}
}
