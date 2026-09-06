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
	for _, want := range []string{"claude-code", "ollama", "anthropic"} {
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

// Ambiguity is a question, not a failure — and the question names the options
// and the flag that answers it.
func TestAnAmbiguousMachineIsToldHowToChoose(t *testing.T) {
	err := ambiguousProviderError([]ProviderInfo{
		{Kind: ProviderClaudeCode, Label: "Claude Code", Local: true},
		{Kind: ProviderOllama, Label: "Ollama", Local: true, Model: "codellama:70b"},
	})
	msg := err.Error()
	for _, want := range []string{"claude-code", "ollama", "--provider", "will not guess"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message omits %q:\n%s", want, msg)
		}
	}
}

// The model ids are named in ONE place. The previous defaults aged out while
// sitting in four switch arms nobody re-read.
func TestModelDefaultsAreCentralAndCurrent(t *testing.T) {
	if defaultModels[ProviderAnthropic] != "claude-opus-5" {
		t.Errorf("anthropic default = %q", defaultModels[ProviderAnthropic])
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
	for _, k := range []ProviderKind{ProviderClaudeCode, ProviderCodex} {
		if defaultModels[k] != "" {
			t.Errorf("%s names a model (%q); it should defer to the tool", k, defaultModels[k])
		}
	}
}
