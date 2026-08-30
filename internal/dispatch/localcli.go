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
	"time"
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
	searched := make([]string, 0, len(localCLIInstallDirs))
	for _, dir := range localCLIInstallDirs {
		full := dir
		if strings.HasPrefix(dir, "~/") && home != "" {
			full = filepath.Join(home, dir[2:])
		}
		candidate := filepath.Join(full, name)
		searched = append(searched, candidate)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", searched
}

// LocalCLIProvider drives a coding-agent CLI as a subprocess.
type LocalCLIProvider struct {
	Bin     string        // resolved binary path
	Name    string        // provider name, for errors
	Args    []string      // flags before the prompt
	Model   string        // "" leaves the tool's default
	JSON    bool          // parse stdout as a result envelope
	Timeout time.Duration // 0 means defaultLocalCLITimeout
	Dir     string        // working directory, "" means inherit
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
	args = append(args, flattenMessages(messages))

	cmd := exec.CommandContext(ctx, p.Bin, args...)
	if p.Dir != "" {
		cmd.Dir = p.Dir
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("%s timed out after %s — the model may still be working; "+
				"raise WL_AI_TIMEOUT if generation legitimately takes longer", p.Name, timeout)
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("%s failed: %s", p.Name, detail)
	}

	raw := strings.TrimSpace(stdout.String())
	if raw == "" {
		return "", fmt.Errorf("%s returned no output", p.Name)
	}
	if !p.JSON {
		return raw, nil
	}

	var res claudeCodeResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		// Not the envelope we expected. The output is still a completion, and
		// refusing it because the wrapper changed shape would be worse than
		// using it.
		return raw, nil
	}
	if res.IsError {
		return "", fmt.Errorf("%s reported an error: %s", p.Name, res.Result)
	}
	return res.Result, nil
}

// claudeCodeArgs are the flags that make a one-shot, non-interactive call.
//
// The built-in tool set, the user's MCP servers, the slash-command catalogue and
// session persistence are all switched off: this is a single generation from a
// prompt, and every one of those would either change the output or reach for
// state that has nothing to do with the hub being generated.
func claudeCodeArgs() []string {
	return []string{
		"--print", "--output-format", "json",
		"--tools", "",
		"--strict-mcp-config",
		"--disable-slash-commands",
		"--no-session-persistence",
	}
}

// newLocalCLIProvider builds a subprocess provider for a named tool.
//
// `claude-code` is a verified preset. Anything else is driven entirely from
// configuration, because inventing another tool's flags would produce a provider
// that looks supported and fails on first use.
func newLocalCLIProvider(kind, model string) (Provider, error) {
	timeout := parseTimeoutEnv(os.Getenv("WL_AI_TIMEOUT"))

	switch kind {
	case "claude-code", "claude-local":
		configured := os.Getenv("WL_AI_COMMAND")
		bin, searched := ResolveLocalCLI("claude", configured)
		if bin == "" {
			if strings.TrimSpace(configured) != "" {
				return nil, fmt.Errorf("WL_AI_COMMAND is set to %q, which is not an executable file", configured)
			}
			return nil, fmt.Errorf("claude code is not installed, or is not where this process can see it.\n"+
				"Looked on PATH and in:\n  %s\n\n"+
				"Install it, or set WL_AI_COMMAND to its full path.",
				strings.Join(searched, "\n  "))
		}
		return &LocalCLIProvider{
			Bin: bin, Name: "claude code", Args: claudeCodeArgs(),
			Model: model, JSON: true, Timeout: timeout,
		}, nil

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
	if kind == "local-cli" {
		name = strings.TrimSpace(os.Getenv("WL_AI_COMMAND"))
		if name == "" {
			return false
		}
		name = filepath.Base(name)
	}
	bin, _ := ResolveLocalCLI(name, os.Getenv("WL_AI_COMMAND"))
	if bin == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, bin, "--version").Run() == nil
}
