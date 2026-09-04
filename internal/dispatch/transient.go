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
// transientAttempts and the backoff below are sized for an UNATTENDED BUILD,
// not for an interactive command.
//
// They were three attempts with 2s and 8s of backoff — a total window of ten
// seconds. A tenant build takes forty minutes, and a provider outage of half a
// minute therefore threw away everything done so far: one run died at the
// reachability check on "529 Overloaded. This is a server-side issue, usually
// temporary — try again in a moment", having correctly identified the error as
// temporary and then given up before the moment passed.
//
// A run that has already spent thirty minutes should obviously wait five for a
// provider to come back. The window is now ~5.7 minutes across seven attempts,
// which survives an ordinary outage and still fails in bounded time.
//
// This does NOT make a session limit slow to report: isTransient checks the
// permanent conditions FIRST, so a quota error still returns immediately.
const transientAttempts = 7

// transientBackoff is the wait before attempt n (1-indexed).
//
// Doubling, capped. Uncapped doubling reaches half an hour by attempt ten,
// which stops being patience and becomes a hang.
func transientBackoff(attempt int) time.Duration {
	waits := []time.Duration{
		2 * time.Second,
		8 * time.Second,
		20 * time.Second,
		45 * time.Second,
		90 * time.Second,
		180 * time.Second,
	}
	if attempt >= 1 && attempt <= len(waits) {
		return waits[attempt-1]
	}
	return 180 * time.Second
}

// TransientWindow is the total time retries may span, for reporting.
func TransientWindow() time.Duration {
	var total time.Duration
	for i := 1; i < transientAttempts; i++ {
		total += transientBackoff(i)
	}
	return total
}

// isTransient reports whether a provider error is worth trying again.
//
// Structure first. A provider that told us its HTTP status has already answered
// this question, and reading its status is not the same kind of act as reading
// its prose. Only when nothing structural is available does this fall back to
// matching words — and then against the human message alone.
//
// The order matters and is the whole fix: this used to match the permanent
// words against the error's full text, and every Claude Code envelope contains
// "permission_denials", so every transient failure was classified permanent and
// nothing was ever retried. See fault.go.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	if f := FaultOf(err); f != nil {
		switch f.Class() {
		case FaultTransient:
			return true
		case FaultPermanent:
			return false
		}
		// The provider gave us a fault but not enough of one. Its message is
		// still better evidence than the envelope around it.
		return transientMessage(f.Message) && !permanentMessage(f.Message)
	}
	// A provider that reports failures as bare sentences — or a Go error from
	// the transport. permanentMessage is asked first: a session limit arrives
	// as a 429 and 429 is otherwise the most retryable thing there is.
	s := err.Error()
	if permanentMessage(s) {
		return false
	}
	return transientMessage(s)
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

// Unwrap returns the provider beneath the retry, so a caller that needs to know
// WHICH provider was selected can ask without the wrapper hiding it.
//
// Named Unwrap so errors.As-style introspection and type assertions have one
// obvious way through, rather than each caller reaching for newRawProvider and
// re-deciding the selection.
func (r *retryingProvider) Unwrap() Provider { return r.inner }

// Underlying returns the provider beneath any number of decorators.
func Underlying(p Provider) Provider {
	for {
		u, ok := p.(interface{ Unwrap() Provider })
		if !ok {
			return p
		}
		p = u.Unwrap()
	}
}
