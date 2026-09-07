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
		Bin:         p.Bin,
		Name:        p.Name,
		Args:        p.Args,
		Model:       p.Model,
		JSON:        p.JSON,
		Timeout:     p.Timeout,
		Dir:         p.Dir,
		Stream:      p.Stream,
		IdleTimeout: idle,
		TotalCap:    total,
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
		return p, false
	}
	return b.WithBounds(advisoryIdle, AdvisoryBudget(generationElapsed)), true
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
