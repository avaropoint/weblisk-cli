package dispatch

// Running the thing.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// a conformant orchestrator, in miniature.
func conformantServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name": "orchestrator", "status": "healthy", "version": "1.0.0",
			"uptime": 3, "timestamp": 1788275300,
		})
	})
	deny := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]any{
			"error": "valid token required", "code": "INVALID_SIGNATURE", "category": "permanent",
		})
	}
	mux.HandleFunc("/v1/services", deny)
	mux.HandleFunc("/v1/audit", deny)
	mux.HandleFunc("/v1/admin/overview", deny)
	return httptest.NewServer(mux)
}

func runTest(t *testing.T, id, base string) ConformanceResult {
	return runTestFor(t, id, base, "orchestrator")
}

func runTestFor(t *testing.T, id, base, component string) ConformanceResult {
	t.Helper()
	for _, tc := range l1Tests {
		if tc.id != id {
			continue
		}
		ok, detail, evidence := tc.run(base, component)
		return ConformanceResult{ID: id, Passed: ok, Detail: detail, Evidence: evidence}
	}
	t.Fatalf("no test %s", id)
	return ConformanceResult{}
}

func TestAConformantComponentPassesL1(t *testing.T) {
	s := conformantServer()
	defer s.Close()
	for _, id := range []string{"L1-01", "L1-07", "L1-10"} {
		if r := runTest(t, id, s.URL); !r.Passed {
			t.Errorf("%s failed a conformant component: %s", id, r.Detail)
		}
	}
}

func TestL1_01AssertsTheHealthShapeTheTypesDefine(t *testing.T) {
	// The test used to demand `state == "online"` and `uptime_seconds`, which no
	// blueprint defines — so a conformant hub failed it. It now asserts
	// protocol/types' HealthStatus, and must reject anything else.
	cases := []struct {
		name string
		body map[string]any
		pass bool
	}{
		{"HealthStatus", map[string]any{"name": "o", "status": "healthy", "version": "1", "uptime": 1, "timestamp": 1}, true},
		{"degraded is valid", map[string]any{"name": "o", "status": "degraded", "version": "1", "uptime": 1, "timestamp": 1}, true},
		{"the old invented shape", map[string]any{"name": "o", "state": "online", "version": "1", "uptime_seconds": 1}, false},
		{"a status outside the enum", map[string]any{"name": "o", "status": "online", "version": "1", "uptime": 1, "timestamp": 1}, false},
		{"missing timestamp", map[string]any{"name": "o", "status": "healthy", "version": "1", "uptime": 1}, false},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(tc.body)
		}))
		r := runTest(t, "L1-01", srv.URL)
		srv.Close()
		if r.Passed != tc.pass {
			t.Errorf("%s: passed=%v want %v (%s)", tc.name, r.Passed, tc.pass, r.Detail)
		}
	}
}

func TestL1_07CatchesAnUnprotectedEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"name": "o", "status": "healthy", "version": "1", "uptime": 1, "timestamp": 1})
	})
	// The fault: a protected endpoint answering without a token.
	mux.HandleFunc("/v1/services", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"services": []string{}})
	})
	mux.HandleFunc("/v1/audit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]any{"error": "no token"})
	})
	s := httptest.NewServer(mux)
	defer s.Close()

	r := runTest(t, "L1-07", s.URL)
	if r.Passed {
		t.Fatal("an endpoint serving data without a token passed L1-07")
	}
	if !strings.Contains(r.Detail, "/v1/services") {
		t.Errorf("the detail does not name the unprotected endpoint: %s", r.Detail)
	}
}

func TestL1_10CatchesANonJSONError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"name": "o", "status": "healthy", "version": "1", "uptime": 1, "timestamp": 1})
	})
	mux.HandleFunc("/v1/services", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", 401) // plain text
	})
	mux.HandleFunc("/v1/audit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"message":"no token"}`) // JSON, but no `error` field
	})
	s := httptest.NewServer(mux)
	defer s.Close()

	r := runTest(t, "L1-10", s.URL)
	if r.Passed {
		t.Fatal("a non-JSON error body and a body without `error` both passed L1-10")
	}
	for _, want := range []string{"non-JSON", "error"} {
		if !strings.Contains(r.Detail, want) {
			t.Errorf("the detail does not describe the fault (%q): %s", want, r.Detail)
		}
	}
}

func TestAgentOnlyTestsDoNotApplyToAnOrchestrator(t *testing.T) {
	// Four of the ten L1 tests address an agent. Running them against an
	// orchestrator reports a correct implementation as broken — the same fault
	// the Verification Checklists had before their groups were read.
	for _, tc := range l1Tests {
		if tc.id == "L1-03" || tc.id == "L1-04" || tc.id == "L1-05" {
			if !tc.applies["orchestrator"] || tc.applies["agent"] {
				t.Errorf("%s is orchestrator-only and is scoped %v", tc.id, tc.applies)
			}
		}
	}
}

func TestUnrunIsNeverCountedAsPassed(t *testing.T) {
	// L1-03/04/05 need a signing key and the mock agent. Stated as unrun rather
	// than quietly absent: an assertion nobody checked is not one that passed.
	rs := []ConformanceResult{
		{ID: "L1-01", Passed: true},
		{ID: "L1-03", Unrun: true},
		{ID: "L1-07", Passed: false},
	}
	passed, failed, unrun := ConformanceSummary(rs)
	if passed != 1 || failed != 1 || unrun != 1 {
		t.Errorf("summary = %d/%d/%d, want 1/1/1", passed, failed, unrun)
	}
	if f := FailedConformance(rs); len(f) != 1 || f[0].ID != "L1-07" {
		t.Errorf("failed set = %+v; unrun must not appear", f)
	}
}

func TestARuntimeFailureIsAttributedToBothSidesOfTheContradiction(t *testing.T) {
	// The real one. The message is assembled from format strings in two files:
	// server.go asks for the namespace, namespaces.go refuses it. The fix could
	// belong to either, so a repair shown only one would be guessing.
	output := `[dev] using in-memory storage — data will not survive restart
orchestrator: startup: reserve namespace "system": namespace "system" is reserved`

	content := map[string]string{
		"internal/orchestrator/server.go": "package orchestrator\n\n" +
			"// return fmt.Errorf(\"reserve namespace %q: %w\", protocol.NamespaceSystem, err)\n",
		"internal/storage/namespaces.go": "package storage\n\n" +
			"// return protocol.NewErrorf(code, \"namespace %q is reserved\", namespace)\n",
		"internal/protocol/types.go": "package protocol\n\ntype AgentManifest struct{}\n",
		"go.mod":                     "module tenant\n",
	}
	got := FilesBehindRuntimeFailure(output, content)
	for _, want := range []string{"internal/orchestrator/server.go", "internal/storage/namespaces.go"} {
		if !containsString(got, want) {
			t.Errorf("%s is named in the failure and was not found: got %v", want, got)
		}
	}
	// Files with nothing to do with it must be left alone.
	if containsString(got, "internal/protocol/types.go") {
		t.Errorf("an unrelated file was targeted: %v", got)
	}
	if containsString(got, "go.mod") {
		t.Error("a non-Go file was targeted")
	}
}

func TestAFailureMatchingNothingTargetsNothing(t *testing.T) {
	// A component killed by the OS, or failing on something no generated file
	// mentions. Repairing a file at random is worse than reporting the failure.
	content := map[string]string{"a.go": "package main\n\nfunc main() {}\n"}
	if got := FilesBehindRuntimeFailure("signal: killed", content); len(got) != 0 {
		t.Errorf("targeted %v for a failure naming nothing", got)
	}
	if got := FilesBehindRuntimeFailure("exit status 1", content); len(got) != 0 {
		t.Errorf("targeted %v for a bare exit status", got)
	}
}

func TestTheRuntimeRepairPromptCarriesWhatItNeeds(t *testing.T) {
	p := runtimeRepairPrompt(
		PlannedFile{Path: "internal/storage/namespaces.go", Purpose: "namespace registry"},
		"package storage\n\nfunc ClaimNamespace() {}\n",
		`orchestrator: startup: reserve namespace "system": namespace "system" is reserved`,
		"PLATFORM", "avaropoint")
	for _, want := range []string{
		"does not run", "reserve namespace", "logic fault, not a compile error",
		"Module path: avaropoint", "ClaimNamespace", "Keep every one",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the runtime-repair prompt is missing %q", want)
		}
	}
}

func TestANormalStartupWarningIsNotAFailure(t *testing.T) {
	// The fault that made one run spend thirty-three repair calls. This line is
	// ordinary dev-mode output, and its fragments — component_type, log_type —
	// appear in every file that emits a structured log, so matching on it
	// rewrote seven innocent files per round.
	output := `{"component":"identity","component_type":"library","event":"security.key_unencrypted","level":"warn","log_type":"security","msg":"private key is protected by file permissions alone — development only","ts":"2026-09-01T22:52:47.940Z"}
{"component":"orchestrator","level":"info","msg":"orchestrator listening","port":9800}
[dev] using in-memory storage — data will not survive restart`

	content := map[string]string{
		"internal/observability/log.go":   "package observability\n// \"component_type\":\"library\"\n// \"log_type\":\"security\"\n",
		"internal/orchestrator/audit.go":  "package orchestrator\n// \"component_type\":\"library\"\n",
		"internal/orchestrator/config.go": "package orchestrator\n// \"log_type\":\"security\"\n",
	}
	if got := FilesBehindRuntimeFailure(output, content); len(got) != 0 {
		t.Errorf("a startup that logged only warnings and info targeted %v", got)
	}
}

func TestAnErrorLevelLogIsAFailure(t *testing.T) {
	// The component marks its own severity. An error entry is a finding, and it
	// is the message that matters — not the JSON envelope around it.
	output := `{"component":"orchestrator","level":"warn","msg":"private key is protected by file permissions alone"}
{"component":"orchestrator","level":"error","msg":"reserve namespace \"system\": namespace \"system\" is reserved"}`

	content := map[string]string{
		"internal/storage/namespaces.go": "package storage\n// \"namespace %q is reserved\"\n",
		"internal/observability/log.go":  "package observability\n// private key is protected by file permissions alone\n",
	}
	got := FilesBehindRuntimeFailure(output, content)
	if !containsString(got, "internal/storage/namespaces.go") {
		t.Errorf("the error-level failure was not attributed: %v", got)
	}
	if containsString(got, "internal/observability/log.go") {
		t.Errorf("a warn-level line was matched as a failure: %v", got)
	}
}

func TestAPlainTextDeathIsAFailure(t *testing.T) {
	// A process dying prints a sentence, not JSON. That sentence is the finding.
	output := `{"level":"info","msg":"starting"}
orchestrator: startup: reserve namespace "system": namespace "system" is reserved`
	content := map[string]string{
		"internal/orchestrator/server.go": "package orchestrator\n// \"reserve namespace %q: %w\"\n",
		"internal/protocol/types.go":      "package protocol\ntype X struct{}\n",
	}
	got := FilesBehindRuntimeFailure(output, content)
	if !containsString(got, "internal/orchestrator/server.go") {
		t.Errorf("a plain-text death was not attributed: %v", got)
	}
	if containsString(got, "internal/protocol/types.go") {
		t.Errorf("an unrelated file was targeted: %v", got)
	}
}

// A required warning is not a failure.
//
// platforms/go requires every component to print the dev-storage warning under
// WL_DEV=1. It is plain text, so it reached failureLines as a candidate, matched
// five files, and a build that had ALREADY SUCCEEDED was sent to repair them —
// while the real message, an empty orchestrator public key, went unread.
func TestRequiredWarningsAreNotTreatedAsFailures(t *testing.T) {
	output := strings.Join([]string{
		"[dev] using in-memory storage — data will not survive restart",
		"[warn] no orchestrator configured; protected endpoints will refuse tokens",
		`{"level":"warn","event":"security.key_unencrypted","msg":"signing key is stored unencrypted"}`,
		"content: initialise service: build authenticator: orchestrator public key is empty",
	}, "\n")

	got := failureLines(output)
	if len(got) != 1 {
		t.Fatalf("want exactly the real failure, got %d lines: %q", len(got), got)
	}
	if !strings.Contains(got[0], "orchestrator public key is empty") {
		t.Errorf("the failure attributed was not the failure: %q", got[0])
	}
}

// A failure is not excused for containing a warning-ish word.
func TestAFailureMentioningWarningIsStillAFailure(t *testing.T) {
	got := failureLines("startup failed: warning threshold exceeded, refusing to serve")
	if len(got) != 1 {
		t.Fatalf("a real failure was discarded: %q", got)
	}
}
