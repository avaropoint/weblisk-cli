package dispatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallModelGatewayWrapsHandler(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "orchestrator")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package orchestrator

import "net/http"

func (o *Orchestrator) Handler() http.Handler {
	var h http.Handler = o.Routes()
	return h
}

func (o *Orchestrator) Routes() *http.ServeMux { return http.NewServeMux() }
`
	if err := os.WriteFile(filepath.Join(dir, "server.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InstallModelGateway(root); err != nil {
		t.Fatal(err)
	}
	gw, err := os.ReadFile(filepath.Join(dir, "model_gateway.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gw), "GET /v1/admin/model") {
		t.Fatal("gateway is missing the model routes")
	}
	patched, err := os.ReadFile(filepath.Join(dir, "server.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patched), "wrapModelGateway(o.Routes())") {
		t.Fatalf("Handler was not wrapped:\n%s", patched)
	}
}

func TestInstallModelGatewayIsIdempotent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "orchestrator")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "server.go"), []byte(`package orchestrator
import "net/http"
func (o *Orchestrator) Handler() http.Handler {
	var h http.Handler = wrapModelGateway(o.Routes())
	return h
}
func (o *Orchestrator) Routes() *http.ServeMux { return nil }
`), 0o644)
	if err := InstallModelGateway(root); err != nil {
		t.Fatal(err)
	}
}

func TestInstallModelGatewayWiresCompletions(t *testing.T) {
	if strings.Contains(modelGatewaySrc, "StatusNotImplemented") ||
		strings.Contains(modelGatewaySrc, "not wired completions") {
		t.Fatal("the installed gateway still 501s complete — Studio chat would stall")
	}
	if !strings.Contains(modelGatewaySrc, "/chat/completions") {
		t.Fatal("the gateway does not drive an HTTP completion")
	}
	if !strings.Contains(modelGatewaySrc, "hubLookCLI") {
		t.Fatal("the gateway does not drive a local CLI")
	}
}

func TestInstalledGatewayCompletesAndListsProviders(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module hubgwtest\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := strings.Replace(modelGatewaySrc, "package orchestrator", "package hubgwtest", 1)
	if err := os.WriteFile(filepath.Join(dir, "model_gateway.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "model_gateway_test.go"), []byte(installedGatewayProbe), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installed gateway failed:\n%s", out)
	}
}

const installedGatewayProbe = `package hubgwtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompleteHitsConfiguredHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(` + "`" + `{"choices":[{"message":{"content":"from-the-hub"}}]}` + "`" + `))
	}))
	t.Cleanup(upstream.Close)

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".weblisk"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := ` + "`" + `{"provider":"ollama","model":"llama3.1","base_url":"` + "`" + ` + upstream.URL + ` + "`" + `"}` + "`" + `
	if err := os.WriteFile(filepath.Join(root, ".weblisk", "model.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	h := wrapModelGateway(http.NotFoundHandler())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/complete",
		strings.NewReader(` + "`" + `{"messages":[{"role":"user","content":"hi"}]}` + "`" + `))
	req.Header.Set("Authorization", "Bearer tok")
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("complete %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "from-the-hub") {
		t.Fatalf("body %s", rr.Body.String())
	}
}

func TestProvidersNeverEmpty(t *testing.T) {
	h := wrapModelGateway(http.NotFoundHandler())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/model/providers", nil)
	req.Header.Set("Authorization", "Bearer tok")
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("providers %d: %s", rr.Code, rr.Body.String())
	}
	var found []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &found); err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 {
		t.Fatal("discovery returned nothing — even an empty machine must list what is missing")
	}
}

func TestCompleteRefusesAnUnconfiguredTenant(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	h := wrapModelGateway(http.NotFoundHandler())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/complete",
		strings.NewReader(` + "`" + `{"messages":[{"role":"user","content":"hi"}]}` + "`" + `))
	req.Header.Set("Authorization", "Bearer tok")
	h.ServeHTTP(rr, req)
	if rr.Code != 409 {
		t.Fatalf("status %d, want 409: %s", rr.Code, rr.Body.String())
	}
}
`
