package dispatch

// Finishing a long build across a provider outage.
//
// # Why an outer retry, when there is already an inner one
//
// The inner retry handles a blip: seven attempts across about six minutes,
// which covers the overwhelming majority of "529 Overloaded … usually
// temporary". It cannot cover an outage that lasts longer than that, and
// waiting half an hour inside one file's generation would be
// indistinguishable from a hang.
//
// A tenant build takes forty minutes and five separate runs died to provider
// instability: twice on a stream abort, once at the reachability check, once at
// file 10 of 35, once at file 32 of 35. Every one of them left its completed
// files in the cache, so resuming cost only what remained — and every one of
// them still needed a person to notice and type the command again.
//
// So the outer loop resumes rather than restarts. It is not a second retry of
// the same call; it is the observation that a run which got to file 32 has
// banked 31 files, and the cheapest correct response is to run it again.
//
// # What it will not do
//
// It does not retry a failure that is not the provider's. A build that will not
// compile, a plan that will not validate, a refusal to overwrite an edited file
// — those recur identically and looping on them wastes the window that a real
// outage needs. And a session limit is never retried, because it is measured in
// hours: isTransient reports it permanent, and this asks isTransient.

import (
	"fmt"
	"time"
)

// superviseAttempts bounds how many times a run is resumed.
//
// Five, because the observed failures were single outages rather than a
// sustained one, and a bound that never gives up is a process nobody can
// reason about.
const superviseAttempts = 5

// superviseWait is the pause before resuming, which is deliberately longer than
// the inner window: if six minutes of retrying did not clear it, the outage is
// not measured in seconds.
func superviseWait(attempt int) time.Duration {
	waits := []time.Duration{
		60 * time.Second,
		3 * time.Minute,
		8 * time.Minute,
		15 * time.Minute,
	}
	if attempt >= 1 && attempt <= len(waits) {
		return waits[attempt-1]
	}
	return 15 * time.Minute
}

// Supervise runs an operation, resuming it while it fails for provider
// reasons and the attempts hold.
//
// resume is called with the attempt number so a caller can report it. The
// operation itself must be idempotent and cache-backed — which generation is:
// every completed file is keyed by the prompt that produced it, so a resumed
// run regenerates only what is missing.
func Supervise(op func(attempt int) error, notify func(attempt int, wait time.Duration, err error)) error {
	return supervise(op, notify, time.Sleep)
}

// supervise is Supervise with the wait injected, so a test can assert the
// policy without waiting for it. A test that sleeps for the real interval
// stops being run.
func supervise(op func(attempt int) error, notify func(attempt int, wait time.Duration, err error),
	sleep func(time.Duration)) error {
	var err error
	for attempt := 1; attempt <= superviseAttempts; attempt++ {
		err = op(attempt)
		if err == nil {
			return nil
		}
		if !isTransient(err) {
			// Not the provider's fault. Recurs identically, so return it rather
			// than spend an outage's worth of waiting on it.
			return err
		}
		if attempt == superviseAttempts {
			break
		}
		wait := superviseWait(attempt)
		if notify != nil {
			notify(attempt, wait, err)
		}
		sleep(wait)
	}
	return fmt.Errorf("gave up after %d attempts across provider failures: %w", superviseAttempts, err)
}
