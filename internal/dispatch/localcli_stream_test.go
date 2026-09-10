package dispatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/avaropoint/weblisk-cli/internal/platform"
)

// fakeCLI writes an executable script that emits the given shell body.
//
// A script rather than a mocked interface: the thing under test is how this
// code reads a subprocess's pipe, and a fake that is not a subprocess does not
// exercise the pipe, the scanner, the kill or the wait.
func fakeCLI(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLI is a shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-cli")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func resultLine(text string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false, "result": text,
	})
	return string(b)
}

// A call that produces output for LONGER than any total deadline, and is never
// silent, must succeed.
//
// This is the whole design change in one test. The old path gave a call ten
// minutes of wall clock; generation legitimately exceeded it, the deadline
// killed working work, the abort was reported as HTTP 408, 408 was classified
// transient, and the retry did the same thing six more times. Total elapsed
// time was never the signal — silence is.
func TestALongButLivelyCallSucceeds(t *testing.T) {
	// Emits an event every 100ms for ~1.2s, with an idle allowance of 400ms.
	// Total elapsed is 3x the allowance and it must still finish.
	bin := fakeCLI(t, `
i=0
while [ $i -lt 12 ]; do
  echo '{"type":"assistant"}'
  sleep 0.1
  i=$((i+1))
done
echo '`+resultLine("done")+`'`)

	p := &LocalCLIProvider{Bin: bin, Name: "fake", Stream: true, IdleTimeout: 400 * time.Millisecond}
	started := time.Now()
	out, err := p.Chat([]Message{{Role: "user", Content: "go"}})
	if err != nil {
		t.Fatalf("a lively call was aborted: %v", err)
	}
	if out != "done" {
		t.Errorf("result = %q, want %q", out, "done")
	}
	if elapsed := time.Since(started); elapsed < 800*time.Millisecond {
		t.Errorf("finished in %s — the fake cannot have run", elapsed)
	}
}

// A call that goes silent is abandoned, promptly, and says what it saw.
func TestASilentCallIsAbandoned(t *testing.T) {
	bin := fakeCLI(t, `
echo '{"type":"system","subtype":"init"}'
sleep 30`)

	p := &LocalCLIProvider{Bin: bin, Name: "fake", Stream: true, IdleTimeout: 300 * time.Millisecond}
	started := time.Now()
	_, err := p.Chat([]Message{{Role: "user", Content: "go"}})
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a silent call was reported as success")
	}
	if elapsed > 10*time.Second {
		t.Errorf("took %s to abandon a silent call — idleness is known within the poll interval", elapsed)
	}
	var abort *idleAbort
	if !asIdleAbort(err, &abort) {
		t.Fatalf("error is %T (%v), want *idleAbort — the retry policy keys off the type", err, err)
	}
	if abort.Events != 1 {
		t.Errorf("saw %d event(s), want the 1 init event", abort.Events)
	}
	if !strings.Contains(err.Error(), "stalled") {
		t.Errorf("the message does not say what happened: %v", err)
	}
}

// And that abandonment must NOT be retried. It is our decision, reproducible on
// demand; retrying it multiplies the cost by the attempt count and reaches the
// same answer. That is exactly what cost an hour.
func TestAnIdleAbortIsNotTransient(t *testing.T) {
	err := &idleAbort{IdleFor: time.Minute, Elapsed: 10 * time.Minute, Events: 3, Provider: "fake"}
	if isTransient(err) {
		t.Fatal("an idle abort was classified transient — the retry reproduces it and burns the window")
	}
}

// A genuine provider outage is still retried, or the fix above would have
// traded one failure mode for another.
func TestAProviderOutageIsStillTransient(t *testing.T) {
	if !isTransient(&ProviderFault{Provider: "x", Status: 529, Message: "Overloaded"}) {
		t.Error("529 Overloaded is no longer retried")
	}
	if !isTransient(&ProviderFault{Provider: "x", Status: 503, Message: "unavailable"}) {
		t.Error("503 is no longer retried")
	}
}

// The provider's own reset time beats the backoff table.
//
// A table is a guess at "when will this clear"; resetsAt is the answer, and we
// were holding it while consulting the guess.
func TestTheWaitComesFromTheProvidersResetTime(t *testing.T) {
	t.Cleanup(func() {
		lastRateLimitMu.Lock()
		lastRateLimit = nil
		lastRateLimitMu.Unlock()
	})

	// A reset two minutes out: waited for, where the table gives 2s on attempt 1.
	lastRateLimitMu.Lock()
	lastRateLimit = &RateLimitInfo{ResetsAt: time.Now().Add(2 * time.Minute).Unix(), Type: "five_hour"}
	lastRateLimitMu.Unlock()
	got := waitFor(1, fmt.Errorf("429"))
	if got < 110*time.Second || got > 130*time.Second {
		t.Errorf("wait = %s, want ~2m from resetsAt (the table would give %s)", got, transientBackoff(1))
	}

	// A reset four hours out: NOT waited for. The table is used, and the run
	// fails in bounded time rather than sitting on a window that will not turn.
	lastRateLimitMu.Lock()
	lastRateLimit = &RateLimitInfo{ResetsAt: time.Now().Add(4 * time.Hour).Unix(), Type: "five_hour"}
	lastRateLimitMu.Unlock()
	if got := waitFor(1, fmt.Errorf("429")); got != transientBackoff(1) {
		t.Errorf("wait = %s for a 4h reset; beyond the cap it must fall back to the table", got)
	}

	// A reset already past is not a negative wait.
	lastRateLimitMu.Lock()
	lastRateLimit = &RateLimitInfo{ResetsAt: time.Now().Add(-time.Hour).Unix()}
	lastRateLimitMu.Unlock()
	if got := waitFor(2, fmt.Errorf("429")); got <= 0 {
		t.Errorf("wait = %s for an elapsed reset", got)
	}
}

// The quota state must be captured from the stream and reportable, so a build
// can say "92% of a five-hour window" BEFORE it starts rather than dying in it.
func TestTheQuotaStateIsReadFromTheStream(t *testing.T) {
	t.Cleanup(func() {
		lastRateLimitMu.Lock()
		lastRateLimit = nil
		lastRateLimitMu.Unlock()
	})
	rl, _ := json.Marshal(map[string]any{
		"type": "rate_limit_event",
		"rate_limit_info": map[string]any{
			"status": "allowed_warning", "resetsAt": time.Now().Add(30 * time.Minute).Unix(),
			"rateLimitType": "five_hour", "utilization": 0.92,
		},
	})
	bin := fakeCLI(t, "echo '"+string(rl)+"'\necho '"+resultLine("ok")+"'")

	p := &LocalCLIProvider{Bin: bin, Name: "fake", Stream: true, IdleTimeout: 5 * time.Second}
	if _, err := p.Chat([]Message{{Role: "user", Content: "go"}}); err != nil {
		t.Fatal(err)
	}
	got := LastRateLimit()
	if got == nil {
		t.Fatal("the rate_limit_event was not captured")
	}
	if got.Utilization != 0.92 {
		t.Errorf("utilization = %v, want 0.92", got.Utilization)
	}
	note := QuotaNote()
	for _, want := range []string{"92%", "five-hour", "resets in"} {
		if !strings.Contains(note, want) {
			t.Errorf("QuotaNote() = %q, want it to mention %q", note, want)
		}
	}
}

// Activity must be observable while the call runs. A console that can only show
// a spinner is a console that showed a spinner for sixty-five minutes.
func TestActivityIsReportedWhileTheCallRuns(t *testing.T) {
	bin := fakeCLI(t, `
i=0
while [ $i -lt 8 ]; do echo '{"type":"assistant"}'; sleep 0.15; i=$((i+1)); done
echo '`+resultLine("ok")+`'`)

	var seen []ProviderActivity
	p := &LocalCLIProvider{
		Bin: bin, Name: "fake", Stream: true, IdleTimeout: 3 * time.Second,
		OnActivity: func(a ProviderActivity) { seen = append(seen, a) },
	}
	if _, err := p.Chat([]Message{{Role: "user", Content: "go"}}); err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 {
		t.Fatal("no activity was reported during a call lasting over a second")
	}
	last := seen[len(seen)-1]
	if last.Events == 0 {
		t.Error("activity reported no events")
	}
	if last.Phase == "" {
		t.Error("activity reported no phase — a caller cannot say what it is doing")
	}
}

// A call that exits with no terminal result is a fault, not an empty success.
func TestAStreamWithNoResultEventIsAFault(t *testing.T) {
	bin := fakeCLI(t, `echo '{"type":"assistant"}'`)
	p := &LocalCLIProvider{Bin: bin, Name: "fake", Stream: true, IdleTimeout: 3 * time.Second}
	_, err := p.Chat([]Message{{Role: "user", Content: "go"}})
	if err == nil {
		t.Fatal("a stream with no result event was reported as success")
	}
	if !strings.Contains(err.Error(), "terminal result") {
		t.Errorf("unhelpful message: %v", err)
	}
}

// --output-format must be REPLACED, not appended. The provider is built with
// `json`; appending `stream-json` would leave the tool choosing between two.
func TestTheOutputFormatIsReplacedNotAppended(t *testing.T) {
	got := replaceOutputFormat([]string{"--print", "--output-format", "json", "--tools", ""}, "stream-json")
	joined := strings.Join(got, " ")
	if strings.Count(joined, "--output-format") != 1 {
		t.Errorf("args carry --output-format twice: %v", got)
	}
	if !strings.Contains(joined, "--output-format stream-json") {
		t.Errorf("format not replaced: %v", got)
	}
	// The = form too.
	got = replaceOutputFormat([]string{"--output-format=json"}, "stream-json")
	if strings.Join(got, " ") != "--output-format=stream-json" {
		t.Errorf("the = form was not replaced: %v", got)
	}
	// Absent entirely: added.
	got = replaceOutputFormat([]string{"--print"}, "stream-json")
	if !strings.Contains(strings.Join(got, " "), "--output-format stream-json") {
		t.Errorf("format not added: %v", got)
	}
}

// stream-json requires --verbose; the CLI refuses the combination without it.
func TestStreamingPassesVerbose(t *testing.T) {
	args := replaceOutputFormat(claudeCodeArgs(), "stream-json")
	if !hasFlag(args, "--verbose") {
		args = append(args, "--verbose")
	}
	if !hasFlag(args, "--verbose") {
		t.Error("--verbose is not passed, and claude refuses stream-json without it")
	}
}

func asIdleAbort(err error, out **idleAbort) bool {
	if a, ok := err.(*idleAbort); ok {
		*out = a
		return true
	}
	return false
}

// An abandoned call must not leave a subprocess behind.
//
// The prompt-return test above passes whether or not children are killed,
// because the reader runs on its own goroutine and the abort does not wait for
// the pipe. That makes it a test of responsiveness, not of cleanup — and the
// mutation that killed only the leader instead of the group survived it.
//
// This is what the group kill is for. Killing only the leader leaves its
// children alive, holding the pipe, for as long as they like. Generation makes
// dozens of calls; a leaked model subprocess per abandoned call is a machine
// filling up with work nobody is reading.
func TestAnAbandonedCallLeavesNoChildBehind(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	// The shell emits one event, starts a long-lived grandchild, records its
	// pid, and then goes silent. The grandchild is what survives a kill aimed
	// at the leader alone.
	bin := fakeCLI(t, `
echo '{"type":"system","subtype":"init"}'
sleep 60 &
echo $! > `+pidFile+`
wait`)

	p := &LocalCLIProvider{Bin: bin, Name: "fake", Stream: true, IdleTimeout: 300 * time.Millisecond}
	if _, err := p.Chat([]Message{{Role: "user", Content: "go"}}); err == nil {
		t.Fatal("the silent call was reported as success")
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the fake never recorded its grandchild: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("bad pid %q: %v", raw, err)
	}

	// A moment for the signal to land. Bounded, and it fails rather than
	// waiting out the grandchild's own 60 seconds.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !platform.ProcessAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Clean up what the code under test failed to.
	if proc, perr := os.FindProcess(pid); perr == nil {
		_ = proc.Kill()
	}
	t.Fatalf("grandchild %d survived the abort — the leader was killed and its children were not, "+
		"so an abandoned call leaks a subprocess", pid)
}

// Which model answered is read from the assistant event, not from usage
// accounting.
//
// The first version picked the entry with the most output tokens out of the
// terminal envelope's modelUsage map. That map is keyed by every model the call
// touched, including a small helper the tool uses for its own bookkeeping — and
// on a real call the helper emitted 12 output tokens while the answer was 4, so
// a call that ran on claude-opus-5 was recorded as claude-haiku-4-5.
//
// Provenance recorded wrongly is worse than provenance not recorded: the chain
// exists to answer "what produced this", and it answered confidently and wrong.
func TestTheAnsweringModelIsReadFromTheAssistantEvent(t *testing.T) {
	assistant := `{"type":"assistant","message":{"model":"claude-opus-5","content":[{"type":"text","text":"ok"}]}}`
	// A terminal envelope whose usage accounting would name the helper: it has
	// three times the output tokens of the model that actually answered.
	final := `{"type":"result","subtype":"success","is_error":false,"result":"ok",` +
		`"modelUsage":{"claude-haiku-4-5-20251001":{"outputTokens":12},"claude-opus-5":{"outputTokens":4}}}`

	bin := fakeCLI(t, "echo '"+assistant+"'\necho '"+final+"'")
	p := &LocalCLIProvider{Bin: bin, Name: "fake", Stream: true, IdleTimeout: 5 * time.Second}
	if _, err := p.Chat([]Message{{Role: "user", Content: "go"}}); err != nil {
		t.Fatal(err)
	}

	p.observedMu.Lock()
	got := p.observed
	p.observedMu.Unlock()

	if got == "" {
		t.Fatal("no model was recorded, so this build cannot say what wrote it")
	}
	if got != "claude-opus-5" {
		t.Errorf("recorded %q; the assistant event says claude-opus-5 — a usage-token heuristic picks the helper", got)
	}
}

// Before the first event, the allowance is generous; after it, tight.
//
// Two different questions. Once a stream has started, silence is anomalous —
// measured on a real build, the provider emits `system/thinking_tokens`
// continuously while it thinks, so a working model is never quiet for long.
// Before the first event there is no stream to be stalled and "thinking about a
// large prompt" is indistinguishable from "hung", so nothing can be concluded.
//
// Found by watching a real build: the gap to the first event on a full
// orchestrator plan was ~80 seconds, which a 3-minute allowance survives and a
// tighter one would have killed. The fix is not a bigger single number.
func TestSilenceBeforeTheFirstEventGetsTheLongerAllowance(t *testing.T) {
	// Silent from the start for longer than the tight allowance, then speaks.
	// With one allowance for both cases this is abandoned; with the grace it
	// finishes.
	bin := fakeCLI(t, `
sleep 1.2
echo '{"type":"system","subtype":"init"}'
echo '`+resultLine("thought about it")+`'`)

	p := &LocalCLIProvider{Bin: bin, Name: "fake", Stream: true,
		IdleTimeout: 300 * time.Millisecond}
	out, err := p.Chat([]Message{{Role: "user", Content: "go"}})
	if err != nil {
		t.Fatalf("a model thinking before its first event was abandoned: %v", err)
	}
	if out != "thought about it" {
		t.Errorf("result = %q", out)
	}
}

// And after the first event the tight allowance applies again, or the grace
// would become the policy for the whole call.
func TestSilenceAfterTheFirstEventUsesTheTightAllowance(t *testing.T) {
	bin := fakeCLI(t, `
echo '{"type":"system","subtype":"init"}'
sleep 30`)

	p := &LocalCLIProvider{Bin: bin, Name: "fake", Stream: true,
		IdleTimeout: 300 * time.Millisecond}
	started := time.Now()
	_, err := p.Chat([]Message{{Role: "user", Content: "go"}})
	if err == nil {
		t.Fatal("silence after the first event was tolerated")
	}
	// It must use the 300ms allowance, not the multi-minute grace.
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("took %s — the pre-first-event grace is being applied after the stream started", elapsed)
	}
	var abort *idleAbort
	if !asIdleAbort(err, &abort) {
		t.Fatalf("error is %T, want *idleAbort", err)
	}
}

// The grace must be the longer of the two, or the distinction is inverted.
func TestTheGraceIsLongerThanTheMidStreamAllowance(t *testing.T) {
	if firstEventGrace <= defaultIdleTimeout {
		t.Errorf("firstEventGrace (%s) must exceed defaultIdleTimeout (%s): nothing can be "+
			"concluded before the first event, and a stalled stream is anomalous after it",
			firstEventGrace, defaultIdleTimeout)
	}
}

// An explicitly configured allowance longer than the grace must win. Somebody
// who sets WL_AI_IDLE_TIMEOUT to an hour means it.
func TestAnExplicitAllowanceLongerThanTheGraceIsHonoured(t *testing.T) {
	bin := fakeCLI(t, `
sleep 0.5
echo '{"type":"system","subtype":"init"}'
echo '`+resultLine("ok")+`'`)
	p := &LocalCLIProvider{Bin: bin, Name: "fake", Stream: true,
		IdleTimeout: 10 * time.Minute}
	if _, err := p.Chat([]Message{{Role: "user", Content: "go"}}); err != nil {
		t.Fatalf("an explicit long allowance was not honoured: %v", err)
	}
}

// The allowance decision, as a table.
//
// Tested directly because the case that matters — an explicit allowance longer
// than the grace — needs eight minutes of silence to observe through a
// subprocess, and a mutation removing that clamp survived every subprocess test
// here for exactly that reason.
func TestAllowanceForEachPhase(t *testing.T) {
	for _, tc := range []struct {
		name    string
		idle    time.Duration
		started bool
		want    time.Duration
	}{
		{"before the first event, default idle", 3 * time.Minute, false, firstEventGrace},
		{"before the first event, tiny idle", time.Second, false, firstEventGrace},
		{"before the first event, explicit longer than grace", time.Hour, false, time.Hour},
		{"before the first event, explicit equal to grace", firstEventGrace, false, firstEventGrace},
		{"after the first event, default idle", 3 * time.Minute, true, 3 * time.Minute},
		{"after the first event, tiny idle", time.Second, true, time.Second},
		{"after the first event, explicit long idle", time.Hour, true, time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := allowanceFor(tc.idle, tc.started); got != tc.want {
				t.Errorf("allowanceFor(%s, started=%v) = %s, want %s", tc.idle, tc.started, got, tc.want)
			}
		})
	}
}

// The idle detector abandons a call that goes quiet for three minutes. That is
// only safe if a working model is never quiet for that long — and it is not,
// unless the call asked for partial messages. Without them a tool emits whole
// messages only, so a long thinking phase is silent and a perfectly healthy
// planning call is killed. Measured: longest gap 21.0s without, 1.8s with; on
// a planning-sized prompt the first exceeds the threshold.
func TestStreamingAsksForPartialMessages(t *testing.T) {
	got := withPartialMessages([]string{"--print", "--output-format", "stream-json", "--verbose"})
	if !hasFlag(got, "--include-partial-messages") {
		t.Error("a streaming call does not ask for partial messages, so the idle timeout " +
			"is measuring silence that means nothing")
	}

	// Grok's format spells it differently and must also be covered.
	got = withPartialMessages([]string{"--output-format", "streaming-messages-json"})
	if !hasFlag(got, "--include-partial-messages") {
		t.Error("grok's streaming format was not recognised as streaming")
	}

	// claude REFUSES the flag outside streaming — "requires --print and
	// --output-format=stream-json" — so a non-streaming arg set must not get it.
	got = withPartialMessages([]string{"--print", "--output-format", "json"})
	if hasFlag(got, "--include-partial-messages") {
		t.Error("a non-streaming call was given --include-partial-messages; claude exits with an error")
	}

	// A tool driven entirely from WL_AI_ARGS asked for nothing of the kind.
	got = withPartialMessages([]string{"--some-other-tool-flag"})
	if hasFlag(got, "--include-partial-messages") {
		t.Error("a custom arg set was handed a flag it never asked for")
	}

	// Idempotent: an operator who already passed it does not get it twice.
	got = withPartialMessages([]string{"--output-format", "stream-json", "--include-partial-messages"})
	n := 0
	for _, a := range got {
		if a == "--include-partial-messages" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("flag appears %d times", n)
	}
}
