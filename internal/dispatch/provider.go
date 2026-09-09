package dispatch

// LLM Provider
//
// Abstraction over LLM backends. The catalog in catalog.go names the
// first-class ones (local CLIs, local HTTP, hosted APIs). Anything else is
// an OpenAI-compatible URL (WL_AI_BASE_URL) or a local binary (local-cli).
// Configured via WL_AI_* environment variables, --provider, or discovery.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Interface

// Provider is the interface for AI model backends.
type Provider interface {
	Chat(messages []Message) (string, error)
}

// Message is a single chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenAI-compatible

// OpenAIProvider implements the Provider interface for OpenAI-compatible APIs.
type OpenAIProvider struct {
	BaseURL string
	APIKey  string
	Model   string
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *OpenAIProvider) Chat(messages []Message) (string, error) {
	body, err := json.Marshal(chatRequest{Model: p.Model, Messages: messages})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", &ProviderFault{Provider: "provider", Message: "request failed: " + err.Error()}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", httpFault("provider", resp.StatusCode, respBody)
	}

	var result chatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing response: %w", err)
	}
	if result.Error != nil {
		return "", &ProviderFault{Provider: "provider", Message: result.Error.Message}
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("provider returned no choices")
	}
	return result.Choices[0].Message.Content, nil
}

// Anthropic

// AnthropicProvider implements the Provider interface for Anthropic's API.
type AnthropicProvider struct {
	BaseURL string
	APIKey  string
	Model   string
}

type anthropicRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *AnthropicProvider) Chat(messages []Message) (string, error) {
	var system string
	var apiMsgs []Message
	for _, m := range messages {
		if m.Role == "system" {
			system = m.Content
		} else {
			apiMsgs = append(apiMsgs, m)
		}
	}

	body, err := json.Marshal(anthropicRequest{
		Model: p.Model, MaxTokens: 8192, System: system, Messages: apiMsgs,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", p.BaseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", &ProviderFault{Provider: "anthropic", Message: "request failed: " + err.Error()}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", httpFault("anthropic", resp.StatusCode, respBody)
	}

	var result anthropicResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing anthropic response: %w", err)
	}
	if result.Error != nil {
		return "", &ProviderFault{Provider: "anthropic", Message: result.Error.Message}
	}

	var text strings.Builder
	for _, c := range result.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	return text.String(), nil
}

// Factory

// NewProvider creates a Provider from WL_AI_* environment variables, with
// transient-failure retry already applied.
//
// The retry is HERE rather than at the call sites deliberately. It used to be
// applied in one place, RequireProvider, and every other way of getting a
// provider silently got none — which is the same shape as the bug that made the
// retry inert in the first place: a policy that exists but is not reached.
// Wrapping at construction makes the retrying provider the only kind there is,
// so a new call site cannot forget.
//
// newRawProvider is for the one caller that must NOT retry: a status probe,
// where six minutes of patience would be six minutes of an operator watching a
// prompt that has not come back.
func NewProvider() (Provider, error) {
	p, err := newRawProvider()
	if err != nil {
		return nil, err
	}
	return WithTransientRetry(p), nil
}

// selected is the provider a command chose for this run, from a --provider flag
// or from asking. It outranks the environment because it is more specific: an
// operator typing --provider on this invocation means it for this invocation.
var selected struct {
	kind  ProviderKind
	model string
	set   bool
}

// UseProvider pins this process to one backend. Called by the command layer once
// a choice has been made — see Resolve in discover.go.
func UseProvider(kind ProviderKind, model string) {
	selected.kind, selected.model, selected.set = kind, model, true
}

// SelectedProvider reports the pinned choice, if there is one.
func SelectedProvider() (ProviderKind, string, bool) {
	return selected.kind, selected.model, selected.set
}

func newRawProvider() (Provider, error) {
	api := os.Getenv("WL_AI_PROVIDER")
	model := os.Getenv("WL_AI_MODEL")
	if selected.set {
		api = string(selected.kind)
		if selected.model != "" {
			model = selected.model
		}
	}
	// No choice and no environment: take the highest-weighted backend this
	// machine can actually GENERATE with, rather than defaulting to "openai"
	// and demanding a key.
	//
	// That default is why `weblisk server init` failed out of the box on a
	// machine with Claude Code installed and logged in — a working provider on
	// the PATH, and nothing looked. Walking the catalog from the top is the
	// operator default; --provider still pins, and a pinned backend that cannot
	// run is still an error rather than a silent fall-back.
	//
	// ResolveReady, not Resolve, and this is the line that matters most:
	// ChooseProvider is called from exactly ONE place, `server init`. Every
	// other generating command — `agent create`, `domain create`,
	// `gateway create`, `pattern apply` — arrives here instead, through
	// RequireProvider. Verifying only in ChooseProvider would have fixed
	// "installed but not logged in" for one command out of five.
	if api == "" {
		c := ResolveReady(context.Background(), "")
		if c.Err != nil {
			return nil, c.Err
		}
		if c.Ambiguous {
			k, m := Default(c.Options)
			c = Choice{Kind: k, Model: m}
		}
		api = string(c.Kind)
		if model == "" {
			model = c.Model
		}
		// Pinned for the rest of the process, so the walk happens once per run
		// rather than once per call to this function — and so RequireProvider
		// can see that this backend has already answered and skip asking again.
		UseProvider(ProviderKind(api), model)
	}
	return BuildProvider(normaliseKind(ProviderKind(api)), model)
}

// BuildProvider constructs one named backend, with no retry wrapper and
// without consulting the pinned selection.
//
// Split out of newRawProvider so the readiness walk in discover.go can build a
// candidate it has NOT pinned. Choosing a provider used to mean pinning it
// first and finding out whether it works afterwards, which is precisely how an
// installed-but-unauthenticated CLI became the default for a whole build.
func BuildProvider(kind ProviderKind, model string) (Provider, error) {
	kind = normaliseKind(kind)

	// Local coding-agent CLIs are a subprocess, not an HTTP endpoint: no base URL,
	// no key, and the credential is whatever the tool is already logged in with.
	switch kind {
	case ProviderClaudeCode, ProviderGrok, ProviderCodex, ProviderLocalCLI:
		return newLocalCLIProvider(string(kind), model)
	}

	baseURL := strings.TrimRight(os.Getenv("WL_AI_BASE_URL"), "/")
	b := lookupBackend(kind)
	if b == nil {
		if baseURL == "" {
			return nil, fmt.Errorf("WL_AI_BASE_URL required for custom provider %q — "+
				"this pipeline drives %s; anything else is an OpenAI-compatible HTTP endpoint "+
				"or local-cli", kind, strings.Join(kindNames(), ", "))
		}
		if model == "" {
			model = "default"
		}
		return &OpenAIProvider{BaseURL: baseURL, APIKey: os.Getenv("WL_AI_KEY"), Model: model}, nil
	}
	return buildHTTPProvider(b, baseURL, model)
}

func buildHTTPProvider(b *backend, baseURL, model string) (Provider, error) {
	if baseURL == "" {
		baseURL = b.BaseURL
	}
	if b.Driver == driverOllama && baseURL == "" {
		baseURL = "http://localhost:11434/v1"
	}
	if b.RequiresURL && baseURL == "" {
		return nil, fmt.Errorf("WL_AI_BASE_URL required for %s", b.Label)
	}
	if baseURL == "" {
		return nil, fmt.Errorf("WL_AI_BASE_URL required for %s", b.Label)
	}
	if model == "" {
		if b.Driver == driverOllama {
			// Whatever this machine actually has pulled, not a name that may
			// never have been downloaded — naming an absent model produces a
			// 404 at generation time, which reads as a broken pipeline.
			model = discoveredOllamaModel()
		} else if b.Local {
			st := probeLocalOpenAI(context.Background(), *b)
			if st.Model != "" {
				model = st.Model
			} else {
				model = defaultModels[b.Kind]
			}
		} else {
			model = defaultModels[b.Kind]
		}
	}
	if model == "" {
		model = "default"
	}

	key := apiKeyFor(b)
	switch b.Driver {
	case driverAnthropic:
		if key == "" {
			return nil, fmt.Errorf("%s required for %s", b.keyHint(), b.Label)
		}
		return &AnthropicProvider{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: key, Model: model}, nil
	case driverOllama:
		if key == "" {
			key = "ollama"
		}
		return &OpenAIProvider{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: key, Model: model}, nil
	default:
		if !b.Local && key == "" && !b.RequiresURL {
			return nil, fmt.Errorf("%s required for %s", b.keyHint(), b.Label)
		}
		// Cloudflare needs a URL and accepts an empty key (some gateways do).
		// Local OpenAI-compatible servers (LM Studio) typically need none.
		if b.RequiresURL && key == "" {
			key = os.Getenv("WL_AI_KEY")
		}
		if key == "" && b.Local {
			key = string(b.Kind)
		}
		return &OpenAIProvider{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: key, Model: model}, nil
	}
}

// Provider Config

// ProviderConfig describes the current AI provider configuration.
type ProviderConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url"`
	HasKey   bool   `json:"has_key"`
}
