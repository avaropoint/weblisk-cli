package dispatch

// Local CLI providers.
//
// Some of the most capable models are reachable only through a coding-agent CLI
// installed on the machine — Claude Code being the one this file verifies
// against. These are not HTTP APIs: there is no base URL and no key, the
// transport is a subprocess, and the credential is whatever the tool was already
// logged in with.
//
// That matters for hub generation specifically. `weblisk server init` dispatches
// to a model to generate a hub from blueprints, and requiring an API key to do it
// puts a paid account between an operator and their first hub. With a local CLI
// there is nothing to configure and nothing to bill.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/avaropoint/weblisk-cli/internal/platform"
)

// localCLIInstallDirs are the places a coding-agent CLI actually installs
// itself, searched when it is not on PATH.
//
// A process started by launchd, systemd, a double-click or an editor does NOT
// inherit a login shell's PATH, and these tools install to directories that are
// only on PATH because a shell profile puts them there. So exec.LookPath fails
// on machines where the tool is installed and working — which presents as "not
// installed" to somebody who is using it in another window at the time.
var localCLIInstallDirs = []string{
	"~/.local/bin",      // the official Claude Code installer
	"~/.claude/local",   // older local installs
	"~/.grok/bin",       // Grok managed install
	"/opt/homebrew/bin", // Homebrew, Apple silicon
	"/usr/local/bin",    // Homebrew, Intel; manual installs
	"~/.npm-global/bin", // npm -g with a user prefix
	"/usr/local/lib/node_modules/.bin",
	"~/.bun/bin",
	"~/.cargo/bin",
}

// ResolveLocalCLI returns a usable path to a CLI, or "" plus the places it
// looked so the caller can say something useful.
//
// An explicit path is honoured exactly as given and never second-guessed: an
// operator who typed a path meant it, and silently running a different binary
// than the one they named is worse than failing.
func ResolveLocalCLI(name, configured string) (string, []string) {
	if c := strings.TrimSpace(configured); c != "" {
		// Honoured as given — but checked, so a typo fails where it was made
		// rather than as an exec error several steps later.
		if filepath.IsAbs(c) {
			if info, err := os.Stat(c); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
				return "", []string{c}
			}
		}
		return c, nil
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	dirs := append([]string(nil), localCLIInstallDirs...)
	dirs = append(dirs, platform.ExtraInstallDirs()...)
	searched := make([]string, 0, len(dirs)*4)
	for _, dir := range dirs {
		full := dir
		if strings.HasPrefix(dir, "~/") && home != "" {
			full = filepath.Join(home, dir[2:])
		}
		for _, candName := range platform.LookPathCandidates(name) {
			candidate := filepath.Join(full, candName)
			searched = append(searched, candidate)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				return candidate, nil
			}
		}
	}
	return "", searched
}

// LocalCLIProvider drives a coding-agent CLI as a subprocess.
type LocalCLIProvider struct {
	Bin   string   // resolved binary path
	Name  string   // provider name, for errors
	Args  []string // flags before the prompt
	Model string   // "" leaves the tool's default
	// observed is the model the tool actually used, as it reported it.
	//
	// A build that does not record which model wrote a file cannot answer the
	// question the whole chain exists to answer. WL_AI_MODEL was unset on every
	// run so far, so generation ran on whatever the CLI's default happened to
	// be that week — it resolved to a small model, and nothing in the output
	// said so. That is not a setting anybody chose; it is a setting nobody saw.
	observed   string
	observedMu sync.Mutex
	JSON       bool          // parse stdout as a result envelope
	Timeout    time.Duration // 0 means defaultLocalCLITimeout (non-streaming only)
	Dir        string        // working directory, "" means inherit
	// PromptFlag, when set, passes the prompt as that flag's value rather than
	// as a trailing positional argument. Grok's `--single` is the reason: a
	// positional prompt opens the TUI, which is not a generation backend.
	PromptFlag string
	// PromptFileFlag, when set, writes the prompt to a temp file and passes
	// that path as the flag's value. Hub generation prompts exceed ARG_MAX
	// (`argument list too long`) if they go on argv — measured against grok.
	PromptFileFlag string
	// PromptStdin, when set, feeds the prompt on stdin instead of argv, and
	// passes PromptArg in the prompt's place.
	//
	// Codex is the reason and it is the same argv limit grok hit, one step
	// further along: Linux caps a SINGLE argument at MAX_ARG_STRLEN (128 KiB)
	// however large ARG_MAX is, and codex has no --prompt-file. It documents
	// stdin instead, and `codex exec … -` with the prompt piped in was measured
	// echoing that prompt back, so this is its supported route for a prompt too
	// big to be an argument. It matters more now than it did: the readiness
	// walk will actually SELECT codex when the backends above it refuse.
	PromptStdin bool
	// PromptArg is the placeholder passed where the prompt would go when it is
	// being sent on stdin. Codex spells it "-".
	PromptArg string
	// OutputFileFlag, when set, passes a temp file path as that flag's value
	// and reads the ANSWER back from that file instead of from stdout.
	//
	// Codex is the reason. `codex exec` writes a framed transcript to stdout —
	// a banner, `workdir:`, `model:`, `provider:`, `approval:`, `sandbox:`, a
	// `--------` rule and an echo of the prompt — before any answer. Measured.
	// Returning that as a completion puts the banner inside the generated file.
	// `--output-last-message FILE` is the documented way to get the answer and
	// nothing else.
	OutputFileFlag string
	// NativeStream means Args already select a streaming output format, so
	// chatStreaming must not rewrite them to Claude Code's stream-json.
	NativeStream bool
	// Stream drives the CLI with --output-format stream-json and watches its
	// events, so liveness is observed rather than assumed. See
	// localcli_stream.go for why that replaces a total deadline.
	Stream bool
	// IdleTimeout is how long the provider may be SILENT. 0 means
	// defaultIdleTimeout. Total elapsed time is never an abort condition.
	IdleTimeout time.Duration
	// OnActivity, when set, is called as the stream progresses. This is what a
	// console shows instead of a spinner.
	OnActivity func(ProviderActivity)
	// TotalCap bounds one call's total wall time. 0 means only the absolute
	// backstop applies.
	//
	// This is NOT the deadline that was removed. That one bounded every call,
	// including the generation calls that legitimately take longer than any
	// number somebody would pick. This bounds ADVISORY calls, where running out
	// of time is a normal outcome because nothing depends on the answer. See
	// advisory.go.
	TotalCap time.Duration
}

// defaultLocalCLITimeout bounds one call. Generation from blueprints is
// legitimately slow, so this is generous; it is finite because a wedged
// subprocess would otherwise hang the command forever with no output.
const defaultLocalCLITimeout = 10 * time.Minute

// claudeCodeResult is the envelope `claude --output-format json` prints.
type claudeCodeResult struct {
	Result     string `json:"result"`
	IsError    bool   `json:"is_error"`
	Subtype    string `json:"subtype"`
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
}

// flattenMessages turns a conversation into one prompt string. Roles are
// prefixed only when there is more than one message, so a single-turn call is
// not decorated with a label it does not need.
func flattenMessages(messages []Message) string {
	var b strings.Builder
	for i, m := range messages {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if len(messages) > 1 && m.Role != "" {
			b.WriteString(strings.ToUpper(m.Role[:1]) + m.Role[1:] + ":\n")
		}
		b.WriteString(m.Content)
	}
	return b.String()
}

func (p *LocalCLIProvider) Chat(messages []Message) (string, error) {
	if p.Stream {
		return p.chatStreaming(messages)
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultLocalCLITimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := append([]string(nil), p.Args...)
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	args, outPath, outCleanup, err := p.appendOutputFile(args)
	if err != nil {
		return "", err
	}
	defer outCleanup()
	args, cleanup, err := p.appendPrompt(args, flattenMessages(messages))
	if err != nil {
		return "", err
	}
	defer cleanup()

	cmd := exec.CommandContext(ctx, p.Bin, args...)
	if p.Dir != "" {
		cmd.Dir = p.Dir
	}
	if p.PromptStdin {
		cmd.Stdin = strings.NewReader(flattenMessages(messages))
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			// Retryable: 408 is the status for exactly this, and a subprocess
			// that ran out of time on one attempt often finishes on the next.
			return "", &ProviderFault{Provider: p.Name, Status: 408,
				Message: fmt.Sprintf("timed out after %s — the model may still be working; "+
					"raise WL_AI_TIMEOUT if generation legitimately takes longer", timeout)}
		}
		// A non-zero exit still prints the result envelope on stdout, and that
		// envelope carries the status and terminal reason that decide whether
		// this is worth retrying. Read it as a fault rather than pasting the
		// JSON into an error string: the string form was matched against for
		// retry decisions, and its "permission_denials" field made every
		// transient failure look permanent.
		if f := faultFromCLI(p.Name, strings.TrimSpace(stdout.String())); f != nil {
			return "", f
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail == "" {
			detail = err.Error()
		}
		return "", &ProviderFault{Provider: p.Name, Message: detail, Raw: detail}
	}

	// The answer file wins when one was asked for: stdout is a transcript for
	// a person to read, and only this file is the completion.
	if outPath != "" {
		answer, readErr := os.ReadFile(outPath)
		if readErr == nil {
			if a := strings.TrimSpace(string(answer)); a != "" {
				return a, nil
			}
		}
		// Exit 0 and no answer file is a contract this build does not
		// understand. Say that, rather than returning the banner as if it
		// were generated code.
		return "", &ProviderFault{Provider: p.Name, Raw: strings.TrimSpace(stdout.String()),
			Message: fmt.Sprintf("%s exited 0 but wrote no answer to %s (%s). "+
				"Its stdout is a transcript, not a completion, so there is nothing safe to return. "+
				"Override the flags with WL_AI_ARGS if this build of %s reports differently.",
				p.Name, p.OutputFileFlag, outPath, p.Name)}
	}

	raw := strings.TrimSpace(stdout.String())
	if raw == "" {
		return "", fmt.Errorf("%s returned no output", p.Name)
	}
	if !p.JSON {
		return raw, nil
	}

	text, model, isErr := parseCLICompletion(raw)
	if model != "" {
		p.observedMu.Lock()
		p.observed = model
		p.observedMu.Unlock()
	}
	if isErr {
		if f := faultFromCLI(p.Name, raw); f != nil {
			return "", f
		}
		return "", &ProviderFault{Provider: p.Name, Message: text, Raw: raw}
	}
	return text, nil
}

// appendOutputFile reserves a temp path for OutputFileFlag and passes it.
//
// Returns "" for the path when the provider does not use one, so the caller
// keeps a single code path.
func (p *LocalCLIProvider) appendOutputFile(args []string) ([]string, string, func(), error) {
	nop := func() {}
	if p.OutputFileFlag == "" {
		return args, "", nop, nil
	}
	f, err := os.CreateTemp("", "weblisk-answer-*.txt")
	if err != nil {
		return nil, "", nop, fmt.Errorf("reserving answer file: %w", err)
	}
	path := f.Name()
	_ = f.Chmod(0o600)
	// Closed and left in place: the tool writes it, this process only reads it.
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, "", nop, err
	}
	return append(args, p.OutputFileFlag, path), path, func() { _ = os.Remove(path) }, nil
}

func (p *LocalCLIProvider) appendPrompt(args []string, prompt string) ([]string, func(), error) {
	nop := func() {}
	if p.PromptFileFlag != "" {
		f, err := os.CreateTemp("", "weblisk-prompt-*.txt")
		if err != nil {
			return nil, nop, fmt.Errorf("writing prompt file: %w", err)
		}
		path := f.Name()
		cleanup := func() { _ = os.Remove(path) }
		if err := f.Chmod(0o600); err != nil {
			_ = f.Close()
			cleanup()
			return nil, nop, err
		}
		if _, err := f.WriteString(prompt); err != nil {
			_ = f.Close()
			cleanup()
			return nil, nop, fmt.Errorf("writing prompt file: %w", err)
		}
		if err := f.Close(); err != nil {
			cleanup()
			return nil, nop, err
		}
		return append(args, p.PromptFileFlag, path), cleanup, nil
	}
	if p.PromptFlag != "" {
		return append(args, p.PromptFlag, prompt), nop, nil
	}
	if p.PromptStdin {
		if p.PromptArg != "" {
			return append(args, p.PromptArg), nop, nil
		}
		return args, nop, nil
	}
	return append(args, prompt), nop, nil
}

// parseCLICompletion reads a one-shot JSON envelope from a coding-agent CLI.
//
// Claude Code uses `result` / `is_error` / `model`. Grok's `--output-format json`
// uses `text` / `type:error` / `modelUsage`. Both are valid completions; refusing
// one because it is not the other would drop a working answer.
func parseCLICompletion(raw string) (text, model string, isErr bool) {
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) != nil {
		return raw, "", false
	}
	if t, _ := obj["type"].(string); t == "error" {
		msg, _ := obj["message"].(string)
		if msg == "" {
			msg = raw
		}
		return msg, "", true
	}
	if errFlag, _ := obj["is_error"].(bool); errFlag {
		msg := stringFrom(obj, "result", "text", "message")
		if msg == "" {
			msg = raw
		}
		return msg, stringFrom(obj, "model"), true
	}
	text = stringFrom(obj, "result", "text", "content")
	model = stringFrom(obj, "model")
	if model == "" {
		if mu, ok := obj["modelUsage"].(map[string]any); ok {
			for k := range mu {
				model = k
				break
			}
		}
	}
	return text, model, false
}

func stringFrom(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := obj[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// codexArgs runs Codex non-interactively.
//
// `codex exec` is its documented headless mode: no TUI and no session.
//
// # Measured against codex-cli 0.153.4
//
// The previous comment said these flags were never run against a real binary.
// They have been now, and the honest result is in two parts.
//
// What was proved: every flag below is accepted — codex parsed the whole list
// and started a session rather than exiting on an unknown argument. Of the
// five, exactly ONE is corroborated as APPLIED: the preamble echoes
// `sandbox: read-only`. It also echoes `approval: never`, but no approval flag
// is passed here, so that line is codex's own default for `exec` and is
// evidence of nothing about this list.
//
// The account on the measuring machine is not authenticated, so codex answers
// 401 and the generation itself could NOT be exercised end to end. That 401 is
// at least classified correctly: "unauthorized" is in permanentMessage, so it
// fails fast instead of retrying.
//
// What the measurement CHANGED: `codex exec` does not write a bare answer to
// stdout. It writes a framed transcript —
//
//	OpenAI Codex v0.153.4
//	--------
//	workdir: … / model: … / provider: … / approval: … / sandbox: …
//	--------
//	user
//	<the prompt, echoed back>
//
// — and only then the answer. With JSON:false this provider returned stdout
// verbatim, so that banner would have been written into generated source as if
// the model had produced it. `--output-last-message` is the documented flag
// that yields the answer alone; it is read via OutputFileFlag. Not observed
// producing a file (that needs a successful call), so a missing file is an
// explicit error rather than a silent fall back to the transcript.
//
// `--sandbox read-only` is the codex analogue of grok's zero-tool set: codex
// has no flag that removes its tools, so the next best guarantee is that a
// tool call cannot write to the tree being generated. `--ephemeral` keeps a
// one-shot generation out of the session store. `--color never` keeps ANSI
// escapes out of the TRANSCRIPT — it no longer protects the answer, which is
// read from the file below rather than from stdout, and is kept because the
// transcript is what an operator reads when this fails.
//
// WL_AI_ARGS overrides this list entirely, so an operator whose codex
// disagrees can correct it without a new build.
func codexArgs() []string {
	if custom := splitArgs(os.Getenv("WL_AI_ARGS")); len(custom) > 0 {
		return custom
	}
	return []string{
		"exec",
		"--skip-git-repo-check",
		"--ephemeral",
		"--sandbox", "read-only",
		"--color", "never",
	}
}

// claudeCodeArgs are the flags that make a one-shot, non-interactive call.
//
// The built-in tool set, the user's MCP servers, the slash-command catalogue and
// session persistence are all switched off: this is a single generation from a
// prompt, and every one of those would either change the output or reach for
// state that has nothing to do with the hub being generated.
//
// # `--tools ""` here means the opposite of what it means in grokArgs
//
// Claude Code honours the empty value: asked to run a shell command with these
// flags, claude 2.1.266 answered "NO_TOOLS" and reported no tool available.
// Measured. Grok IGNORES the same empty value and hands the model all 27 of
// its tools — see grokArgs, where that cost a tenant build.
//
// So this line is correct and the identical-looking one in grokArgs was not.
// Do not "make them consistent" by copying either into the other; they are
// different CLIs that happen to spell a flag the same way. The guard is
// TestEveryLocalCLIPresetRestrictsTools, which asserts the OUTCOME rather than
// the spelling.
func claudeCodeArgs() []string {
	return []string{
		"--print", "--output-format", "json",
		"--tools", "",
		"--strict-mcp-config",
		"--disable-slash-commands",
		"--no-session-persistence",
	}
}

// grokArgs are the flags that make a one-shot, non-interactive Grok call.
//
// A positional prompt opens the TUI; the prompt itself is passed with
// `--prompt-file` because a hub-generation prompt does not fit on argv
// (`argument list too long`, measured). `--output-format
// streaming-messages-json` is the Claude-shaped event stream this provider
// already knows how to watch, so a long generation is observed rather than
// bounded by a wall clock.
//
// # Why the tool flags look like this
//
// `--tools ""` was here, on the assumption it meant "allow no tools". It does
// not. Measured against grok 1.0.24 by reading the `system/init` event, which
// lists the resolved tool set before any API call is made:
//
//	(no tool flags)                 27 tools
//	--tools ""                      27 tools  ← the empty value is IGNORED
//	--tools none / --tools __none__ 27 tools  ← an unknown name is ignored too
//	--tools read_file                3 tools  (read_file + search_tool, use_tool)
//	--tools search_tool              2 tools  (search_tool, use_tool always survive
//	                                           an allowlist)
//	--tools search_tool \
//	  --disallowed-tools search_tool,use_tool  0 tools
//
// So the CLI believed it was asking for a text-only generation while handing
// the model `write`, `run_terminal_command` and `spawn_subagent`.
//
// That the tool set was wrong is measured. That it CAUSED the tenant-planning
// hang — eleven stream events, a last phase of `user`, then three minutes of
// silence and an idle abort — is the best-supported explanation and not an
// observation: a `user` phase is a tool RESULT being fed back, which is a shape
// only a tooled run can produce. It has not been reproduced end to end, because
// the measuring account answers HTTP 402 before a turn completes. Removing the
// tools is worth doing on the measurement alone; do not read the causal claim
// as settled until a run with balance confirms it.
//
// The allowlist alone cannot reach zero — `search_tool` and `use_tool` survive
// it — so the subtraction is the second half of the pair, not redundancy.
//
// `--permission-mode dontAsk` is belt and braces for the same failure. This
// machine has `permission_mode = "always-approve"` in ~/.grok/config.toml, so
// a tool call here EXECUTES; on a machine without that, the default mode
// blocks on stdin for an approval that headless mode can never deliver — the
// same silence, from the opposite cause. Pinning the mode makes the behaviour
// independent of whatever config the operator happens to have.
//
// WL_AI_ARGS overrides the list entirely.
func grokArgs() []string {
	if custom := splitArgs(os.Getenv("WL_AI_ARGS")); len(custom) > 0 {
		return custom
	}
	return []string{
		"--output-format", "streaming-messages-json",
		"--no-subagents",
		"--disable-web-search",
		"--no-plan",
		"--verbatim",
		// Together these resolve to zero tools. Neither does it alone.
		"--tools", "search_tool",
		"--disallowed-tools", "search_tool,use_tool",
		"--permission-mode", "dontAsk",
	}
}

func configuredCLIPath(binary string) string {
	if c := strings.TrimSpace(os.Getenv("WL_AI_COMMAND")); c != "" {
		return c
	}
	return os.Getenv(strings.ToUpper(binary) + "_BIN")
}

func missingLocalCLI(name, configured string, searched []string) error {
	if strings.TrimSpace(configured) != "" {
		return fmt.Errorf("WL_AI_COMMAND is set to %q, which is not an executable file", configured)
	}
	return fmt.Errorf("%s is not installed, or is not where this process can see it.\n"+
		"Looked on PATH and in:\n  %s\n\n"+
		"Install it, or set WL_AI_COMMAND to its full path.",
		name, strings.Join(searched, "\n  "))
}

// newLocalCLIProvider builds a subprocess provider for a named tool.
//
// `claude-code` and `grok` are verified presets. Anything else is driven
// entirely from configuration, because inventing another tool's flags would
// produce a provider that looks supported and fails on first use.
func newLocalCLIProvider(kind, model string) (Provider, error) {
	timeout := parseTimeoutEnv(os.Getenv("WL_AI_TIMEOUT"))
	idle := parseTimeoutEnv(os.Getenv("WL_AI_IDLE_TIMEOUT"))

	switch kind {
	case "claude-code", "claude-local":
		configured := configuredCLIPath("claude")
		bin, searched := ResolveLocalCLI("claude", configured)
		if bin == "" {
			return nil, missingLocalCLI("claude code", configured, searched)
		}
		return &LocalCLIProvider{
			Bin: bin, Name: "claude code", Args: claudeCodeArgs(),
			Model: model, JSON: true, Timeout: timeout,
			// Streamed, so liveness is observed. WL_AI_TIMEOUT is still read
			// and still bounds the non-streaming path, but it no longer decides
			// whether a working call is allowed to finish — see
			// localcli_stream.go on why a total deadline was the wrong control.
			Stream: true, IdleTimeout: idle,
			// So a console can say whether the model is alive rather than
			// showing a spinner and hoping. See observe.go.
			OnActivity: observeActivity,
		}, nil

	case "grok":
		configured := configuredCLIPath("grok")
		bin, searched := ResolveLocalCLI("grok", configured)
		if bin == "" {
			return nil, missingLocalCLI("grok", configured, searched)
		}
		return &LocalCLIProvider{
			Bin: bin, Name: "grok", Args: grokArgs(),
			Model: model, JSON: true, Timeout: timeout,
			// --prompt-file, not --single: the planning prompt is the whole
			// blueprint corpus, which does not fit on argv.
			PromptFileFlag: "--prompt-file", NativeStream: true,
			Stream: true, IdleTimeout: idle,
			OnActivity: observeActivity,
		}, nil

	case "codex":
		configured := configuredCLIPath("codex")
		bin, searched := ResolveLocalCLI("codex", configured)
		if bin == "" {
			return nil, missingLocalCLI("codex", configured, searched)
		}
		codex := &LocalCLIProvider{
			Bin: bin, Name: "codex", Args: codexArgs(),
			Model: model, JSON: false, Timeout: timeout,
		}
		// Only alongside the built-in flags. WL_AI_ARGS is documented as
		// overriding the list ENTIRELY, and pinning --output-last-message
		// outside that override broke the promise in the most confusing
		// possible way: the flag was appended to an operator's own arguments,
		// and the error when no answer file appeared told them to fix it with
		// WL_AI_ARGS — the very thing that could not reach it.
		if strings.TrimSpace(os.Getenv("WL_AI_ARGS")) == "" {
			// Not stdout — stdout is a transcript. See codexArgs.
			codex.OutputFileFlag = "--output-last-message"
			// And the prompt on stdin, because it does not fit on argv.
			codex.PromptStdin, codex.PromptArg = true, "-"
		}
		return codex, nil

	case "local-cli":
		command := strings.TrimSpace(os.Getenv("WL_AI_COMMAND"))
		if command == "" {
			return nil, fmt.Errorf("WL_AI_COMMAND is required for WL_AI_PROVIDER=local-cli — " +
				"it is the CLI to run, e.g. /usr/local/bin/mytool")
		}
		bin, searched := ResolveLocalCLI(filepath.Base(command), command)
		if bin == "" {
			return nil, fmt.Errorf("%q not found. Looked on PATH and in:\n  %s", command, strings.Join(searched, "\n  "))
		}
		return &LocalCLIProvider{
			Bin: bin, Name: filepath.Base(bin), Args: splitArgs(os.Getenv("WL_AI_ARGS")),
			Model: model, JSON: os.Getenv("WL_AI_JSON") == "1", Timeout: timeout,
		}, nil
	}
	return nil, fmt.Errorf("unknown local CLI provider %q", kind)
}

// splitArgs splits a whitespace-separated argument string, honouring simple
// double quotes so a flag value containing a space can be passed.
func splitArgs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t') && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// parseTimeoutEnv reads a Go duration, falling back to the default on anything
// unparseable rather than failing the command over a malformed setting.
func parseTimeoutEnv(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// LocalCLIAvailable reports whether a local provider could run right now.
// Used by `weblisk doctor` and by any caller that wants to offer it only when
// it will work.
func LocalCLIAvailable(kind string) bool {
	name := "claude"
	switch normaliseKind(ProviderKind(kind)) {
	case ProviderGrok:
		name = "grok"
	case ProviderCodex:
		name = "codex"
	case ProviderLocalCLI:
		name = strings.TrimSpace(os.Getenv("WL_AI_COMMAND"))
		if name == "" {
			return false
		}
		name = filepath.Base(name)
	}
	bin, _ := ResolveLocalCLI(name, configuredCLIPath(name))
	if bin == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, bin, "--version").Run() == nil
}

// ModelUsed reports the model the tool said it used, once it has been asked
// something. Empty before the first call.
func (p *LocalCLIProvider) ModelUsed() string {
	p.observedMu.Lock()
	defer p.observedMu.Unlock()
	return p.observed
}
