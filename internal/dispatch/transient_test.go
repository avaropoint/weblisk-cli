package dispatch

// Retrying what says it is temporary — and only that.

import (
	"errors"
	"strings"
	"testing"
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
