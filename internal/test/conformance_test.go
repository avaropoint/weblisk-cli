package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The suite declared 24 assertions and issued a request for four of them. The
// other 20 reached `default: return true`, marked "pass by default until full
// test harness is implemented", so a run printed 24 ticks against a hub that
// had answered one request — and called that hub conformant.
//
// These hold the line in both directions: an unimplemented assertion is never
// a pass, and an implemented one can actually fail.

func TestAnUnimplementedAssertionIsNeverAPass(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Answers everything agreeably, so nothing fails for another reason.
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "healthy"})
	}))
	defer hub.Close()
	client := &http.Client{Timeout: 2 * time.Second}

	for _, id := range []string{
		"L1-01", "L1-03", "L1-06", "L1-07", "L1-08", "L1-10", "L1-11",
		"L2-01", "L2-05", "L2-08", "L3-01", "L3-04",
	} {
		r := runConformanceTest(client, hub.URL, id, "", false)
		if r.outcome == outcomePassed {
			t.Errorf("%s reported PASSED with no check implemented — "+
				"a hub that answered nothing relevant is being called conformant", id)
		}
		if r.outcome != outcomeNotChecked {
			t.Errorf("%s outcome = %v, want not-checked", id, r.outcome)
		}
		if strings.TrimSpace(r.detail) == "" {
			t.Errorf("%s is not checked and does not say what it would need", id)
		}
	}
}

// A check that cannot fail is no better than the `return true` it replaced.
func TestTheImplementedChecksCanFail(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/register":
			_, _ = w.Write([]byte(`{"agent_id":"abc"}`)) // accepts unsigned
		case r.URL.Path == "/v1/health" && r.Method == "POST":
			_, _ = w.Write([]byte(`{}`)) // no status
		case r.URL.Path == "/v1/health":
			_, _ = w.Write([]byte(`{"status":"ok","version":"1"}`)) // missing required fields
		case r.URL.Path == "/v1/admin/overview":
			_, _ = w.Write([]byte(`{"secret":"leaked"}`)) // no auth required
		default:
			w.WriteHeader(404)
		}
	}))
	defer bad.Close()
	client := &http.Client{Timeout: 2 * time.Second}

	for _, tc := range []struct{ id, wantDetail string }{
		{"L1-02", "unsigned"},
		{"L1-04", "status"},
		{"L1-09", "unprotected"},
		{"L1-12", "required field"},
	} {
		r := runConformanceTest(client, bad.URL, tc.id, "", false)
		if r.outcome != outcomeFailed {
			t.Errorf("%s outcome = %v against a hub that violates it, want failed", tc.id, r.outcome)
			continue
		}
		if !strings.Contains(r.detail, tc.wantDetail) {
			t.Errorf("%s detail = %q, want it to mention %q so an operator can act", tc.id, r.detail, tc.wantDetail)
		}
	}
}

// And the same checks must pass against a hub that honours the spec, or they
// are noise nobody can ever satisfy.
func TestTheImplementedChecksPassAgainstAConformantHub(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/register":
			http.Error(w, "unsigned registration", 401)
		case r.URL.Path == "/v1/health" && r.Method == "POST":
			_, _ = w.Write([]byte(`{"name":"a","status":"healthy","version":"1","uptime":1,"timestamp":1}`))
		case r.URL.Path == "/v1/health":
			_, _ = w.Write([]byte(`{"name":"o","status":"healthy","version":"1","uptime":1,"timestamp":1}`))
		case r.URL.Path == "/v1/services":
			_, _ = w.Write([]byte(`{"agents":[]}`))
		case r.URL.Path == "/v1/admin/overview":
			if r.Header.Get("Authorization") == "" {
				http.Error(w, "unauthorized", 401)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer good.Close()
	client := &http.Client{Timeout: 2 * time.Second}

	for _, id := range []string{"L1-02", "L1-04", "L1-05", "L1-09", "L1-12"} {
		r := runConformanceTest(client, good.URL, id, "", false)
		if r.outcome != outcomePassed {
			t.Errorf("%s = %v (%s) against a conformant hub, want passed", id, r.outcome, r.detail)
		}
	}
}

// HealthStatus's required fields are read from protocol/types.md, not invented.
// If that list drifts, this is the reminder to go and re-read it.
func TestHealthStatusRequiredFieldsMatchTheProtocol(t *testing.T) {
	want := map[string]bool{"name": true, "status": true, "version": true, "uptime": true, "timestamp": true}
	if len(healthStatusRequired) != len(want) {
		t.Fatalf("required = %v, want exactly the five protocol/types.md marks required: true", healthStatusRequired)
	}
	for _, f := range healthStatusRequired {
		if !want[f] {
			t.Errorf("%q is demanded but is not required in protocol/types.md", f)
		}
	}
	for _, s := range []string{"healthy", "degraded", "unhealthy"} {
		if !validHealthStates[s] {
			t.Errorf("%q is a legal HealthStatus state and is not accepted", s)
		}
	}
	if validHealthStates["ok"] {
		t.Error(`"ok" is accepted as a health state; the protocol names three and that is not one`)
	}
}

// The mock is advertised as a conformance target. It used to accept every
// registration unsigned and answer health without name, uptime or timestamp —
// so it could not fail the suite's security assertions, and a real hub with
// the same holes would have looked no different.
func TestTheMockOrchestratorIsItselfConformant(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a listener")
	}
	go func() { _ = handleMockOrchestrator([]string{"--port", "19899"}) }()

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := client.Get("http://localhost:19899/v1/health"); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	for _, id := range []string{"L1-02", "L1-04", "L1-05", "L1-09", "L1-12"} {
		r := runConformanceTest(client, "http://localhost:19899", id, "", false)
		if r.outcome != outcomePassed {
			t.Errorf("the mock fails %s (%s) — it is offered as a test target, "+
				"so it has to satisfy the assertions it is tested with", id, r.detail)
		}
	}
}
