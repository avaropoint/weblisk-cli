package dispatch

// localcli_stream.go — watching a provider work, instead of timing it.
//
// # Why this replaces a deadline
//
// The old call buffered the subprocess into a strings.Builder and gave it ten
// minutes. Nothing observed the process in between, so "wedged" and "working
// hard" were indistinguishable and a wall clock stood in for the difference.
//
// That is not a conservative choice, it is an unsound one, and one run showed
// exactly how: generation legitimately exceeded ten minutes, the deadline
// killed it, the abort was reported as HTTP 408, 408 was classified transient,
// and the retry ran the same call into the same deadline six more times. Sixty
// minutes of attempts plus six minutes of backoff, to reach the answer the
// first attempt had already reached. The retry could never help — a fixed
// per-attempt deadline is deterministic, so if the work needs more than ten
// minutes then every attempt needs more than ten minutes.
//
// The fix is not a bigger number. `claude --output-format stream-json` emits
// events as it works, so liveness becomes something we OBSERVE:
//
//	system/init        the process is up and talking
//	rate_limit_event   the provider's own quota state, with resetsAt
//	assistant          content, as it is produced
//	result             the terminal envelope: is_error, terminal_reason
//
// So the control is an IDLE deadline: abort when nothing has arrived for a
// while, at any total elapsed time. A call producing output for forty minutes
// is healthy. A call silent for three is not, and no amount of total budget
// makes it healthy. Total elapsed time was never the signal.
//
// # The absolute cap is a backstop, not the policy
//
// A generous absolute cap remains, because a provider that emits a token every
// minute forever is a hang that idle-detection cannot see. It is deliberately
// far above any real build so that it never decides an ordinary outcome — if it
// ever fires, that is a fault to investigate and not a budget to raise.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"

	"github.com/avaropoint/weblisk-cli/internal/platform"
	"strings"
	"sync"
	"time"
)

// defaultIdleTimeout is how long a provider may be silent before it is abandoned.
//
// Sized against what the stream actually does: an init event arrives in under a
// second, and thereafter content or a tool event arrives continuously while the
// model works. Three minutes of complete silence is not slow work, it is a
// stalled pipe — and unlike a total deadline, waiting longer here costs nothing
// on a healthy call because a healthy call never approaches it.
const defaultIdleTimeout = 3 * time.Minute

// firstEventGrace is the allowance BEFORE the first event arrives.
//
// A different question, so a different allowance — not a tuned one.
//
// Once a stream has started, silence is anomalous: measured on a real build,
// the provider emits `system/thinking_tokens` events continuously while it
// thinks, so a model that is working is never quiet for long. Before the first
// event there is no stream to be stalled, and "thinking about a large prompt"
// and "hung" are genuinely indistinguishable — so nothing can be concluded and
// the allowance has to be generous.
//
// Measured: the gap to the first event on a full orchestrator plan was ~80
// seconds. This is not that number with margin added; it is the window in which
// no judgement is possible, and it is deliberately far past any observed gap
// because being wrong here kills work that was fine.
const firstEventGrace = 8 * time.Minute

// absoluteCap is the backstop described above. Not a budget: a fault marker.
const absoluteCap = 4 * time.Hour

// pollFor is how often idleness is checked, derived from the allowance rather
// than fixed.
//
// A fixed 2s interval meant an idle allowance shorter than 2s was never checked
// at all — and it meant OnActivity never fired on a call that finished in under
// two seconds, so nothing was reported for exactly the calls a person is most
// likely to be watching. A quarter of the allowance gives four observations
// before a decision, clamped so it neither spins nor sleeps through a decision.
func pollFor(idle time.Duration) time.Duration {
	step := idle / 4
	if step < 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	if step > 2*time.Second {
		return 2 * time.Second
	}
	return step
}

// maxStreamLine bounds one event. The init event alone is several kilobytes and
// a result envelope carries full usage accounting, so the scanner's 64KB
// default is too small — a long line would otherwise read as a truncated
// stream, which looks like a protocol fault and is a buffer size.
const maxStreamLine = 4 << 20

// RateLimitInfo is the provider's own account of its quota.
//
// Taken from the stream rather than inferred from a 429. When the provider says
// `resetsAt`, that is the answer to "how long should we wait" — a backoff table
// is a guess at the same number, and it is a guess we make while holding the
// real one.
type RateLimitInfo struct {
	Status      string  `json:"status"`
	ResetsAt    int64   `json:"resetsAt"`
	Type        string  `json:"rateLimitType"`
	Utilization float64 `json:"utilization"`
	Overage     bool    `json:"isUsingOverage"`
}

// ProviderActivity is what a caller can know about a call in flight.
//
// This is the instrumentation a console needs. "Generating file 12 of 29" says
// which file; it does not say whether anything is happening. Studio showed a
// stage card and a spinner for sixty-five minutes and had nothing more to
// report, because nothing more was known.
type ProviderActivity struct {
	// Events is how many stream events have arrived.
	Events int `json:"events"`
	// Bytes is how much has arrived, which moves even mid-message.
	Bytes int64 `json:"bytes"`
	// LastEventAt is when the stream last said anything.
	LastEventAt time.Time `json:"last_event_at"`
	// IdleFor is how long it has been silent. This, not Elapsed, is the
	// number that decides whether a call is in trouble.
	IdleFor time.Duration `json:"idle_for"`
	// Elapsed is total wall time, reported because a person wants it — never
	// used to abort.
	Elapsed time.Duration `json:"elapsed"`
	// Phase is the last event type seen, so a caller can say what it is doing.
	Phase string `json:"phase"`
	// RateLimit is the provider's quota state, when it has told us.
	RateLimit *RateLimitInfo `json:"rate_limit,omitempty"`
}

// streamEvent is the subset of a stream-json line this needs.
type streamEvent struct {
	Type          string         `json:"type"`
	Subtype       string         `json:"subtype"`
	RateLimitInfo *RateLimitInfo `json:"rate_limit_info"`
	// Message.Model is which model produced an assistant turn.
	//
	// This is the authoritative answer to "what wrote this", and it is why the
	// model is read from the STREAM and not from the terminal envelope's
	// modelUsage accounting. modelUsage is keyed by every model the call
	// touched, including a small helper the tool uses for its own bookkeeping —
	// and picking the one with the most output tokens chose the helper: a call
	// that ran on claude-opus-5 was recorded as claude-haiku-4-5, because the
	// helper emitted 12 tokens and the answer was 4.
	//
	// A build that records the wrong model cannot answer the question the whole
	// chain exists to answer, and it answers it confidently.
	Message struct {
		Model string `json:"model"`
	} `json:"message"`
}

// streamResult is one completed streaming call.
type streamResult struct {
	// Final is the terminal `result` line, verbatim, for faultFromClaudeCode.
	Final string
	// Text is the result text, when the terminal line carried one.
	Text     string
	Activity ProviderActivity
	// Model is which model produced the assistant turn, as it said.
	Model     string
	RateLimit *RateLimitInfo
}

// idleAbort reports that a call was abandoned for silence.
//
// Its own type because the retry policy must be able to tell OUR decision from
// the provider's condition. An idle abort is not transient: nothing about the
// provider changed, we stopped waiting — and retrying reproduces it exactly.
type idleAbort struct {
	IdleFor  time.Duration
	Elapsed  time.Duration
	Events   int
	Provider string
}

func (e *idleAbort) Error() string {
	return fmt.Sprintf("%s produced nothing for %s (after %s and %d event(s)) — abandoned as stalled. "+
		"This is not a provider outage and retrying it would stall the same way; "+
		"raise WL_AI_IDLE_TIMEOUT only if this provider is known to go quiet mid-call",
		e.Provider, e.IdleFor.Round(time.Second), e.Elapsed.Round(time.Second), e.Events)
}

// allowanceFor decides how long silence may last.
//
// Extracted as a pure function because the interesting case cannot be tested
// through a subprocess: detecting a wrongly-clamped explicit allowance would
// need a fake provider that stays silent for longer than firstEventGrace, and
// an eight-minute test is a test nobody runs. A mutation that removed the clamp
// survived the subprocess tests for exactly that reason.
//
// Before the first event: the generous grace, because nothing can be concluded
// — there is no stream to be stalled, and "thinking about a large prompt" and
// "hung" look identical. After it: the tight allowance, because a provider that
// has started emitting emits continuously, thinking included.
//
// An explicitly configured allowance longer than the grace WINS in both phases.
// Somebody who sets WL_AI_IDLE_TIMEOUT to an hour means it, and silently
// clamping it to our default would be this file making the same mistake it was
// written to fix.
func allowanceFor(idle time.Duration, started bool) time.Duration {
	if started {
		return idle
	}
	if idle > firstEventGrace {
		return idle
	}
	return firstEventGrace
}

// runStreaming runs the CLI and reads its event stream.
func (p *LocalCLIProvider) runStreaming(ctx context.Context, args []string, onActivity func(ProviderActivity)) (*streamResult, error) {
	idle := p.IdleTimeout
	if idle <= 0 {
		idle = defaultIdleTimeout
	}

	cap := absoluteCap
	if p.TotalCap > 0 && p.TotalCap < cap {
		cap = p.TotalCap
	}
	ctx, cancel := context.WithTimeout(ctx, cap)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.Bin, args...)
	if p.Dir != "" {
		cmd.Dir = p.Dir
	}
	// Its own process group, so an abandoned call can be killed WITH its
	// children. Killing only the leader leaves a child holding the stdout pipe,
	// and the reader then blocks on a pipe nothing will ever write to again —
	// the abort is detected and does not take effect. Measured: a stalled fake
	// provider was correctly identified as idle after 300ms and the call still
	// took the child's full 30 seconds to return.
	platform.StartInOwnGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	started := time.Now()
	var (
		mu         sync.Mutex
		last       = started
		events     int
		nbytes     int64
		phase      string
		rateLimit  *RateLimitInfo
		finalLine  string
		answeredBy string
	)

	snapshot := func() ProviderActivity {
		return ProviderActivity{
			Events: events, Bytes: nbytes, LastEventAt: last,
			IdleFor: time.Since(last), Elapsed: time.Since(started),
			Phase: phase, RateLimit: rateLimit,
		}
	}

	// The idle watcher. Counted and stopped: a goroutine that outlives the call
	// it was watching is a leak per call, and generation makes dozens of calls.
	done := make(chan struct{})
	stalled := make(chan ProviderActivity, 1)
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		tick := time.NewTicker(pollFor(idle))
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				mu.Lock()
				act := snapshot()
				started := events > 0
				mu.Unlock()
				if act.IdleFor > allowanceFor(idle, started) {
					select {
					case stalled <- act:
					default:
					}
					// The GROUP, so children die with it and release the pipe.
					_ = platform.KillGroup(cmd)
					return
				}
				if onActivity != nil {
					onActivity(act)
				}
			}
		}
	}()

	// Read on its own goroutine so an abort does not have to wait for the pipe
	// to close. Killing the group should release it, but "should" is how a
	// stalled provider becomes a stalled build — the abort path must not depend
	// on the thing it is aborting cooperating.
	readDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64<<10), maxStreamLine)
		for scanner.Scan() {
			line := scanner.Bytes()
			mu.Lock()
			last = time.Now()
			events++
			nbytes += int64(len(line))
			var ev streamEvent
			if json.Unmarshal(line, &ev) == nil {
				if ev.Type != "" {
					phase = ev.Type
					if ev.Subtype != "" {
						phase = ev.Type + "/" + ev.Subtype
					}
				}
				if ev.RateLimitInfo != nil {
					rateLimit = ev.RateLimitInfo
				}
				// Which model produced the answer, as it said. See streamEvent
				// on why this is read here and not from modelUsage accounting.
				if ev.Type == "assistant" && ev.Message.Model != "" {
					answeredBy = ev.Message.Model
				}
				// The terminal envelope. `result` is the type the final line
				// carries, and it is the one faultFromClaudeCode reads.
				if ev.Type == "result" {
					finalLine = string(line)
				}
			}
			mu.Unlock()
		}
		readDone <- scanner.Err()
	}()

	var scanErr error
	var waitErr error
	select {
	case scanErr = <-readDone:
		close(done)
		watcher.Wait()
		waitErr = cmd.Wait()
	case stalledAct := <-stalled:
		close(done)
		watcher.Wait()
		// Not waited on: cmd.Wait blocks until the pipe is drained, and the
		// point of this branch is that something is not draining.
		return nil, &idleAbort{IdleFor: stalledAct.IdleFor, Elapsed: stalledAct.Elapsed,
			Events: stalledAct.Events, Provider: p.Name}
	}

	mu.Lock()
	act := snapshot()
	final := finalLine
	rl := rateLimit
	model := answeredBy
	mu.Unlock()

	if ctx.Err() == context.DeadlineExceeded {
		// An advisory budget running out is an ordinary outcome, reported as
		// itself. The backstop firing is not — that is a provider that never
		// terminates, and it is a fault to investigate rather than a number to
		// raise.
		if p.TotalCap > 0 && cap == p.TotalCap {
			return nil, &advisoryExhausted{Budget: p.TotalCap, Provider: p.Name}
		}
		return nil, fmt.Errorf("%s ran for %s without finishing, past the %s backstop — "+
			"this is not a budget to raise, it is a provider that never terminates",
			p.Name, act.Elapsed.Round(time.Second), absoluteCap)
	}

	res := &streamResult{Final: final, Activity: act, RateLimit: rl, Model: model}
	if final != "" {
		var env struct {
			Result  string `json:"result"`
			IsError bool   `json:"is_error"`
		}
		if json.Unmarshal([]byte(final), &env) == nil {
			res.Text = env.Result
			if env.IsError {
				if f := faultFromClaudeCode(p.Name, final); f != nil {
					return res, f
				}
			}
		}
	}
	if waitErr != nil {
		// A non-zero exit that still produced a terminal envelope is described
		// by that envelope; the exit code adds nothing.
		if final != "" {
			if f := faultFromClaudeCode(p.Name, final); f != nil {
				return res, f
			}
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = fmt.Sprintf("%d event(s), %d byte(s) received", act.Events, act.Bytes)
		}
		return res, fmt.Errorf("%s: %w: %s", p.Name, waitErr, detail)
	}
	if scanErr != nil && scanErr != io.EOF {
		return res, fmt.Errorf("%s: reading the event stream: %w", p.Name, scanErr)
	}
	if final == "" {
		return res, fmt.Errorf("%s exited without a terminal result event (%d event(s) seen)", p.Name, act.Events)
	}
	return res, nil
}

// chatStreaming is Chat over the event stream.
//
// The flags matter and are not interchangeable: `--output-format stream-json`
// requires `--verbose`, and without it the CLI refuses the combination. Found
// by running it, which is the only way this kind of thing is ever found.
func (p *LocalCLIProvider) chatStreaming(messages []Message) (string, error) {
	args := append([]string(nil), p.Args...)
	args = replaceOutputFormat(args, "stream-json")
	if !hasFlag(args, "--verbose") {
		args = append(args, "--verbose")
	}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	args = append(args, flattenMessages(messages))

	res, err := p.runStreaming(context.Background(), args, p.OnActivity)
	if res != nil && res.RateLimit != nil {
		p.noteRateLimit(res.RateLimit)
	}
	if err != nil {
		return "", err
	}
	if res.Model != "" {
		p.observedMu.Lock()
		p.observed = res.Model
		p.observedMu.Unlock()
	}
	if strings.TrimSpace(res.Text) == "" {
		return "", fmt.Errorf("%s returned an empty result after %d event(s)", p.Name, res.Activity.Events)
	}
	return res.Text, nil
}

// replaceOutputFormat swaps the value of --output-format, or adds it.
//
// The provider is constructed with `--output-format json`; streaming needs
// `stream-json` and appending a second flag would leave the tool to choose
// between them.
func replaceOutputFormat(args []string, value string) []string {
	out := make([]string, 0, len(args)+2)
	replaced := false
	for i := 0; i < len(args); i++ {
		if args[i] == "--output-format" && i+1 < len(args) {
			out = append(out, "--output-format", value)
			i++
			replaced = true
			continue
		}
		if strings.HasPrefix(args[i], "--output-format=") {
			out = append(out, "--output-format="+value)
			replaced = true
			continue
		}
		out = append(out, args[i])
	}
	if !replaced {
		out = append(out, "--output-format", value)
	}
	return out
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

// lastRateLimit is the provider's most recent account of its own quota.
var (
	lastRateLimitMu sync.Mutex
	lastRateLimit   *RateLimitInfo
)

func (p *LocalCLIProvider) noteRateLimit(rl *RateLimitInfo) {
	lastRateLimitMu.Lock()
	lastRateLimit = rl
	lastRateLimitMu.Unlock()
}

// LastRateLimit returns what the provider last said about its quota, or nil.
//
// Read by the retry policy so a wait can be derived from `resetsAt` instead of
// guessed by a backoff table, and reportable so a build can say "the provider
// is at 92% of a five-hour window" before it starts rather than after it fails.
func LastRateLimit() *RateLimitInfo {
	lastRateLimitMu.Lock()
	defer lastRateLimitMu.Unlock()
	return lastRateLimit
}
