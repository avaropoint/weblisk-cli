package dispatch

// advisory.go — a step whose failure is a warning must not cost more than the
// work it comments on.
//
// `SelfVerify` asks the model whether the implementation satisfies the
// blueprint's assertions. Its failure is a `[warn]` and the build continues, so
// it is advisory by design. It was not advisory in cost: one run spent
// sixty-five minutes there — six ten-minute attempts plus backoff — on a step
// whose entire output is a paragraph the build does not depend on.
//
// Two things made that possible and both are fixed elsewhere: a total deadline
// standing in for liveness (localcli_stream.go) and a local abort classified as
// a provider outage (transient.go). What is left is the principle: advisory work
// gets an advisory budget, and running out of it is a normal outcome to report,
// not a failure to retry.
//
// # Why the budget is derived and not a constant
//
// The step reads every generated file and every assertion, so what it costs
// scales with what was built. A constant would be too small for a large tenant
// and far too large for a small one. Generation's own elapsed time is the best
// available measure of size — it is the work being commented on — so the
// budget is a fraction of it, floored so a fast build still gets a real
// attempt and capped so a slow one cannot be doubled by its own commentary.

import (
	"fmt"
	"time"
)

const (
	// advisoryShare is the fraction of generation time an advisory step may use.
	advisoryShare = 5
	// advisoryFloor gives a small build a real attempt.
	advisoryFloor = 90 * time.Second
	// advisoryCeiling stops a long build's commentary from becoming its own
	// second build.
	advisoryCeiling = 6 * time.Minute
	// advisoryIdle is how long an advisory call may be silent. Shorter than a
	// generation call's allowance: for work nobody depends on, giving up early
	// is the right instinct.
	advisoryIdle = 60 * time.Second
)

// AdvisoryBudget is how long an advisory step may take, given how long the work
// it comments on took.
func AdvisoryBudget(generationElapsed time.Duration) time.Duration {
	budget := generationElapsed / advisoryShare
	if budget < advisoryFloor {
		return advisoryFloor
	}
	if budget > advisoryCeiling {
		return advisoryCeiling
	}
	return budget
}

// boundable is a provider that can produce a copy of itself with tighter limits.
//
// An optional interface rather than a field on Provider: a provider that cannot
// enforce a bound must not be handed one and left to ignore it. Asking, and
// falling back to the unbounded provider with that said out loud, is honest;
// wrapping every provider in a timeout goroutine would report a bound that
// leaks the call it claims to have stopped.
type boundable interface {
	WithBounds(idle, total time.Duration) Provider
}

// WithBounds returns a copy of this provider with tighter limits.
//
// Field by field, NOT `clone := *p`. The struct carries a sync.Mutex guarding
// the observed model, and copying a struct copies the mutex — `go vet` refuses
// it, and it is refused for a reason: the copy's mutex starts life holding
// whatever state the original's was in.
func (p *LocalCLIProvider) WithBounds(idle, total time.Duration) Provider {
	return &LocalCLIProvider{
		Bin:   p.Bin,
		Name:  p.Name,
		Args:  p.Args,
		Model: p.Model,
		JSON:  p.JSON,
		// Timeout is CLAMPED, not carried over.
		//
		// IdleTimeout and TotalCap are read only by runStreaming. codex and
		// local-cli do not stream, so a copy that set only those two reported a
		// bound and then ran to the ten-minute default — the same field-vs-path
		// mismatch discover.go's readiness clamp exists for, and its comment
		// records: "the other two are read ONLY by runStreaming, so setting them
		// alone left codex bounded by the 10-minute default."
		Timeout:     tighter(p.Timeout, total),
		Dir:         p.Dir,
		Stream:      p.Stream,
		IdleTimeout: idle,
		TotalCap:    total,
		// How the prompt gets IN and how the answer comes OUT are not bounds,
		// and dropping them silently produced a copy that talked to the tool
		// wrongly rather than one that talked to it briefly: a codex clone
		// without OutputFileFlag reads its own banner back as the answer, and
		// one without PromptStdin puts a whole prompt on argv. They were
		// missing, and the guard test below could not see it because its
		// fixture left them at their zero values.
		PromptFlag:     p.PromptFlag,
		PromptFileFlag: p.PromptFileFlag,
		PromptStdin:    p.PromptStdin,
		PromptArg:      p.PromptArg,
		OutputFileFlag: p.OutputFileFlag,
		NativeStream:   p.NativeStream,
		// No activity callback: a console showing per-event progress for a step
		// nobody waits on is noise, and this step's own state would overwrite
		// the generation state a person is actually watching.
		OnActivity: nil,
	}
}

// AdvisoryProvider returns p bounded for advisory work, and whether it could be.
//
// The second return exists so a caller can SAY that the step is unbounded when
// it is. "This took nine minutes" and "this took nine minutes and nothing was
// going to stop it" are different facts about the same run.
func AdvisoryProvider(p Provider, generationElapsed time.Duration) (Provider, bool) {
	b, ok := p.(boundable)
	if !ok {
		// Not reachable for a local CLI any more — retryingProvider forwards
		// WithBounds — but a decorator added later that does not forward will
		// land here, and falling back unbounded WITH THAT SAID is the
		// documented behaviour rather than a silent miss.
		return p, false
	}
	bounded := b.WithBounds(advisoryIdle, AdvisoryBudget(generationElapsed))
	// A forwarder that cannot bound its inner provider returns itself. Taking
	// that as success would report a bound nothing enforces, which is the exact
	// thing the second return value exists to prevent.
	if bounded == p {
		return p, false
	}
	return bounded, true
}

// advisoryExhausted describes a budget that ran out.
type advisoryExhausted struct {
	Budget   time.Duration
	Provider string
}

func (e *advisoryExhausted) Error() string {
	return fmt.Sprintf("%s did not answer within the %s allowed for an advisory step — "+
		"skipped, and the build is unaffected. This step reads every generated file at once, "+
		"so it is the first thing to outgrow a budget on a large tenant",
		e.Provider, e.Budget.Round(time.Second))
}

// tighter is the smaller of a provider's existing limit and a new bound.
//
// A bound must never loosen what was already there, and an unset limit is not a
// small one — it means the default applies, which is larger than any budget an
// advisory step derives.
func tighter(existing, bound time.Duration) time.Duration {
	if bound <= 0 {
		return existing
	}
	if existing <= 0 || existing > bound {
		return bound
	}
	return existing
}
