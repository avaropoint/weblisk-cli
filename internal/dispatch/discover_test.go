package dispatch

import (
	"context"
	"strings"
	"testing"
)

// A requested provider that cannot be used is an ERROR, never a fall-back.
//
// This is the data-residency guarantee. An operator who pinned a tenant to a
// local model did so because that tenant's blueprints may not leave the
// machine; silently generating with a hosted API instead because the local one
// was not running would be the single worst thing this resolver could do.
func TestARequestedProviderIsNeverSilentlySubstituted(t *testing.T) {
	// A provider that certainly has no key in a test environment.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("WL_AI_KEY", "")

	c := Resolve(context.Background(), "anthropic")
	if c.Err == nil {
		t.Fatalf("an unusable provider resolved to %q instead of failing", c.Kind)
	}
	if c.Kind != "" {
		t.Errorf("a failed resolution still named a provider: %q", c.Kind)
	}
	if !strings.Contains(c.Err.Error(), "no key") {
		t.Errorf("the refusal does not say what is missing: %v", c.Err)
	}
}

// An unknown name is a typo, and is reported as one with the list.
func TestAnUnknownProviderNamesTheOnesThatExist(t *testing.T) {
	c := Resolve(context.Background(), "gpt5-turbo-max")
	if c.Err == nil {
		t.Fatal("an unknown provider was accepted")
	}
	for _, want := range []string{"claude-code", "grok", "ollama", "anthropic", "xai"} {
		if !strings.Contains(c.Err.Error(), want) {
			t.Errorf("the error does not list %q: %v", want, c.Err)
		}
	}
}

// The aliases that already exist in the wild keep working.
func TestTheOldNamesStillResolve(t *testing.T) {
	for in, want := range map[string]ProviderKind{
		"claude":       ProviderClaudeCode,
		"claude-local": ProviderClaudeCode,
		"claude_code":  ProviderClaudeCode,
		"codex-cli":    ProviderCodex,
		"local":        ProviderOllama,
		"ollama":       ProviderOllama,
		"anthropic":    ProviderAnthropic,
		"grok-cli":     ProviderGrok,
		"grok-code":    ProviderGrok,
		"grok-api":     ProviderXAI,
		"x-ai":         ProviderXAI,
		"google":       ProviderGemini,
	} {
		if got := normaliseKind(ProviderKind(in)); got != want {
			t.Errorf("normaliseKind(%q) = %q; want %q", in, got, want)
		}
	}
}

// Preference order is local-first, and a default is only ever taken from what
// was actually found.
func TestADefaultIsOnlyEverChosenFromWhatExists(t *testing.T) {
	all := []ProviderInfo{
		{Kind: ProviderOpenAI, Available: true, Model: "gpt-4o"},
		{Kind: ProviderClaudeCode, Available: false},
		{Kind: ProviderOllama, Available: true, Model: "codellama:70b"},
	}
	// Claude Code is preferred but absent, so the local model wins over the API.
	if k, m := Default(all); k != ProviderOllama || m != "codellama:70b" {
		t.Errorf("Default = %q/%q; want ollama, and never the unavailable claude-code", k, m)
	}
	// Nothing available is not a provider.
	if k, _ := Default([]ProviderInfo{{Kind: ProviderOpenAI, Available: false}}); k != "" {
		t.Errorf("Default picked %q from nothing available", k)
	}
	// And the order really is local-before-hosted.
	both := []ProviderInfo{
		{Kind: ProviderAnthropic, Available: true},
		{Kind: ProviderClaudeCode, Available: true},
	}
	if k, _ := Default(both); k != ProviderClaudeCode {
		t.Errorf("Default = %q; a subscription already on the machine outranks a metered key", k)
	}
}

// Every provider reports WHY, available or not. "No provider" sends somebody
// looking for something to install; "claude is not on PATH, and not in …" does
// not.
func TestEveryProviderSaysWhy(t *testing.T) {
	for _, s := range Available(context.Background()) {
		if strings.TrimSpace(s.Detail) == "" {
			t.Errorf("%s reports availability=%v with no reason", s.Kind, s.Available)
		}
		if s.Label == "" {
			t.Errorf("%s has no human name", s.Kind)
		}
	}
}

// When nobody has chosen, the operator default walks the catalog from the
// top and takes the first backend this machine can actually run. Several
// available is not a question.
func TestAnUnspecifiedMachineTakesTheHighestWeightedAvailable(t *testing.T) {
	usable := []ProviderInfo{
		{Kind: ProviderOpenAI, Available: true, Model: "gpt-4o"},
		{Kind: ProviderGrok, Available: true},
		{Kind: ProviderOllama, Available: true, Model: "llama3.1"},
	}
	c := chooseFromUsable(usable, usable)
	if c.Err != nil {
		t.Fatalf("several available failed: %v", c.Err)
	}
	if c.Ambiguous {
		t.Fatal("several available was treated as a question rather than a default")
	}
	if c.Kind != ProviderGrok {
		t.Errorf("got %q, want grok — the highest-weighted among these that exists", c.Kind)
	}
	if !strings.Contains(c.Why, "highest-weighted") {
		t.Errorf("why = %q, want it to say this is the ranking", c.Why)
	}

	// A higher-weighted backend that is not actually available is skipped.
	skipped := []ProviderInfo{
		{Kind: ProviderClaudeCode, Available: false},
		{Kind: ProviderGrok, Available: true},
		{Kind: ProviderOpenAI, Available: true, Model: "gpt-4o"},
	}
	c = chooseFromUsable([]ProviderInfo{skipped[1], skipped[2]}, skipped)
	if c.Kind != ProviderGrok {
		t.Errorf("got %q, want grok after skipping unavailable claude-code", c.Kind)
	}

	only := []ProviderInfo{{Kind: ProviderOllama, Available: true, Model: "codellama:70b"}}
	c = chooseFromUsable(only, only)
	if c.Kind != ProviderOllama || c.Why != "the only provider available on this machine" {
		t.Errorf("sole provider = %q (%s)", c.Kind, c.Why)
	}
}

// The model ids are named in ONE place. The previous defaults aged out while
// sitting in four switch arms nobody re-read.
func TestModelDefaultsAreCentralAndCurrent(t *testing.T) {
	if defaultModels[ProviderAnthropic] != "claude-opus-5" {
		t.Errorf("anthropic default = %q", defaultModels[ProviderAnthropic])
	}
	if defaultModels[ProviderXAI] != "grok-4.6" {
		t.Errorf("xai default = %q", defaultModels[ProviderXAI])
	}
	// A dated suffix is the shape of a model id that has been copied from
	// somewhere stale — the current ids carry none.
	for kind, model := range defaultModels {
		if strings.Count(model, "-") >= 3 && strings.ContainsAny(model, "0123456789") {
			for _, part := range strings.Split(model, "-") {
				if len(part) == 8 && strings.HasPrefix(part, "202") {
					t.Errorf("%s default %q carries a date suffix", kind, model)
				}
			}
		}
	}
	// The CLI-backed kinds deliberately name no model: the tool has its own,
	// and overriding it here would contradict a choice made in that tool.
	for _, k := range []ProviderKind{ProviderClaudeCode, ProviderGrok, ProviderCodex} {
		if defaultModels[k] != "" {
			t.Errorf("%s names a model (%q); it should defer to the tool", k, defaultModels[k])
		}
	}
}

func TestTheCatalogNamesGrokAndTheGenericEscapes(t *testing.T) {
	kinds := map[ProviderKind]bool{}
	for _, s := range Available(context.Background()) {
		kinds[s.Kind] = true
	}
	for _, want := range []ProviderKind{ProviderClaudeCode, ProviderGrok, ProviderXAI, ProviderOllama, ProviderOpenAI} {
		if !kinds[want] {
			t.Errorf("Available() does not list %s", want)
		}
	}
}

func TestAVendorKeyMakesTheHostedProviderAvailable(t *testing.T) {
	t.Setenv("WL_AI_KEY", "")
	t.Setenv("XAI_API_KEY", "xai-test")
	c := Resolve(context.Background(), "xai")
	if c.Err != nil {
		t.Fatalf("xai with XAI_API_KEY set was refused: %v", c.Err)
	}
	if c.Kind != ProviderXAI {
		t.Errorf("kind = %q, want xai", c.Kind)
	}
	if c.Model != "grok-4.6" {
		t.Errorf("model = %q, want grok-4.6", c.Model)
	}
}

func TestAnUnknownNameWithABaseURLIsACustomEndpoint(t *testing.T) {
	t.Setenv("WL_AI_BASE_URL", "http://127.0.0.1:9999/v1")
	t.Setenv("WL_AI_MODEL", "my-local-llama")
	c := Resolve(context.Background(), "vllm")
	if c.Err != nil {
		t.Fatalf("a named OpenAI-compatible endpoint was refused: %v", c.Err)
	}
	if c.Kind != "vllm" {
		t.Errorf("kind = %q, want vllm", c.Kind)
	}
	if c.Model != "my-local-llama" {
		t.Errorf("model = %q, want my-local-llama", c.Model)
	}
}

func TestGrokIsPreferredOverHostedAPIs(t *testing.T) {
	opts := []ProviderInfo{
		{Kind: ProviderOpenAI, Available: true, Model: "gpt-4o"},
		{Kind: ProviderGrok, Available: true},
		{Kind: ProviderXAI, Available: true, Model: "grok-4.6"},
	}
	if k, _ := Default(opts); k != ProviderGrok {
		t.Errorf("Default = %q; a local Grok CLI outranks a keyed API", k)
	}
	both := []ProviderInfo{
		{Kind: ProviderGrok, Available: true},
		{Kind: ProviderClaudeCode, Available: true},
	}
	if k, _ := Default(both); k != ProviderClaudeCode {
		t.Errorf("Default = %q; claude-code remains first among local CLIs", k)
	}
}
