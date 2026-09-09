package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Provenance, stated per fixture rather than in one sweeping sentence, because
// they do not all have the same provenance and saying they did would be the
// same overclaim these tests exist to prevent.
//
// All three binaries are installed on the measuring machine and `--version`
// succeeds for all three. That is the whole point: discovery cannot tell them
// apart.

// CAPTURED, verbatim except for trimming usage counters: what `grok
// --output-format streaming-messages-json` printed on 2026-09-09 when the
// account had no balance left. Note what is NOT in it: no `result` text and no
// `api_error_status`. Only `errors`, with the status buried in a sentence.
const grokExhausted = `{"type":"result","subtype":"error_during_execution","is_error":true,"duration_ms":103,"num_turns":0,"stop_reason":null,"total_cost_usd":0.0,"modelUsage":{},"errors":["Internal error: {\n  \"message\": \"API error (status 402 Payment Required): Grok Build usage balance exhausted\",\n  \"http_status\": 402\n}"],"session_id":"01a087c3-8ecd-7223-ac63-4aa893f2b562"}`

// CAPTURED, one line of many: `codex exec` on an account that was never signed
// in printed roughly 25 lines of reconnect attempts and then this. Not JSON at
// all. Quoted as one line because one line is what reaches a fault message —
// see salientErrorLine, which is the code that has to pick it out.
const codexUnauthorized = `unexpected status 401 Unauthorized: Missing bearer or basic authentication in header, url: https://api.openai.com/v1/responses`

// NOT captured on the measuring machine — claude is logged in there and works.
// This is the sentence recorded in discover.go's own account of the incident
// that started all of this: the CLI runs, and says this instead of generating.
// It is a fixture for the CLASSIFIER, which is all this test uses it for; it is
// not evidence about claude's wire format, and nothing here treats it as such.
const claudeNotLoggedIn = `Not logged in · Please run /login`

func TestGrokBalanceExhaustedIsPermanent(t *testing.T) {
	f := faultFromCLI("grok", grokExhausted)
	if f == nil {
		t.Fatal("grok's terminal error event was not recognised as a fault at all")
	}
	if f.Status != 402 {
		t.Errorf("status = %d, want 402 recovered from the message — grok reports it nowhere else", f.Status)
	}
	if got := f.Class(); got != FaultPermanent {
		t.Errorf("class = %v, want FaultPermanent.\n"+
			"An account with no balance left classed as transient is retried with backoff "+
			"until the build gives up, against a provider that cannot recover.", got)
	}
}

// A stream that merely dropped must stay retryable. The fix above widened what
// counts as permanent, and the way to get that wrong is to make everything so.
func TestGrokDroppedStreamIsStillTransient(t *testing.T) {
	const dropped = `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":[]}`
	f := faultFromCLI("grok", dropped)
	if f == nil {
		t.Fatal("no fault parsed")
	}
	if got := f.Class(); got != FaultTransient {
		t.Errorf("class = %v, want FaultTransient — a stream that stopped with no reason is the retryable case", got)
	}
}

func TestNotLoggedInIsPermanent(t *testing.T) {
	for _, tc := range []struct{ name, msg string }{
		{"claude", claudeNotLoggedIn},
		{"codex", codexUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &ProviderFault{Provider: tc.name, Message: tc.msg}
			if got := f.Class(); got != FaultPermanent {
				t.Errorf("class = %v, want FaultPermanent for %q", got, tc.msg)
			}
		})
	}
}

// withProbe swaps the readiness call for a scripted one.
func withProbe(t *testing.T, answers map[ProviderKind]error) *[]ProviderKind {
	t.Helper()
	saved := probeReady
	t.Cleanup(func() { probeReady = saved })
	var asked []ProviderKind
	probeReady = func(kind ProviderKind, _ string) error {
		asked = append(asked, kind)
		return answers[kind]
	}
	return &asked
}

func installed(kinds ...ProviderKind) []ProviderInfo {
	out := make([]ProviderInfo, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, ProviderInfo{Kind: k, Label: string(k), Available: true, Local: true,
			Detail: "installed; whether it is logged in is settled by ResolveReady before a build"})
	}
	return out
}

// The machine this was written on, exactly: three CLIs installed, one of them
// able to generate. Discovery reports all three as available.
func TestWalkSkipsInstalledButUnauthenticated(t *testing.T) {
	asked := withProbe(t, map[ProviderKind]error{
		ProviderClaudeCode: faultFromCLI("claude", `{"is_error":true,"result":"Not logged in · Please run /login"}`),
		ProviderGrok:       faultFromCLI("grok", grokExhausted),
		ProviderCodex:      nil,
	})

	c := walkReady(installed(ProviderClaudeCode, ProviderGrok, ProviderCodex))

	if c.Err != nil {
		t.Fatalf("walk failed although codex could generate: %v", c.Err)
	}
	if c.Kind != ProviderCodex {
		t.Errorf("chose %q, want codex — the first two run but will not generate", c.Kind)
	}
	// Compared as a SEQUENCE, not a count. The ranking argument — "highest-
	// weighted that will actually generate" — is the whole justification for
	// walking rather than picking, so a walk that asked in discovery order
	// instead of catalog order must fail here.
	want := []ProviderKind{ProviderClaudeCode, ProviderGrok, ProviderCodex}
	if len(*asked) != len(want) {
		t.Fatalf("asked %v, want the catalog walked in order %v", *asked, want)
	}
	for i := range want {
		if (*asked)[i] != want[i] {
			t.Fatalf("asked %v, want the catalog walked in order %v", *asked, want)
		}
	}
	// The skipped rows must not still read as available to whatever prints them.
	for _, o := range c.Options {
		if o.Kind == ProviderClaudeCode || o.Kind == ProviderGrok {
			if o.Available {
				t.Errorf("%s is still listed available after being skipped", o.Kind)
			}
			if !strings.Contains(strings.ToLower(o.Detail), "log") && !strings.Contains(o.Detail, "402") {
				t.Errorf("%s was skipped with an unhelpful reason: %q", o.Kind, o.Detail)
			}
		}
	}
}

// Busy is not broken. A 529 must NOT demote the operator's default.
func TestWalkKeepsABusyProvider(t *testing.T) {
	asked := withProbe(t, map[ProviderKind]error{
		ProviderClaudeCode: &ProviderFault{Provider: "claude", Status: 529, Message: "overloaded"},
	})
	c := walkReady(installed(ProviderClaudeCode, ProviderGrok))
	if c.Kind != ProviderClaudeCode {
		t.Errorf("chose %q, want claude-code — overloaded for two seconds is not a reason to change provider", c.Kind)
	}
	if len(*asked) != 1 {
		t.Errorf("asked %v, want the walk to stop at the busy-but-working first row", *asked)
	}
	if !strings.Contains(c.Why, "busy") {
		t.Errorf("Why = %q, want it to say the provider is busy", c.Why)
	}
}

func TestWalkReportsWhenNothingCanGenerate(t *testing.T) {
	withProbe(t, map[ProviderKind]error{
		ProviderClaudeCode: &ProviderFault{Provider: "claude", Message: claudeNotLoggedIn},
		ProviderGrok:       faultFromCLI("grok", grokExhausted),
	})
	c := walkReady(installed(ProviderClaudeCode, ProviderGrok))
	if c.Err == nil {
		t.Fatalf("walk returned %q with no error, although nothing could generate", c.Kind)
	}
	for _, want := range []string{"claude-code", "grok", "402"} {
		if !strings.Contains(c.Err.Error(), want) {
			t.Errorf("error does not name %q — an operator cannot act on it:\n%v", want, c.Err)
		}
	}
}

// An explicit --provider must not walk. A tenant pinned to a local model for
// data-residency reasons must never be silently sent to an API instead.
func TestExplicitRequestIsNeverWalked(t *testing.T) {
	asked := withProbe(t, map[ProviderKind]error{})
	_ = ResolveReady(context.Background(), "ollama")
	if len(*asked) != 0 {
		t.Errorf("readiness probed %v for an explicitly requested provider — an explicit pin must fail as itself, not fall back", *asked)
	}
}

// The generation contract is: text out, no side effects. Every local CLI this
// pipeline drives defaults to a full agentic toolbox — grok had 27 tools,
// codex will run shell commands — so each preset must say otherwise, and this
// asserts the OUTCOME rather than any one flag's spelling. `--tools ""` looks
// like a restriction and is one for claude and not for grok; a test that
// matched on that string would have passed throughout the bug it is here to
// prevent.
func TestEveryLocalCLIPresetRestrictsTools(t *testing.T) {
	t.Setenv("WL_AI_ARGS", "")
	for _, tc := range []struct {
		name string
		args []string
		// want is a flag that must be present with a NON-EMPTY value, or a
		// bare flag, that constrains what the model may do.
		want []string
	}{
		{"claude-code", claudeCodeArgs(), []string{"--tools"}},
		{"grok", grokArgs(), []string{"--tools", "--disallowed-tools", "--permission-mode"}},
		{"codex", codexArgs(), []string{"--sandbox"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			joined := strings.Join(tc.args, " ")
			for _, flag := range tc.want {
				if !strings.Contains(joined, flag) {
					t.Errorf("%s is dispatched with no %s — the model gets its full toolbox and may write files, "+
						"run commands, or block on an approval no headless caller can answer.\nargs: %v",
						tc.name, flag, tc.args)
				}
			}
		})
	}
}

// grok specifically: the empty value is a no-op, measured against grok 1.0.24
// by reading the system/init event's resolved tool list (27 tools with
// `--tools ""`, 0 with the pair below). Nothing in the flag NAME says so, so
// the knowledge lives here.
func TestGrokIsNotDispatchedWithTheEmptyToolValue(t *testing.T) {
	t.Setenv("WL_AI_ARGS", "")
	args := grokArgs()
	for i, a := range args {
		if a == "--tools" {
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				t.Fatal(`grok is dispatched with --tools "" again. Grok IGNORES the empty value: ` +
					`it resolves to all 27 built-in tools, including write and run_terminal_command. ` +
					`That is the tenant-planning hang. Pair an allowlist with --disallowed-tools instead.`)
			}
		}
	}
	if !strings.Contains(strings.Join(args, " "), "--disallowed-tools") {
		t.Error("an allowlist alone cannot reach zero tools — search_tool and use_tool survive it, " +
			"so --disallowed-tools is the other half of the pair, not redundancy")
	}
}

// The walk's verdict must reach the operator intact. RequireProvider's
// "configure an AI provider" advice is for a machine with nothing installed;
// printing it when three CLIs are installed and none is logged in tells the
// operator to do what they have already done.
func TestNothingReadyIsDistinguishableFromNothingInstalled(t *testing.T) {
	withProbe(t, map[ProviderKind]error{
		ProviderClaudeCode: &ProviderFault{Provider: "claude", Message: claudeNotLoggedIn},
	})
	c := walkReady(installed(ProviderClaudeCode))
	if c.Err == nil {
		t.Fatal("no error although the only provider could not generate")
	}
	if !errors.Is(c.Err, ErrNothingReady) {
		t.Errorf("the walk's verdict does not carry ErrNothingReady, so RequireProvider "+
			"cannot tell it apart from an empty machine and will bury it: %v", c.Err)
	}

	// And the empty-machine case must NOT carry it.
	empty := chooseFromUsable(nil, nil)
	if empty.Err == nil {
		t.Fatal("no error for a machine with no providers at all")
	}
	if errors.Is(empty.Err, ErrNothingReady) {
		t.Error("a machine with nothing installed reports ErrNothingReady, which would suppress " +
			"the install instructions that are exactly what it needs")
	}
}

// A --model override arrives AFTER the walk has probed, so the thing that
// answered and the thing that will generate are not the same. ChooseProvider
// does `if model != "" { c.Model = model }` below ResolveReady; pkg/tenant does
// the same with spec.Model. Keyed by provider kind alone, the pre-flight skip
// then applied to a model nothing had asked about, and DiscoverProvider printed
// "[ready]" for it.
func TestVerificationDoesNotCarryOverToADifferentModel(t *testing.T) {
	kind := ProviderClaudeCode
	MarkVerified(kind, "opus-from-the-walk")

	if !AlreadyVerified(kind, "opus-from-the-walk") {
		t.Fatal("the model that actually answered is not recorded as verified")
	}
	if AlreadyVerified(kind, "some-other-model") {
		t.Error("a --model override inherits the walk's verification. " +
			"RequireProvider would skip its pre-flight for a model nothing has asked, " +
			"and the status line would report it [ready].")
	}
	if AlreadyVerified(kind, "") {
		t.Error(`the empty model inherits verification from a specific one`)
	}
}

// The fix that defeated itself. Claude Code emits BOTH a human `result` and an
// `errors` array holding a diagnostic — there is a captured example in
// transient_test.go. Reading `errors` unconditionally replaced the sentence
// permanentMessage needs with a diagnostic that matches nothing, so the exact
// failure this whole change exists to catch came out TRANSIENT and got retried.
func TestAnErrorsArrayDoesNotDisplaceTheResultSentence(t *testing.T) {
	const both = `{"type":"result","subtype":"error_during_execution","is_error":true,` +
		`"result":"Not logged in · Please run /login",` +
		`"errors":["[ede_diagnostic] result_type=user last_content_type=n/a stop_reason=null"]}`

	f := faultFromCLI("claude", both)
	if f == nil {
		t.Fatal("no fault parsed")
	}
	if !strings.Contains(f.Message, "Not logged in") {
		t.Errorf("message = %q, want the human sentence — a diagnostic matches nothing in permanentMessage", f.Message)
	}
	if got := f.Class(); got != FaultPermanent {
		t.Errorf("class = %v, want FaultPermanent", got)
	}
	if notReady(f) != true {
		t.Error("the walk would keep a provider that says it is not logged in")
	}

	// And grok, which has no `result` at all, still gets its reason read.
	g := faultFromCLI("grok", grokExhausted)
	if g == nil || !strings.Contains(g.Message, "balance exhausted") {
		t.Errorf("grok's reason was lost by the guard: %+v", g)
	}
}

// A status must be a claim the provider made on purpose. Scraping any
// three-digit number near the word "status" turned Cloudflare's 524 — a
// timeout, the most retryable thing there is — into a permanent fault.
func TestAStatusIsReadOnlyFromAFieldNamedAsOne(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantStatus int
		wantClass  FaultClass
	}{
		{"explicit http_status", `{\"message\": \"nope\", \"http_status\": 402}`, 402, FaultPermanent},
		{"prose 524 is not scraped", `API error (status 524 A Timeout Occurred)`, 0, FaultTransient},
		{"prose 200 is not scraped", `upstream said status 200 but sent nothing`, 0, FaultTransient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["Internal error: ` + tc.body + `"]}`
			f := faultFromCLI("grok", env)
			if f == nil {
				t.Fatal("no fault parsed")
			}
			if f.Status != tc.wantStatus {
				t.Errorf("status = %d, want %d", f.Status, tc.wantStatus)
			}
			if got := f.Class(); got != tc.wantClass {
				t.Errorf("class = %v, want %v — a number in prose is not a status the provider named", got, tc.wantClass)
			}
		})
	}
}

// Our own decision to stop waiting is never a provider condition. A readiness
// probe that hits its own cap says nothing about whether the backend can
// generate, and demoting a healthy provider for being slower than a number we
// picked is the opposite of what the walk is for.
func TestOurOwnDeadlineDoesNotDisqualifyAProvider(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"advisory budget", &advisoryExhausted{Budget: time.Minute, Provider: "claude code"}},
		{"our readiness guard", &ProviderFault{Provider: "ollama", Status: 408, Message: "did not answer in time"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if notReady(tc.err) {
				t.Errorf("%v disqualified the provider — but this is our deadline, not its failure", tc.err)
			}
		})
	}
	// A real refusal still does disqualify.
	if !notReady(&ProviderFault{Provider: "codex", Message: codexUnauthorized}) {
		t.Error("a 401 no longer disqualifies, which defeats the walk")
	}
}
