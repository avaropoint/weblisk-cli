package dispatch

// The type-based transient rules must survive being wrapped.
//
// isTransient decides by TYPE for the two failures that are OURS rather than
// the provider's — an idle abort and an exhausted advisory budget. Both are
// deterministic: a retry reproduces them exactly, which is why the type switch
// exists at all. Its own comment records the cost of getting this wrong: "six
// retries of a ten-minute deadline, sixty minutes spent reaching the answer the
// first attempt already had."

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A deterministic local abort stays non-transient after a caller wraps it.
//
// generate.go wraps every generation failure as `generating %s: %w`, and
// Supervise then asks isTransient about THAT error. A bare `switch err.(type)`
// does not see through a wrap, so the rule that exists to stop a
// self-reproducing failure from being retried was defeated by the one caller
// that matters.
func TestTypeBasedTransientRulesSurviveWrapping(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"idle abort", &idleAbort{Provider: "claude-code", IdleFor: 3 * time.Minute}},
		{"advisory exhausted", &advisoryExhausted{Provider: "claude-code", Budget: 10 * time.Minute}},
	} {
		if isTransient(tc.err) {
			t.Errorf("%s is transient even unwrapped — the type rule is not firing at all", tc.name)
		}
		wrapped := fmt.Errorf("generating internal/agent/handlers.go: %w", tc.err)
		if isTransient(wrapped) {
			t.Errorf("%s becomes transient once wrapped — Supervise would resume the whole "+
				"component five times over a failure that reproduces exactly", tc.name)
		}
		// And through two layers, which is what Supervise's own wrap produces.
		twice := fmt.Errorf("gave up: %w", wrapped)
		if isTransient(twice) {
			t.Errorf("%s becomes transient through two wraps", tc.name)
		}
	}
}

// A non-streaming deadline is ours, so it is not retried.
//
// It returned ProviderFault{Status: 408}, and 408 is transient — so
// withTransientRetry spent seven attempts at ten minutes each, and Supervise
// then resumed the whole component five more times with twenty-seven minutes of
// waiting. Hours, on one call that was never going to finish, reaching the same
// deadline every time.
//
// The streaming path was fixed for exactly this three days after the 408
// rationale was written; the non-streaming path kept the sentence the fix
// refuted.
func TestANonStreamingDeadlineIsNotRetried(t *testing.T) {
	err := &deadlineAbort{Provider: "codex", After: 10 * time.Minute}
	if isTransient(err) {
		t.Error("a deadline we imposed is retried into itself")
	}
	if isTransient(fmt.Errorf("generating cmd/x/main.go: %w", err)) {
		t.Error("wrapped, a deadline we imposed becomes transient again")
	}
	// The TYPE must decide, against a message that says otherwise.
	//
	// The message names WL_AI_TIMEOUT, because that is the one thing an operator
	// can act on — and "WL_AI_TIMEOUT" lowercases to contain "timeout", which is
	// in transientMessage's list. So the fallback word-matching, reached for any
	// error the type rules miss, would call this retryable.
	//
	// That is not a flaw to reword around; it is the reason the type rule
	// exists. advisoryExhausted's comment records the alternative: it was
	// classified correctly only because its sentence happened to contain no
	// transient phrase, and "an accident is not a rule".
	if !transientMessage(err.Error()) {
		t.Skip("the message no longer contains a transient phrase; this test no longer " +
			"proves the type rule beats word matching")
	}
	if isTransient(err) {
		t.Error("word matching overrode the type rule")
	}
	if !strings.Contains(err.Error(), "WL_AI_TIMEOUT") {
		t.Error("the message no longer names the knob that raises the limit")
	}
}

// Being slower than a limit we picked does not disqualify a provider.
//
// notReady names each of our own abort types explicitly, and says why: "a
// readiness probe that hits its own cap ... says nothing about whether the
// backend can generate; disqualifying a healthy provider for being slower than
// a number we picked is the opposite of what this walk is for."
//
// deadlineAbort had to be added there. It replaced a 408 ProviderFault, which
// notReady saw as FaultTransient and kept — so making the type non-transient,
// and stopping there, would have started disqualifying every non-streaming
// provider that ran past the 90-second readiness cap. The walk would then
// report codex as unable to generate because it was slow once.
func TestASlowProviderIsBusyNotBroken(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"deadline abort", &deadlineAbort{Provider: "codex", After: 90 * time.Second}},
		{"idle abort", &idleAbort{Provider: "claude-code", IdleFor: 3 * time.Minute}},
		{"advisory exhausted", &advisoryExhausted{Provider: "codex", Budget: 10 * time.Minute}},
	} {
		if notReady(tc.err) {
			t.Errorf("%s disqualified a provider from the readiness walk", tc.name)
		}
		if notReady(fmt.Errorf("probing: %w", tc.err)) {
			t.Errorf("%s disqualified a provider once wrapped", tc.name)
		}
	}
	// A provider that genuinely cannot generate is still disqualified.
	if !notReady(&ProviderFault{Provider: "grok", Status: 402, Message: "balance exhausted"}) {
		t.Error("a provider that cannot generate was kept in the walk")
	}
}
