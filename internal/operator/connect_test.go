package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// hub is a stand-in orchestrator with a chosen policy, so the three outcomes
// can be produced deliberately rather than waited for.
func hub(t *testing.T, register int, token int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/admin/operators/register", func(w http.ResponseWriter, r *http.Request) {
		if register != 200 {
			w.WriteHeader(register)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "refused"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "x", "role": "admin", "status": "approved"})
	})
	mux.HandleFunc("POST /v1/admin/operators/token", func(w http.ResponseWriter, r *http.Request) {
		if token != 200 {
			w.WriteHeader(token)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "refused"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "wlt.header.sig", "expires_at": 1 << 40})
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func isolated(t *testing.T) {
	t.Helper()
	SetKeysDir(t.TempDir())
	cachedOnce = sync.Once{}
	t.Cleanup(func() { SetKeysDir(""); cachedOnce = sync.Once{} })
}

// A hub that already knows the operator issues a token without registering.
//
// Asking for a token FIRST matters: attempting registration at a hub with no
// operators would silently make this identity the bootstrap admin of somebody
// else's tenant.
func TestConnectToAHubThatAlreadyKnowsTheOperator(t *testing.T) {
	isolated(t)
	s := hub(t, 500 /* register would fail */, 200)
	res, err := Connect(s.URL, "lloyd", "passphrase-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	if res.State != ConnectedState {
		t.Fatalf("state = %q (%s), want connected", res.State, res.Detail)
	}
	if res.Token == "" {
		t.Error("connected with no token")
	}
}

// A hub with existing operators refuses an unauthenticated registration. That
// is the specification, so it is reported as pending approval and names who
// can act — not as a failure to debug.
func TestConnectToAnEstablishedHubReportsPendingApproval(t *testing.T) {
	isolated(t)
	s := hub(t, 401, 401)
	res, err := Connect(s.URL, "carol", "passphrase-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	if res.State != PendingApprovalState {
		t.Fatalf("state = %q (%s), want pending_approval", res.State, res.Detail)
	}
	if !strings.Contains(res.Detail, "existing admin") {
		t.Errorf("the detail does not say who can act: %q", res.Detail)
	}
	if !strings.Contains(res.Detail, "carol") {
		t.Errorf("the detail does not name the operator to be added: %q", res.Detail)
	}
}

// A hub with no token endpoint must NOT be reported as awaiting approval.
// Doing so told someone to wait for something that was never coming.
func TestAHubWithoutTheTokenEndpointIsNotReportedAsPending(t *testing.T) {
	isolated(t)
	s := hub(t, 200, 405)
	res, err := Connect(s.URL, "lloyd", "passphrase-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	if res.State == PendingApprovalState {
		t.Fatal("a hub that cannot issue tokens was reported as awaiting approval")
	}
	if res.State != UnreachableState {
		t.Fatalf("state = %q, want unreachable", res.State)
	}
	for _, want := range []string{"operators/token", "predates", "regenerate"} {
		if !strings.Contains(res.Detail, want) {
			t.Errorf("the detail is missing %q: %q", want, res.Detail)
		}
	}
}

// Connecting needs an address and nothing else — no tenant directory, no run
// state, no access to the hub's files.
func TestConnectNeedsOnlyAnAddress(t *testing.T) {
	isolated(t)
	s := hub(t, 200, 200)
	res, err := Connect(s.URL, "lloyd", "passphrase-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	if res.State != ConnectedState {
		t.Fatalf("state = %q (%s)", res.State, res.Detail)
	}
	if res.Address != s.URL {
		t.Errorf("address = %q, want %q", res.Address, s.URL)
	}
}

func TestConnectRefusesAnEmptyAddressAndAShortPassphrase(t *testing.T) {
	isolated(t)
	if _, err := Connect("", "lloyd", "passphrase-long-enough"); err == nil {
		t.Error("an empty address was accepted")
	}
	if _, err := Connect("http://x", "lloyd", "short"); err == nil {
		t.Error("a short passphrase was accepted")
	}
	if _, err := Connect("http://x", "", "passphrase-long-enough"); err == nil {
		t.Error("an empty operator name was accepted")
	}
}
