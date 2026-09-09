package dispatch

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// The budget scales with the work it comments on, and is bounded both ways.
//
// A constant would be wrong in both directions: too small for a tenant whose
// generation takes forty minutes and reads 29 files, and absurdly large for one
// that takes two. Generation's elapsed time is the measure of size available at
// the moment the decision is made.
func TestTheAdvisoryBudgetScalesAndIsBounded(t *testing.T) {
	// A fast build still gets a real attempt.
	if got := AdvisoryBudget(10 * time.Second); got != advisoryFloor {
		t.Errorf("budget for a 10s build = %s, want the %s floor", got, advisoryFloor)
	}
	// A middling build gets a proportionate share.
	if got := AdvisoryBudget(15 * time.Minute); got != 3*time.Minute {
		t.Errorf("budget for a 15m build = %s, want 3m (a fifth)", got)
	}
	// A long build's commentary cannot become a second build.
	if got := AdvisoryBudget(4 * time.Hour); got != advisoryCeiling {
		t.Errorf("budget for a 4h build = %s, want the %s ceiling", got, advisoryCeiling)
	}
	// Monotonic: more work never buys less budget.
	prev := time.Duration(0)
	for _, d := range []time.Duration{time.Second, time.Minute, 10 * time.Minute, time.Hour} {
		got := AdvisoryBudget(d)
		if got < prev {
			t.Errorf("budget fell from %s to %s as the build grew", prev, got)
		}
		prev = got
	}
}

// A bounded provider is a COPY. Mutating the shared one would tighten the
// generation calls too, which is the opposite of the intent — advisory limits
// must not leak onto the work that matters.
func TestBoundingDoesNotAlterTheOriginalProvider(t *testing.T) {
	p := &LocalCLIProvider{Bin: "/bin/true", Name: "fake", Stream: true,
		IdleTimeout: 5 * time.Minute, OnActivity: func(ProviderActivity) {}}

	bounded, ok := AdvisoryProvider(p, 20*time.Minute)
	if !ok {
		t.Fatal("a LocalCLIProvider could not be bounded")
	}
	if p.IdleTimeout != 5*time.Minute {
		t.Errorf("the original's idle allowance changed to %s", p.IdleTimeout)
	}
	if p.TotalCap != 0 {
		t.Errorf("the original gained a total cap of %s", p.TotalCap)
	}
	if p.OnActivity == nil {
		t.Error("the original lost its activity callback")
	}

	b := bounded.(*LocalCLIProvider)
	if b.TotalCap != 4*time.Minute {
		t.Errorf("bounded cap = %s, want 4m (a fifth of 20m)", b.TotalCap)
	}
	if b.IdleTimeout != advisoryIdle {
		t.Errorf("bounded idle = %s, want %s", b.IdleTimeout, advisoryIdle)
	}
}

// A provider that cannot be bounded must SAY so rather than be handed a limit
// it will ignore. "This took nine minutes" and "this took nine minutes and
// nothing was going to stop it" are different facts.
type unboundableProvider struct{}

func (unboundableProvider) Chat([]Message) (string, error) { return "", nil }

func TestAnUnboundableProviderIsReportedAsSuch(t *testing.T) {
	p, ok := AdvisoryProvider(unboundableProvider{}, time.Minute)
	if ok {
		t.Error("a provider with no WithBounds reported itself as bounded")
	}
	if p == nil {
		t.Error("the provider was dropped instead of returned unbounded")
	}
}

// An exhausted advisory budget reads as a normal outcome, and says what to do
// about it. A build that continues must not print something that looks fatal.
func TestAnExhaustedBudgetReadsAsAnOrdinaryOutcome(t *testing.T) {
	err := &advisoryExhausted{Budget: 4 * time.Minute, Provider: "claude code"}
	msg := err.Error()
	for _, want := range []string{"advisory", "skipped", "build is unaffected", "4m"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q is missing %q", msg, want)
		}
	}
}

// And it must not be retried. Retrying an advisory step that ran out of time
// spends the budget again to reach the same place — which is the shape of the
// original hour-long stall.
func TestAnExhaustedBudgetIsNotTransient(t *testing.T) {
	if isTransient(&advisoryExhausted{Budget: time.Minute, Provider: "fake"}) {
		t.Fatal("an exhausted advisory budget was classified transient")
	}
}

// WithBounds copies field by field, because the struct carries a mutex and
// `clone := *p` copies it — go vet refuses that, correctly.
//
// The cost of copying by hand is that a field added later is silently dropped
// from the clone: an advisory call would then run without the setting the
// generation calls have, and nothing would say so. So the check is DERIVED from
// the struct rather than a list typed here — a hand-written list tests the
// diligence of whoever last edited it, which is how nineteen area roots drifted
// past a guard that checked ten.
func TestBoundingCopiesEveryFieldItShould(t *testing.T) {
	// Fields WithBounds is expected to change, with why. Anything not named
	// here must survive the copy unchanged.
	intentional := map[string]string{
		"IdleTimeout": "tightened for advisory work",
		"TotalCap":    "the advisory budget",
		"OnActivity":  "deliberately dropped: an advisory step must not overwrite the state a person is watching",
	}
	// Fields that cannot be compared or copied.
	skip := map[string]string{
		"observed":   "set from a call's own output, not carried",
		"observedMu": "a mutex is never copied",
	}

	// Every settable, comparable field given a distinctive non-zero value, so a
	// dropped field shows up as a zero.
	// EVERY field non-zero, including the transport ones. This fixture used to
	// stop at the bounds and leave PromptFlag/PromptFileFlag/PromptStdin/
	// PromptArg/OutputFileFlag/NativeStream at their zero values — so when
	// WithBounds dropped them, the loop below compared zero against zero and
	// this test passed while the bug it exists to catch was live. A guard whose
	// fixture omits a field cannot guard that field.
	src := &LocalCLIProvider{
		Bin: "/some/bin", Name: "named", Args: []string{"--a", "--b"},
		Model: "some-model", JSON: true, Timeout: 7 * time.Minute,
		Dir: "/some/dir", Stream: true,
		IdleTimeout: 11 * time.Minute, TotalCap: 13 * time.Minute,
		PromptFlag: "--prompt", PromptFileFlag: "--prompt-file",
		PromptStdin: true, PromptArg: "-",
		OutputFileFlag: "--output-last-message", NativeStream: true,
		OnActivity: func(ProviderActivity) {},
	}

	got := src.WithBounds(time.Minute, 2*time.Minute).(*LocalCLIProvider)

	st := reflect.TypeOf(LocalCLIProvider{})
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if _, ok := skip[f.Name]; ok {
			continue
		}
		if _, ok := intentional[f.Name]; ok {
			continue
		}
		// .Elem() on the POINTER, not ValueOf(*src): dereferencing copies the
		// struct and therefore its mutex, which is the very thing go vet
		// refuses and the reason WithBounds copies field by field. This test
		// tripped that rule on its first attempt.
		a := reflect.ValueOf(src).Elem().Field(i)
		b := reflect.ValueOf(got).Elem().Field(i)
		if f.Type.Kind() == reflect.Func {
			// A func field not named in `intentional` must still be carried.
			if a.IsNil() != b.IsNil() {
				t.Errorf("field %s: nil-ness changed across WithBounds", f.Name)
			}
			continue
		}
		if !reflect.DeepEqual(a.Interface(), b.Interface()) {
			t.Errorf("field %s was not carried into the bounded copy: %v became %v.\n"+
				"  Either copy it in WithBounds, or name it in this test's `intentional` map with a reason.",
				f.Name, a.Interface(), b.Interface())
		}
	}

	// And the intentional changes actually happened.
	if got.IdleTimeout != time.Minute {
		t.Errorf("IdleTimeout = %s, want the bound", got.IdleTimeout)
	}
	if got.TotalCap != 2*time.Minute {
		t.Errorf("TotalCap = %s, want the bound", got.TotalCap)
	}
	if got.OnActivity != nil {
		t.Error("OnActivity survived; an advisory step would overwrite the watched state")
	}
}
