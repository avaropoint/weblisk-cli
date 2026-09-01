package dispatch

// Retrying the failures that say they are temporary.
//
// Two runs died on this:
//
//	API Error: 529 Overloaded. This is a server-side issue, usually temporary —
//	try again in a moment.
//
// One at the provider reachability probe before any work began; one on file 49
// of 49, discarding the run. The error names itself temporary and asks to be
// retried, and nothing retried it.
//
// # What is retried, and what is not
//
// Only failures that are transient by definition: overload, rate limiting,
// gateway errors, and timeouts reaching the provider. A wrong API key, a
// missing binary, an exhausted session quota and a malformed response are not
// transient — retrying them burns the same call to reach the same answer, and
// worse, hides a fault that needs a person.
//
// A session limit is the important exclusion. "You've hit your session limit ·
// resets 2am" is a 429, and 429 is ordinarily retryable — but this one is not
// going to clear inside a backoff window, and retrying it turns a clear message
// into a long silence.

import (
	"fmt"
	"strings"
	"time"
)

// transientAttempts is the number of tries, not the number of retries.
//
// Three, with growing delays: an overload that has not cleared in half a minute
// is not the momentary spike this exists for, and a run that stalls silently is
// worse than one that fails with the provider's own words.
const transientAttempts = 3

// transientBackoff is the wait before attempt n (1-indexed).
func transientBackoff(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 2 * time.Second
	case 2:
		return 8 * time.Second
	default:
		return 20 * time.Second
	}
}

// isTransient reports whether a provider error is worth trying again.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())

	// A session or quota limit is not transient on any useful timescale, and it
	// arrives as a 429 — so this is checked FIRST, before the retryable codes.
	for _, permanent := range []string{
		"session limit", "quota", "insufficient_quota", "credit balance",
		"authentication", "invalid api key", "permission",
	} {
		if strings.Contains(s, permanent) {
			return false
		}
	}

	for _, retryable := range []string{
		"529", "overloaded", "rate limit", "429",
		"502", "503", "504", "bad gateway", "service unavailable",
		"connection reset", "connection refused", "eof",
		"timeout", "temporarily", "try again",
	} {
		if strings.Contains(s, retryable) {
			return true
		}
	}
	return false
}

// withTransientRetry calls a provider, retrying failures that say they are
// temporary.
//
// onRetry is told what happened, so a run that pauses says why rather than
// appearing to hang.
func withTransientRetry(onRetry func(attempt int, wait time.Duration, err error),
	call func() (string, error)) (string, error) {
	var out string
	var err error
	for attempt := 1; attempt <= transientAttempts; attempt++ {
		out, err = call()
		if err == nil || !isTransient(err) {
			return out, err
		}
		if attempt == transientAttempts {
			break
		}
		wait := transientBackoff(attempt)
		if onRetry != nil {
			onRetry(attempt, wait, err)
		}
		time.Sleep(wait)
	}
	return out, err
}

// retryingProvider adds transient retry to any provider.
//
// A decorator rather than eleven call sites: every path that talks to a model —
// planning, generation, all three repair kinds, verification, the reachability
// probe — gets the same behaviour, and a new path gets it without being told.
type retryingProvider struct {
	inner Provider
}

// WithTransientRetry wraps a provider so failures that say they are temporary
// are tried again.
func WithTransientRetry(p Provider) Provider {
	if p == nil {
		return nil
	}
	return &retryingProvider{inner: p}
}

func (r *retryingProvider) Chat(messages []Message) (string, error) {
	return withTransientRetry(func(attempt int, wait time.Duration, err error) {
		// Printed rather than silent: a run that pauses for twenty seconds
		// should say why, or it looks like a hang.
		fmt.Printf("    [retry %d/%d] provider reported a temporary failure; waiting %s\n      %s\n",
			attempt, transientAttempts-1, wait, firstErrorLine(err))
	}, func() (string, error) {
		return r.inner.Chat(messages)
	})
}

// firstErrorLine trims a provider error to something a person can read. These
// arrive as a full JSON result object.
func firstErrorLine(err error) string {
	s := err.Error()
	if i := strings.Index(s, `"result":"`); i >= 0 {
		rest := s[i+len(`"result":"`):]
		if j := strings.Index(rest, `"`); j > 0 {
			return rest[:j]
		}
	}
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

// withTransientRetryNoSleep is withTransientRetry without the waiting, so the
// retry policy can be tested without spending thirty seconds proving it.
func withTransientRetryNoSleep(call func() (string, error)) (string, error) {
	var out string
	var err error
	for attempt := 1; attempt <= transientAttempts; attempt++ {
		out, err = call()
		if err == nil || !isTransient(err) {
			return out, err
		}
	}
	return out, err
}
