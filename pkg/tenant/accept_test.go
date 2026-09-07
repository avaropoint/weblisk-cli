package tenant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// healthyTenant is a stand-in that answers the way a working tenant does:
// health without a credential, everything else with a 401.
func healthyTenant(t *testing.T, missing map[string]bool, wrongMethod map[string]bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"healthy","checks":{"storage":"ok","identity":"ok"}}`))
			return
		}
		key := r.Method + " " + r.URL.Path
		if missing[r.URL.Path] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if wrongMethod[key] {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	return httptest.NewServer(mux)
}

// A tenant that serves everything passes everything — and the passes are
// reported, not just the failures.
func TestAcceptPassesAWorkingTenant(t *testing.T) {
	srv := healthyTenant(t, nil, nil)
	defer srv.Close()

	checks := Accept(context.Background(), srv.URL)
	if len(checks) != len(requiredRoutes)+1 {
		t.Fatalf("got %d checks, want %d", len(checks), len(requiredRoutes)+1)
	}
	for _, c := range checks {
		if !c.OK {
			t.Errorf("check %q failed on a healthy tenant: %s", c.Name, c.Detail)
		}
	}
}

// The three faults this pass exists to catch. Each is reproduced exactly as it
// occurred against a real generated tenant.
func TestAcceptCatchesTheRouteFaultsThatShipped(t *testing.T) {
	t.Run("no approve route", func(t *testing.T) {
		srv := healthyTenant(t, map[string]bool{
			"/v1/admin/operators/" + probeSubject + "/approve": true,
		}, nil)
		defer srv.Close()
		c := findCheck(t, Accept(context.Background(), srv.URL), "POST /v1/admin/operators/{name}/approve")
		if c.OK {
			t.Fatal("a tenant with no approve route passed")
		}
		if !strings.Contains(c.Detail, "second operator can never be admitted") {
			t.Errorf("the detail does not say what breaks: %s", c.Detail)
		}
	})

	t.Run("role served but not as PUT", func(t *testing.T) {
		srv := healthyTenant(t, nil, map[string]bool{
			"PUT /v1/admin/operators/" + probeSubject + "/role": true,
		})
		defer srv.Close()
		c := findCheck(t, Accept(context.Background(), srv.URL), "PUT /v1/admin/operators/{name}/role")
		if c.OK {
			t.Fatal("a tenant that refuses PUT on the role route passed")
		}
		if !strings.Contains(c.Detail, "not PUT") {
			t.Errorf("a method mismatch must be named as one: %s", c.Detail)
		}
	})

	t.Run("no delete route", func(t *testing.T) {
		srv := healthyTenant(t, map[string]bool{
			"/v1/admin/operators/" + probeSubject: true,
		}, nil)
		defer srv.Close()
		c := findCheck(t, Accept(context.Background(), srv.URL), "DELETE /v1/admin/operators/{name}")
		if c.OK {
			t.Fatal("a tenant with no operator-delete route passed")
		}
	})
}

// A tenant that answers but reports a broken subsystem is not healthy, and
// collapsing that into a tick is how a broken store ships.
func TestAcceptReportsDegradedAsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"healthy","checks":{"storage":"failing","identity":"ok"}}`))
	}))
	defer srv.Close()

	checks := Accept(context.Background(), srv.URL)
	if checks[0].OK {
		t.Fatal("a tenant reporting storage=failing passed its health check")
	}
	if !strings.Contains(checks[0].Detail, "storage=failing") {
		t.Errorf("the failing subsystem must be named: %s", checks[0].Detail)
	}
}

// A tenant that does not answer at all must not produce ten timeouts, and the
// routes must be reported as UNRUN rather than as passes or as failures.
func TestAcceptStopsWhenTheTenantIsSilent(t *testing.T) {
	checks := Accept(context.Background(), "http://127.0.0.1:1")
	if len(checks) != 2 {
		t.Fatalf("got %d checks against a dead tenant, want 2 (health, then 'not asked')", len(checks))
	}
	if checks[0].OK || !checks[0].Fatal {
		t.Error("a silent tenant must fail its health check, fatally")
	}
	if checks[1].OK {
		t.Error("an unrun check is not a pass")
	}
	if !strings.Contains(checks[1].Detail, "not asked") {
		t.Errorf("an unrun check must say it was not run: %s", checks[1].Detail)
	}
}

// The probe subject must never be a name a tenant could have issued.
func TestProbeSubjectIsNotARealName(t *testing.T) {
	if !strings.Contains(probeSubject, "does-not-exist") {
		t.Error("the probe subject should be self-evidently not an operator")
	}
	for _, p := range requiredRoutes {
		if strings.Contains(p.Path, probeSubject) && p.Method == "DELETE" {
			// The one destructive probe. It must name the placeholder and
			// nothing else.
			if strings.Count(p.Path, probeSubject) != 1 {
				t.Errorf("the destructive probe %q does not name the placeholder exactly once", p.Path)
			}
		}
	}
}

func findCheck(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %d checks", name, len(checks))
	return Check{}
}
