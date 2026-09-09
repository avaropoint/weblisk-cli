package dispatch

import (
	"os"
	"strings"
)

// catalog.go — the backends this pipeline knows by name, and how each is driven.
//
// # Why a catalog rather than another switch
//
// ProviderKind, preference order, default models, aliases, discovery probes and
// HTTP construction used to be four lists that had to be edited together. Grok
// was the provider that made that shape fail: adding it meant touching every
// list, and the next popular tool would mean the same. One table is the list.
// A backend that speaks OpenAI chat-completions is a row. A local coding-agent
// CLI with verified flags is a row. Anything else is still reachable: an
// OpenAI-compatible URL via WL_AI_BASE_URL, or any binary via local-cli.
//
// # What is allowed to be a first-class CLI
//
// Only tools whose headless flags have been read from the tool itself. Inventing
// another binary's argv produces a provider that `weblisk providers` offers and
// that fails on first use. claude-code, grok and codex are the verified presets.
// Everything else on PATH is `local-cli` plus WL_AI_COMMAND / WL_AI_ARGS.

// ProviderKind is a backend this pipeline knows how to drive.
type ProviderKind string

const (
	ProviderClaudeCode ProviderKind = "claude-code"
	ProviderGrok       ProviderKind = "grok"
	ProviderCodex      ProviderKind = "codex"
	ProviderOllama     ProviderKind = "ollama"
	ProviderLMStudio   ProviderKind = "lmstudio"
	ProviderAnthropic  ProviderKind = "anthropic"
	ProviderXAI        ProviderKind = "xai"
	ProviderOpenAI     ProviderKind = "openai"
	ProviderGemini     ProviderKind = "gemini"
	ProviderGroq       ProviderKind = "groq"
	ProviderMistral    ProviderKind = "mistral"
	ProviderDeepSeek   ProviderKind = "deepseek"
	ProviderOpenRouter ProviderKind = "openrouter"
	ProviderCloudflare ProviderKind = "cloudflare"
	ProviderLocalCLI   ProviderKind = "local-cli"
)

// backendDriver is how a catalog row is reached. Discovery and construction
// both switch on this, so a new OpenAI-compatible API is a row, not a new arm
// in each.
type backendDriver string

const (
	driverLocalCLI  backendDriver = "local-cli"
	driverOllama    backendDriver = "ollama"
	driverOpenAI    backendDriver = "openai-compat"
	driverAnthropic backendDriver = "anthropic"
)

// backend is one named backend. Slice order is weight, highest first: local
// coding-agent CLIs, then local HTTP, then keyed APIs. The operator default
// walks this list and takes the first backend this workstation can actually
// run — see Default.
type backend struct {
	Kind         ProviderKind
	Label        string
	Local        bool
	Aliases      []string
	Driver       backendDriver
	Binary       string   // local CLI name on PATH
	KeyEnvs      []string // vendor keys; WL_AI_KEY always outranks these
	BaseURL      string   // default HTTP endpoint, including /v1
	DefaultModel string
	RequiresURL  bool // true when there is no sensible default endpoint
}

// backends is the catalog. Weight is the order of this slice, highest first.
var backends = []backend{
	{
		Kind:    ProviderClaudeCode,
		Label:   "Claude Code",
		Local:   true,
		Driver:  driverLocalCLI,
		Binary:  "claude",
		Aliases: []string{"claude", "claude-local", "claude_code"},
	},
	{
		Kind:    ProviderGrok,
		Label:   "Grok CLI",
		Local:   true,
		Driver:  driverLocalCLI,
		Binary:  "grok",
		Aliases: []string{"grok-cli", "grok-code", "grok-build"},
	},
	{
		Kind:    ProviderCodex,
		Label:   "Codex CLI",
		Local:   true,
		Driver:  driverLocalCLI,
		Binary:  "codex",
		Aliases: []string{"codex-cli"},
	},
	{
		Kind:         ProviderOllama,
		Label:        "Ollama",
		Local:        true,
		Driver:       driverOllama,
		Aliases:      []string{"local"},
		DefaultModel: "llama3.1",
	},
	{
		Kind:    ProviderLMStudio,
		Label:   "LM Studio",
		Local:   true,
		Driver:  driverOpenAI,
		BaseURL: "http://localhost:1234/v1",
	},
	{
		Kind:         ProviderAnthropic,
		Label:        "Anthropic API",
		Driver:       driverAnthropic,
		KeyEnvs:      []string{"ANTHROPIC_API_KEY"},
		BaseURL:      "https://api.anthropic.com/v1",
		DefaultModel: "claude-opus-5",
	},
	{
		Kind:         ProviderXAI,
		Label:        "xAI API",
		Driver:       driverOpenAI,
		KeyEnvs:      []string{"XAI_API_KEY"},
		BaseURL:      "https://api.x.ai/v1",
		DefaultModel: "grok-4.6",
		Aliases:      []string{"x-ai", "grok-api"},
	},
	{
		Kind:         ProviderOpenAI,
		Label:        "OpenAI API",
		Driver:       driverOpenAI,
		KeyEnvs:      []string{"OPENAI_API_KEY"},
		BaseURL:      "https://api.openai.com/v1",
		DefaultModel: "gpt-4o",
	},
	{
		Kind:         ProviderGemini,
		Label:        "Google Gemini API",
		Driver:       driverOpenAI,
		KeyEnvs:      []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		BaseURL:      "https://generativelanguage.googleapis.com/v1beta/openai",
		DefaultModel: "gemini-2.5-pro",
		Aliases:      []string{"google", "google-ai"},
	},
	{
		Kind:         ProviderGroq,
		Label:        "Groq API",
		Driver:       driverOpenAI,
		KeyEnvs:      []string{"GROQ_API_KEY"},
		BaseURL:      "https://api.groq.com/openai/v1",
		DefaultModel: "llama-3.3-70b-versatile",
	},
	{
		Kind:         ProviderMistral,
		Label:        "Mistral API",
		Driver:       driverOpenAI,
		KeyEnvs:      []string{"MISTRAL_API_KEY"},
		BaseURL:      "https://api.mistral.ai/v1",
		DefaultModel: "mistral-large-latest",
	},
	{
		Kind:         ProviderDeepSeek,
		Label:        "DeepSeek API",
		Driver:       driverOpenAI,
		KeyEnvs:      []string{"DEEPSEEK_API_KEY"},
		BaseURL:      "https://api.deepseek.com/v1",
		DefaultModel: "deepseek-chat",
		Aliases:      []string{"deep-seek"},
	},
	{
		Kind:         ProviderOpenRouter,
		Label:        "OpenRouter",
		Driver:       driverOpenAI,
		KeyEnvs:      []string{"OPENROUTER_API_KEY"},
		BaseURL:      "https://openrouter.ai/api/v1",
		DefaultModel: "openai/gpt-4o",
	},
	{
		Kind:         ProviderCloudflare,
		Label:        "Cloudflare Workers AI",
		Driver:       driverOpenAI,
		KeyEnvs:      []string{"CF_API_TOKEN", "CLOUDFLARE_API_TOKEN"},
		DefaultModel: "@cf/meta/llama-3-8b-instruct",
		RequiresURL:  true,
		Aliases:      []string{"cf"},
	},
}

// defaultModels are the models used when nobody names one. Derived from the
// catalog so the current id is named in ONE place — the previous defaults
// (claude-sonnet-4-20250514, gpt-4o sitting in four switch arms) aged out
// while nobody re-read them.
var defaultModels = map[ProviderKind]string{}

var aliasToKind = map[ProviderKind]ProviderKind{}

func init() {
	for _, b := range backends {
		defaultModels[b.Kind] = b.DefaultModel
		for _, a := range b.Aliases {
			aliasToKind[ProviderKind(a)] = b.Kind
		}
	}
	// CLI-backed kinds deliberately name no model: the tool has its own
	// configured default, and overriding it here would contradict a choice
	// made in that tool.
	defaultModels[ProviderClaudeCode] = ""
	defaultModels[ProviderGrok] = ""
	defaultModels[ProviderCodex] = ""
	defaultModels[ProviderLocalCLI] = ""
}

func lookupBackend(k ProviderKind) *backend {
	k = normaliseKind(k)
	for i := range backends {
		if backends[i].Kind == k {
			return &backends[i]
		}
	}
	return nil
}

func preferenceOrder() []ProviderKind {
	out := make([]ProviderKind, 0, len(backends))
	for _, b := range backends {
		out = append(out, b.Kind)
	}
	return out
}

func kindNames() []string {
	out := make([]string, 0, len(backends)+1)
	for _, b := range backends {
		out = append(out, string(b.Kind))
	}
	out = append(out, string(ProviderLocalCLI))
	return out
}

// apiKeyFor returns the key a backend will use. WL_AI_KEY outranks the vendor
// variable because an operator who set it meant it for this program specifically.
func apiKeyFor(b *backend) string {
	if k := strings.TrimSpace(os.Getenv("WL_AI_KEY")); k != "" {
		return k
	}
	if b == nil {
		return ""
	}
	for _, e := range b.KeyEnvs {
		if k := strings.TrimSpace(os.Getenv(e)); k != "" {
			return k
		}
	}
	return ""
}

func (b backend) keyHint() string {
	if len(b.KeyEnvs) == 0 {
		return "WL_AI_KEY"
	}
	if len(b.KeyEnvs) == 1 {
		return b.KeyEnvs[0] + " (or WL_AI_KEY)"
	}
	return b.KeyEnvs[0] + " (or WL_AI_KEY)"
}
