package hubmodel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	s := Open(root)
	if s.Get().Provider != "" {
		t.Fatal("empty store was not empty")
	}
	if err := s.Set(Config{Provider: "claude-code"}); err != nil {
		t.Fatal(err)
	}
	got := Open(root).Get()
	if got.Provider != "claude-code" {
		t.Fatalf("provider = %q", got.Provider)
	}
	if _, err := os.Stat(filepath.Join(root, ".weblisk", "model.json")); err != nil {
		t.Fatal(err)
	}
}

func TestGetAndPut(t *testing.T) {
	s := Open(t.TempDir())
	h := s.Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/admin/model", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET empty: %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/admin/model", strings.NewReader(`{"provider":"ollama","model":"llama3.1"}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rr.Code, rr.Body.String())
	}
	var got Config
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Provider != "ollama" || got.Model != "llama3.1" {
		t.Fatalf("got %+v", got)
	}
}

func TestCompleteRefusesAnUnconfiguredTenant(t *testing.T) {
	h := Open(t.TempDir()).Handler()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/complete",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", rr.Code, rr.Body.String())
	}
}

func TestProvidersListsWhatThisMachineHas(t *testing.T) {
	h := Open(t.TempDir()).Handler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/admin/model/providers", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var found []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &found); err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 {
		t.Fatal("discovery returned nothing — even an empty machine must list what is missing")
	}
}
