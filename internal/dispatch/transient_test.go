package dispatch

// Retrying what says it is temporary — and only that.

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOverloadIsRetriedAndSessionLimitIsNot(t *testing.T) {
	// Both arrive from the same provider in the same shape. One clears in
	// seconds; the other clears at 2am. Retrying the second turns a clear
	// message into a long silence.
	transient := []string{
		`{"api_error_status":529,"result":"API Error: 529 Overloaded. This is a server-side issue, usually temporary — try again in a moment."}`,
		"API Error: 503 Service Unavailable",
		"read tcp: connection reset by peer",
		"context deadline exceeded: timeout",
	}
	for _, s := range transient {
		if !isTransient(errors.New(s)) {
			t.Errorf("should retry: %s", s[:min(70, len(s))])
		}
	}

	permanent := []string{
		`{"api_error_status":429,"result":"You've hit your session limit · resets 2am (America/Toronto)"}`,
		"authentication failed: invalid api key",
		"your credit balance is too low",
		"the response is not Go source — it has no package clause",
	}
	for _, s := range permanent {
		if isTransient(errors.New(s)) {
			t.Errorf("should NOT retry: %s", s[:min(70, len(s))])
		}
	}
}

func TestASessionLimitIsNotRescuedByItsStatusCode(t *testing.T) {
	// A session limit arrives as a 429, and 429 is ordinarily retryable. The
	// permanent check must run first or the exclusion never fires.
	err := errors.New(`{"api_error_status":429,"result":"You've hit your session limit · resets 2am"}`)
	if isTransient(err) {
		t.Error("a session limit was treated as a rate limit and would be retried into silence")
	}
}

func TestARetriedCallEventuallySucceeds(t *testing.T) {
	calls := 0
	out, err := withTransientRetryNoSleep(func() (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("API Error: 529 Overloaded")
		}
		return "package main\n", nil
	})
	if err != nil {
		t.Fatalf("a transient failure was not retried through: %v", err)
	}
	if out != "package main\n" || calls != 3 {
		t.Errorf("out=%q calls=%d", out, calls)
	}
}

func TestAPermanentFailureIsNotRetried(t *testing.T) {
	calls := 0
	_, err := withTransientRetryNoSleep(func() (string, error) {
		calls++
		return "", errors.New("authentication failed")
	})
	if err == nil {
		t.Fatal("expected the error to surface")
	}
	if calls != 1 {
		t.Errorf("called %d times; a permanent failure must be reported at once", calls)
	}
}

func TestTheProviderErrorIsMadeReadable(t *testing.T) {
	// These arrive as a full JSON result object; a run that pauses should say
	// why in one line.
	err := errors.New(`{"is_error":true,"usage":{"input_tokens":0},"result":"API Error: 529 Overloaded. This is a server-side issue.","type":"result"}`)
	got := firstErrorLine(err)
	if !strings.HasPrefix(got, "API Error: 529 Overloaded") {
		t.Errorf("firstErrorLine = %q", got)
	}
	if strings.Contains(got, "input_tokens") {
		t.Error("the JSON envelope leaked into the message")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// A provider-side stream abort is transient. It killed two forty-minute builds.
func TestAStreamAbortIsRetried(t *testing.T) {
	abort := errors.New(`claude code failed: {"is_error":true,"stop_reason":null,` +
		`"terminal_reason":"aborted_streaming","subtype":"error_during_execution",` +
		`"errors":["[ede_diagnostic] result_type=user last_content_type=n/a stop_reason=null"]}`)
	if !isTransient(abort) {
		t.Error("a stream abort was treated as permanent; a 40-minute build dies on it")
	}
}

// And a session limit inside the same envelope is still NOT transient — it
// arrives as a 429 and retrying it wastes the remaining window.
func TestASessionLimitInsideAStreamEnvelopeIsStillPermanent(t *testing.T) {
	limit := errors.New(`claude code failed: {"is_error":true,"api_error_status":429,` +
		`"subtype":"success","result":"You've hit your session limit · resets 4:30am"}`)
	if isTransient(limit) {
		t.Error("a session limit was treated as transient")
	}
}

// The retry window must be sized for an unattended build.
//
// It was ten seconds across three attempts, so a provider outage of half a
// minute discarded a forty-minute run — after correctly identifying the error
// as temporary.
func TestTheRetryWindowSurvivesAnOrdinaryOutage(t *testing.T) {
	if got := TransientWindow(); got < 4*time.Minute {
		t.Errorf("the retry window is %v; a build that has run for thirty minutes should wait longer than that", got)
	}
	if got := TransientWindow(); got > 15*time.Minute {
		t.Errorf("the retry window is %v — past a point patience is indistinguishable from a hang", got)
	}
	// Backoff must grow, and must be capped.
	prev := time.Duration(0)
	for i := 1; i <= transientAttempts; i++ {
		w := transientBackoff(i)
		if w < prev {
			t.Errorf("backoff fell at attempt %d: %v after %v", i, w, prev)
		}
		if w > 5*time.Minute {
			t.Errorf("backoff at attempt %d is %v — uncapped", i, w)
		}
		prev = w
	}
}

// A permanent condition must still fail fast: the long window is for outages,
// not for quota errors.
func TestAPermanentErrorDoesNotWaitForTheWholeWindow(t *testing.T) {
	calls := 0
	start := time.Now()
	_, err := withTransientRetryNoSleep(func() (string, error) {
		calls++
		return "", errors.New(`{"api_error_status":429,"result":"You've hit your session limit · resets 4:30am"}`)
	})
	if err == nil {
		t.Fatal("a session limit was reported as success")
	}
	if calls != 1 {
		t.Errorf("a session limit was retried %d times; it is permanent on any useful timescale", calls)
	}
	if time.Since(start) > time.Second {
		t.Error("a permanent error waited")
	}
}

// A long build must survive an outage that outlasts the inner retry window.
//
// Five runs died to provider instability, each having banked its completed
// files in the cache, and each still needing a person to notice and type the
// command again.
func TestSupervisorResumesAcrossAProviderOutage(t *testing.T) {
	calls := 0
	err := supervise(func(attempt int) error {
		calls++
		if calls < 3 {
			return errors.New("API Error: 529 Overloaded. This is a server-side issue, usually temporary")
		}
		return nil
	}, func(int, time.Duration, error) {}, func(time.Duration) {})
	if err != nil {
		t.Fatalf("a run that would have succeeded on the third attempt failed: %v", err)
	}
	if calls != 3 {
		t.Errorf("ran %d times, want 3", calls)
	}
}

// It must NOT loop on a failure that is not the provider's — those recur
// identically and looping wastes the window a real outage needs.
func TestSupervisorDoesNotRetryARealFailure(t *testing.T) {
	for _, e := range []string{
		"build failed: go build -o bin/orchestrator ./cmd/orchestrator",
		"no valid plan after 3 attempts",
		"6 file(s) cannot be safely regenerated",
		`{"api_error_status":429,"result":"You've hit your session limit · resets 4:30am"}`,
	} {
		calls := 0
		_ = supervise(func(int) error { calls++; return errors.New(e) }, nil, func(time.Duration) {})
		if calls != 1 {
			t.Errorf("%q was retried %d times; it recurs identically", e[:34], calls)
		}
	}
}

// The outer wait must exceed the inner window: if six minutes of retrying did
// not clear it, the outage is not measured in seconds.
func TestTheSupervisorWaitsLongerThanTheInnerRetry(t *testing.T) {
	if superviseWait(1) < 30*time.Second {
		t.Errorf("the first resume waits %v — too soon to be a different answer", superviseWait(1))
	}
	if superviseWait(4) < TransientWindow() {
		t.Errorf("a later resume waits %v, less than the inner window %v",
			superviseWait(4), TransientWindow())
	}
}
