package dispatch

// observe.go — build state a front end can reason about.
//
// Studio's build console could only ever show a step name and a spinner, and it
// showed exactly that for sixty-five minutes while a provider went nowhere. The
// information existed: generation knew it was on file 7 of 29, the provider's
// stream knew it had been silent for four minutes, and the provider itself had
// reported 92% of a five-hour quota. All of it went to stdout as prose, where
// `readNDJSON` receives each line as `{message: "..."}` — visible, and not
// answerable. "Is this alive" is not a question you can ask a log line.
//
// So the state is emitted as STRUCTURE, alongside the prose rather than instead
// of it: a person reading a terminal wants the sentences, and a console wants
// fields it can compare against the last ones it saw.
//
// # What makes this state and not logging
//
// Every field here answers a question a caller has to act on:
//
//	File/Index/Total   how far — and whether it moved since last time
//	Status/Attempt     what is happening to this file
//	IdleSeconds        whether the provider is alive. THE field: elapsed time
//	                   says nothing, and it was what we used
//	Events/Bytes       what liveness is being measured from
//	Quota              whether this run can finish at all, said before it cannot
//
// A console that has these can stop guessing. It can show "file 7 of 29, model
// quiet for 40s" and, at four minutes, say so rather than spin.

import (
	"fmt"
	"time"
)

// BuildState is a snapshot of a build in flight.
//
// Durations are seconds as numbers, not Go duration strings: a front end
// compares and formats them, and "1m30s" has to be parsed before either.
type BuildState struct {
	// File is the artifact being generated, when one is.
	File  string `json:"file,omitempty"`
	Index int    `json:"index,omitempty"`
	Total int    `json:"total,omitempty"`
	// Status mirrors Progress.Status: generating, retrying, written, reused,
	// failed, planning, building.
	Status  string `json:"status,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
	Detail  string `json:"detail,omitempty"`

	// IdleSeconds is how long the provider has produced nothing.
	//
	// The one number that decides whether a build is in trouble. Reported even
	// when zero, because "0 seconds idle" and "not measured" are different
	// facts and omitempty would collapse them — so it is a pointer.
	IdleSeconds *float64 `json:"idle_seconds,omitempty"`
	// ElapsedSeconds is total wall time on the current provider call. Reported
	// because a person wants it; never a reason to abort anything.
	ElapsedSeconds *float64 `json:"elapsed_seconds,omitempty"`
	Events         int      `json:"events,omitempty"`
	Bytes          int64    `json:"bytes,omitempty"`
	// Phase is the provider's last stream event type.
	Phase string `json:"phase,omitempty"`
	// Quota is the provider's own account of its limits, in words.
	Quota string `json:"quota,omitempty"`
}

// BuildObserver receives build state as it changes.
//
// Two callbacks rather than one, because they fire at unrelated rates: file
// progress changes tens of times across a build, provider activity changes
// every couple of seconds within a single file.
type BuildObserver struct {
	// File is called on each generation progress event.
	File func(BuildState)
	// Activity is called as the provider's stream moves.
	Activity func(BuildState)
}

// stateFromProgress converts generation progress into build state.
func stateFromProgress(p Progress) BuildState {
	return BuildState{
		File: p.Path, Index: p.Step, Total: p.Total,
		Status: p.Status, Attempt: p.Attempt, Detail: p.Detail,
	}
}

// stateFromActivity converts provider activity into build state.
func stateFromActivity(a ProviderActivity) BuildState {
	idle := a.IdleFor.Seconds()
	elapsed := a.Elapsed.Seconds()
	return BuildState{
		Status: "waiting", IdleSeconds: &idle, ElapsedSeconds: &elapsed,
		Events: a.Events, Bytes: a.Bytes, Phase: a.Phase,
		Quota: QuotaNote(),
	}
}

// Describe renders build state as one line, for a terminal or a log.
//
// Kept next to the struct so the two cannot disagree about what a field means.
func (s BuildState) Describe() string {
	if s.IdleSeconds != nil {
		out := fmt.Sprintf("model working — %s elapsed, quiet for %s, %s",
			round(*s.ElapsedSeconds), round(*s.IdleSeconds), plural(s.Events, "event"))
		if s.Quota != "" {
			out += " · " + s.Quota
		}
		return out
	}
	if s.Total > 0 {
		return fmt.Sprintf("[%d/%d] %s %s", s.Index, s.Total, s.File, s.Status)
	}
	if s.Detail != "" {
		return s.Status + ": " + s.Detail
	}
	return s.Status
}

func round(seconds float64) string {
	return (time.Duration(seconds) * time.Second).Round(time.Second).String()
}

// activeObserver is the observer for the build in progress.
//
// Package state because generation reaches the provider through several layers
// that were never given a place to carry one, and threading a parameter through
// all of them to reach one call site would be a larger change than the thing it
// enables. Set and cleared around a build; one build runs at a time in a
// process, which SupervisedComponentInit already assumes.
var activeObserver *BuildObserver

// SetBuildObserver installs the observer for the current build, and returns a
// function that removes it.
func SetBuildObserver(obs *BuildObserver) func() {
	activeObserver = obs
	return func() { activeObserver = nil }
}

// ObserveBuild installs an observer AND names the directory whose state is
// persisted, so `weblisk build status` can answer for this build later.
func ObserveBuild(root string, obs *BuildObserver) func() {
	activeObserver = obs
	observedRoot = root
	return func() {
		activeObserver = nil
		observedRoot = ""
	}
}

// observedRoot is the directory whose build state is being recorded.
//
// Set with the observer. Without it there is nowhere to persist to: the
// observer is installed by pkg/tenant, which knows the root, while
// printProgress is called from deep inside generation, which does not.
var observedRoot string

// observeActivity is the provider callback, wired where a provider is built.
func observeActivity(a ProviderActivity) {
	st := stateFromActivity(a)
	if activeObserver != nil && activeObserver.Activity != nil {
		activeObserver.Activity(st)
	}
	// Throttled: liveness samples arrive every couple of seconds for the length
	// of a build, and this file is read only when somebody asks.
	if observedRoot != "" {
		recordBuildState(observedRoot, st, "", "", false)
	}
}

// observeProgress is called alongside printProgress.
func observeProgress(p Progress) {
	st := stateFromProgress(p)
	if activeObserver != nil && activeObserver.File != nil {
		activeObserver.File(st)
	}
	// Always written. A position change is a durable fact and there are only
	// tens of them, so throttling one away would lose the answer to "how far
	// did it get" for a build that then died.
	if observedRoot != "" {
		recordBuildState(observedRoot, st, "", "", true)
	}
}
