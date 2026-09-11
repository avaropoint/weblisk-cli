package dispatch

// The advisory budget must reach the provider a real run actually holds.
//
// advisory.go's own comment says a step that cannot be bounded must say so out
// loud. It said so on every run — because the bound could never be applied, not
// because the provider could not take one.

import (
	"testing"
	"time"
)

// RequireProvider hands back a retry-wrapped provider. If the wrapper does not
// forward WithBounds, the advisory step is unbounded on every real run and the
// note explaining that prints every time.
func TestTheAdvisoryBudgetReachesARetryWrappedProvider(t *testing.T) {
	raw := &LocalCLIProvider{Name: "claude-code", Bin: "claude", Stream: true}
	// Exactly the shape provider.go returns from NewProvider.
	wrapped := WithTransientRetry(raw)

	bounded, ok := AdvisoryProvider(wrapped, 85*time.Minute)
	if !ok {
		t.Fatal("a retry-wrapped local CLI reports it cannot be time-bounded — " +
			"this is the shape every real run uses, so the budget never applies")
	}
	// The bound must be on the real provider underneath, not lost in the wrapper.
	lp, is := Underlying(bounded).(*LocalCLIProvider)
	if !is {
		t.Fatalf("the bounded provider is not a local CLI: %T", Underlying(bounded))
	}
	want := AdvisoryBudget(85 * time.Minute)
	if lp.TotalCap != want {
		t.Errorf("TotalCap = %s, want %s", lp.TotalCap, want)
	}
	if lp.IdleTimeout != advisoryIdle {
		t.Errorf("IdleTimeout = %s, want %s", lp.IdleTimeout, advisoryIdle)
	}
	// And it must still retry: bounding a provider is not a reason to lose the
	// transient handling every other call path gets.
	if _, isRetry := bounded.(*retryingProvider); !isRetry {
		t.Errorf("the bounded provider lost its retry wrapper: %T", bounded)
	}
}

// A non-streaming provider reads Timeout and nothing else. Bounding one by
// setting only the streaming fields produces a copy that reports a bound and
// runs to the ten-minute default.
func TestBoundingANonStreamingProviderActuallyBoundsIt(t *testing.T) {
	raw := &LocalCLIProvider{Name: "codex", Bin: "codex"} // no Stream
	bounded, ok := AdvisoryProvider(WithTransientRetry(raw), 85*time.Minute)
	if !ok {
		t.Fatal("a non-streaming provider reports it cannot be bounded")
	}
	lp := Underlying(bounded).(*LocalCLIProvider)
	want := AdvisoryBudget(85 * time.Minute)
	if lp.Timeout == 0 || lp.Timeout > want {
		t.Errorf("Timeout = %s, want it clamped to %s — TotalCap and IdleTimeout are "+
			"read only by the streaming path, so this is the one field that bounds codex",
			lp.Timeout, want)
	}
}

// A bound never loosens a limit that was already tighter.
func TestABoundNeverLoosensAnExistingLimit(t *testing.T) {
	raw := &LocalCLIProvider{Name: "codex", Bin: "codex", Timeout: 30 * time.Second}
	bounded, ok := AdvisoryProvider(WithTransientRetry(raw), 85*time.Minute)
	if !ok {
		t.Fatal("not bounded")
	}
	if got := Underlying(bounded).(*LocalCLIProvider).Timeout; got != 30*time.Second {
		t.Errorf("Timeout = %s, want the tighter existing 30s", got)
	}
}

// A provider that genuinely cannot be bounded still says so, which is the whole
// reason AdvisoryProvider returns a second value.
func TestAnUnboundableProviderStillReportsItself(t *testing.T) {
	if _, ok := AdvisoryProvider(&unboundableProvider{}, 85*time.Minute); ok {
		t.Error("a provider with no WithBounds reported that it was bounded")
	}
	// And through the retry wrapper, which must not manufacture a bound it
	// cannot enforce.
	if _, ok := AdvisoryProvider(WithTransientRetry(&unboundableProvider{}), 85*time.Minute); ok {
		t.Error("the retry wrapper reported a bound its inner provider cannot enforce")
	}
}
