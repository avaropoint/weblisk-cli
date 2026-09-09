package hubmodel

// hubmodel is the tenant's own model configuration and the HTTP surface Studio
// uses to talk to it.
//
// Studio must not run tenant chat against a provider it configured for itself.
// This package is what a hub mounts, and what Create stamps into .weblisk/model.json.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/avaropoint/weblisk-cli/internal/dispatch"
)

// Config is what a hub stores. No key — the name of an env var, read at use.
type Config struct {
	Provider  string `json:"provider"`
	Model     string `json:"model,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
}

// Store persists Config under <root>/.weblisk/model.json.
type Store struct {
	mu   sync.Mutex
	path string
	cfg  Config
}

// Open loads (or initialises empty) the config at root.
func Open(root string) *Store {
	s := &Store{path: filepath.Join(root, ".weblisk", "model.json")}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(data, &s.cfg)
	return s
}

func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

func (s *Store) Set(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	c.Model = strings.TrimSpace(c.Model)
	c.BaseURL = strings.TrimSpace(c.BaseURL)
	c.APIKeyEnv = strings.TrimSpace(c.APIKeyEnv)
	s.cfg = c
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, append(data, '\n'), 0o600)
}

// Handler serves the four model routes. Auth is the hub's middleware; this
// assumes the caller already required admin.
func (s *Store) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/admin/model", s.get)
	mux.HandleFunc("PUT /v1/admin/model", s.put)
	mux.HandleFunc("GET /v1/admin/model/providers", s.providers)
	mux.HandleFunc("POST /v1/admin/complete", s.complete)
	return mux
}

func (s *Store) get(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Get())
}

func (s *Store) put(w http.ResponseWriter, r *http.Request) {
	var c Config
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read the body"})
		return
	}
	if strings.TrimSpace(c.Provider) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a provider is required"})
		return
	}
	if err := s.Set(c); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.Get())
}

func (s *Store) providers(w http.ResponseWriter, r *http.Request) {
	found := dispatch.Available(r.Context())
	writeJSON(w, http.StatusOK, found)
}

type completeRequest struct {
	System   string             `json:"system"`
	Messages []dispatch.Message `json:"messages"`
	Model    string             `json:"model,omitempty"`
}

type completeResponse struct {
	Content  string `json:"content"`
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
}

var completeMu sync.Mutex

func (s *Store) complete(w http.ResponseWriter, r *http.Request) {
	var req completeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read the body"})
		return
	}
	cfg := s.Get()
	if cfg.Provider == "" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "this tenant has no model configured",
		})
		return
	}
	model := req.Model
	if model == "" {
		model = cfg.Model
	}
	msgs := req.Messages
	if req.System != "" {
		msgs = append([]dispatch.Message{{Role: "system", Content: req.System}}, msgs...)
	}
	if len(msgs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provide a message"})
		return
	}

	completeMu.Lock()
	defer completeMu.Unlock()
	dispatch.UseProvider(dispatch.ProviderKind(cfg.Provider), model)
	p, err := dispatch.NewProvider()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	text, err := p.Chat(msgs)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, completeResponse{
		Content:  text,
		Provider: cfg.Provider,
		Model:    model,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
