package dispatch

// Running the thing, which is the only way to find out whether it runs.
//
// # The gap this closes
//
// Generation had three gates: the response is a file, it compiles, and the
// model verifies it against the blueprints' assertions. All three passed on a
// hub that died two seconds into startup:
//
//	orchestrator: startup: reserve namespace "system": namespace "system" is reserved
//
// `system.*` is reserved FROM AGENTS; the orchestrator owns it and reserves it
// at startup, and the guard rejected its owner. Two blueprint requirements
// implemented in two files without reconciling — invisible to a compiler,
// invisible to a reader of either file, and obvious the instant the binary runs.
//
// architecture/testing specifies this as L1: twenty named tests, black-box over
// HTTP, "implementations in any language can run the same test suite". It has
// never been executed, which is why L1-01 asserted a health shape no blueprint
// defines.
//
// # Scope
//
// L1, and only the tests that apply to the component being built. Four of the
// ten are agent-only — describe, execute, message, service-directory-update —
// and running them against an orchestrator reports a correct implementation as
// broken.
//
// Tests needing a signed registration (L1-03/04/05) need an ML-DSA-65 key and
// the mock agent architecture/testing specifies. Not built yet, and stated as
// unrun rather than skipped quietly.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ConformanceResult is one test's outcome.
type ConformanceResult struct {
	ID       string
	Name     string
	Passed   bool
	Unrun    bool   // no harness for it yet — never counted as a pass
	Detail   string // what was found, when it failed
	Evidence string // what was seen, when it passed
}

// conformanceTest is one L1 test against a running component.
type conformanceTest struct {
	id      string
	name    string
	applies map[string]bool // component kinds this test addresses
	run     func(base, component string) (bool, string, string)
}

// protectedPaths are the endpoints a component serves that MUST refuse an
// unauthenticated request.
//
// Per component, because the list is not a property of the protocol — it is a
// property of what this component routes. Probing the orchestrator's surface
// against a content service reports "no protected endpoint answered at all",
// which is a correct implementation failing a test that asked the wrong
// question.
func protectedPaths(component string) []string {
	switch component {
	case "content":
		return []string{"/v1/content", "/v1/content/repositories", "/v1/content/stat"}
	case "agent":
		return []string{"/v1/services", "/v1/execute", "/v1/message"}
	}
	return []string{"/v1/services", "/v1/audit", "/v1/admin/overview"}
}

// reCapabilityBullet matches a standard-capability bullet in protocol/types.md,
// so the capability vocabulary is read from the blueprint rather than copied
// into the tooling:
//
//   - `content:read` — read entries and list a content repository
var reCapabilityBullet = regexp.MustCompile("(?m)^-\\s+`([a-z][a-z0-9]*:[a-z*][a-z0-9-]*)`")

// startupTimeout bounds how long a component may take to answer.
//
// Generous for a cold start that generates a key pair; finite because a
// component that never listens has failed, and waiting forever reports that as
// nothing at all.
const startupTimeout = 20 * time.Second

// RunConformance builds nothing — it starts an already-built binary and
// exercises it.
//
// The binary is started on a port nobody else holds, probed, and killed. Its
// output is captured, because a component that fails to start says why on
// stderr and that sentence is the whole finding.
func RunConformance(root, binary, component string, onProgress ProgressFunc) ([]ConformanceResult, string, error) {
	if onProgress == nil {
		onProgress = func(Progress) {}
	}
	port, err := freePort()
	if err != nil {
		return nil, "", err
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	// WL_DEV so a fresh component needs no operator, no passphrase and no
	// persistent store. protocol/identity permits it and requires it to be
	// reported; platforms/go requires the warning. That is the mode a
	// conformance run wants: no state, no prompts, and it says so out loud.
	cmd := exec.Command(binary, "--port", fmt.Sprintf("%d", port))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "WL_DEV=1", fmt.Sprintf("WL_PORT=%d", port))
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("starting %s: %w", binary, err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}()

	onProgress(Progress{Path: "conformance", Status: "starting", Detail: base})
	if err := waitForListen(cmd, base, startupTimeout); err != nil {
		// The component never answered. Its own output is the finding — a
		// startup failure explains itself in one line, and any test result here
		// would be a guess dressed as a measurement.
		//
		// The error is enriched with what the component actually said, because
		// "exit status 1" on its own is not a finding.
		if fl := failureLines(out.String()); len(fl) > 0 {
			err = fmt.Errorf("%v: %s", err, firstLine(fl))
		}
		return nil, out.String(), err
	}

	var results []ConformanceResult
	for _, t := range l1Tests {
		if !t.applies[component] {
			continue
		}
		if t.run == nil {
			results = append(results, ConformanceResult{ID: t.id, Name: t.name, Unrun: true,
				Detail: "no harness yet — needs a signed registration and the mock agent"})
			continue
		}
		ok, detail, evidence := t.run(base, component)
		results = append(results, ConformanceResult{ID: t.id, Name: t.name,
			Passed: ok, Detail: detail, Evidence: evidence})
		onProgress(Progress{Path: t.id, Status: map[bool]string{true: "passed", false: "failed"}[ok],
			Detail: t.name})
	}
	return results, out.String(), nil
}

// waitForListen polls until the component accepts a connection, or dies.
//
// A process that exits is detected rather than waited out: the common startup
// failure is immediate, and twenty seconds of polling a dead process reports a
// timeout when the answer was on stderr in the first hundred milliseconds.
func waitForListen(cmd *exec.Cmd, base string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			return fmt.Errorf("the component exited during startup: %v", err)
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", base+"/v1/health", nil)
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("the component did not answer on %s within %s", base, limit)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func get(base, path string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, nil
}

// l1Tests are the Level 1 conformance tests from architecture/testing.
//
// Each names the components it applies to, because four of the ten address an
// agent and running those against an orchestrator reports a correct
// implementation as broken.
var l1Tests = []conformanceTest{
	{
		id: "L1-01", name: "Health Check",
		applies: map[string]bool{"orchestrator": true, "agent": true, "admin": true, "content": true},
		run: func(base, component string) (bool, string, string) {
			code, body, err := get(base, "/v1/health")
			if err != nil {
				return false, "no response: " + err.Error(), ""
			}
			if code != 200 {
				return false, fmt.Sprintf("status %d, want 200", code), ""
			}
			var h map[string]any
			if json.Unmarshal(body, &h) != nil {
				return false, "response is not JSON", ""
			}
			// HealthStatus, per protocol/types — not the shape this test used to
			// assert, which no blueprint defines.
			var missing []string
			for _, f := range []string{"name", "status", "version", "uptime", "timestamp"} {
				if _, ok := h[f]; !ok {
					missing = append(missing, f)
				}
			}
			if len(missing) > 0 {
				return false, "HealthStatus is missing " + strings.Join(missing, ", "), ""
			}
			status, _ := h["status"].(string)
			switch status {
			case "healthy", "degraded", "unhealthy":
			default:
				return false, fmt.Sprintf("status is %q; protocol/types allows healthy, degraded, unhealthy", status), ""
			}
			return true, "", fmt.Sprintf("200, status=%q, all HealthStatus fields present", status)
		},
	},
	{
		id: "L1-07", name: "Protected Endpoints Require Auth",
		applies: map[string]bool{"orchestrator": true, "agent": true, "admin": true, "content": true},
		run: func(base, component string) (bool, string, string) {
			// This component's protected surface. Health is deliberately absent:
			// it is the one endpoint that must answer without a token.
			protected := protectedPaths(component)
			var wrong []string
			checked := 0
			for _, p := range protected {
				code, _, err := get(base, p)
				if err != nil {
					continue // not routed on this component; L1-10 covers shape
				}
				checked++
				if code == 404 {
					continue // this component does not serve it
				}
				if code != 401 && code != 403 {
					wrong = append(wrong, fmt.Sprintf("%s answered %d without a token", p, code))
				}
			}
			if checked == 0 {
				return false, "no protected endpoint answered at all", ""
			}
			if len(wrong) > 0 {
				return false, strings.Join(wrong, "; "), ""
			}
			return true, "", fmt.Sprintf("%d protected endpoints refuse an unauthenticated request", checked)
		},
	},
	{
		id: "L1-10", name: "Error Response Format",
		applies: map[string]bool{"orchestrator": true, "agent": true, "admin": true, "content": true},
		run: func(base, component string) (bool, string, string) {
			// Any 4xx must be JSON carrying `error`. Provoked with a protected
			// endpoint and an unknown route, which every component has.
			var faults []string
			seen := 0
			for _, p := range protectedPaths(component) {
				code, body, err := get(base, p)
				if err != nil || code < 400 || code >= 600 {
					continue
				}
				seen++
				var e map[string]any
				if json.Unmarshal(body, &e) != nil {
					faults = append(faults, fmt.Sprintf("%s returned %d with a non-JSON body", p, code))
					continue
				}
				if _, ok := e["error"].(string); !ok {
					faults = append(faults, fmt.Sprintf("%s returned %d with no string `error` field", p, code))
				}
			}
			if seen == 0 {
				return false, "no error response could be provoked", ""
			}
			if len(faults) > 0 {
				return false, strings.Join(faults, "; "), ""
			}
			return true, "", fmt.Sprintf("%d error responses carry a JSON `error` field", seen)
		},
	},
	// Registration tests need an ML-DSA-65 signing key and the mock agent
	// architecture/testing specifies. Declared so they are reported unrun rather
	// than quietly absent — an assertion nobody checked is not one that passed.
	{id: "L1-03", name: "Registration", applies: map[string]bool{"orchestrator": true}},
	{id: "L1-04", name: "Registration Rejects Bad Signature", applies: map[string]bool{"orchestrator": true}},
	{id: "L1-05", name: "Registration Rejects Stale Timestamp", applies: map[string]bool{"orchestrator": true}},
}

// ConformanceSummary counts outcomes. Unrun is separate from failed, and from
// passed: no harness exists for it, which is a gap in the runner rather than a
// finding about the component.
func ConformanceSummary(rs []ConformanceResult) (passed, failed, unrun int) {
	for _, r := range rs {
		switch {
		case r.Unrun:
			unrun++
		case r.Passed:
			passed++
		default:
			failed++
		}
	}
	return
}

// FailedConformance returns the tests that ran and did not pass.
func FailedConformance(rs []ConformanceResult) []ConformanceResult {
	var out []ConformanceResult
	for _, r := range rs {
		if !r.Unrun && !r.Passed {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// reQuotedRun matches a quoted value inside a rendered error message.
var reQuotedRun = regexp.MustCompile(`"[^"]*"`)

// failureLines reduces captured output to the lines that are actually a failure.
//
// # Why this is not just "every line"
//
// A component logs while starting. This is normal dev-mode output:
//
//	{"level":"warn","event":"security.key_unencrypted","msg":"private key is
//	 protected by file permissions alone — development only"}
//
// Treated as the failure, its fragments — "component_type":"library",
// "log_type":"security" — match every file that emits a structured log. One run
// made thirty-three repair calls off that line and rewrote seven innocent files
// per round. It converged by brute force, which is not the same as working.
//
// A component marks its own severity, so use it: a JSON line at info, warn or
// debug is context. What counts is a line the component called an error, or a
// line that is not structured logging at all — a panic, a fatal, the sentence a
// process prints as it dies.
func failureLines(output string) []string {
	var out []string
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if len(line) < 16 {
			continue
		}
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil {
			// Structured log. Only an error-level entry is a failure, and then
			// it is the message that matters, not the envelope.
			level, _ := entry["level"].(string)
			switch strings.ToLower(level) {
			case "error", "fatal", "panic":
			default:
				continue
			}
			for _, field := range []string{"msg", "message", "error", "err"} {
				if v, ok := entry[field].(string); ok && len(v) >= 12 {
					out = append(out, v)
				}
			}
			continue
		}
		// Not structured: a panic, a fatal, or the sentence a process prints as
		// it exits. Those are the plain-text failures worth matching on.
		//
		// Except a line that marks its OWN severity as informational. The dev
		// storage warning —
		//
		//	[dev] using in-memory storage — data will not survive restart
		//
		// is required by platforms/go, printed by every component under WL_DEV=1,
		// and matched five files. So a build that had already succeeded was told
		// to repair five working files against a line the blueprints mandate,
		// while the actual failure — an empty orchestrator public key — sat two
		// lines below it, unread.
		//
		// The structured branch above already respects level. This is the same
		// rule for a component that marks its level in a prefix instead.
		if isInformationalPrefix(line) {
			continue
		}
		out = append(out, line)
	}
	return out
}

// isInformationalPrefix reports whether a plain-text line marks itself as
// something other than a failure.
//
// Only a leading bracketed tag counts. A line is not excused for containing the
// word "warning" somewhere — "startup failed: warning threshold exceeded" is a
// failure — and a component that tags its own output has told us the severity
// more reliably than any keyword search of the sentence would.
func isInformationalPrefix(line string) bool {
	if !strings.HasPrefix(line, "[") {
		return false
	}
	end := strings.Index(line, "]")
	if end < 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line[1:end])) {
	case "dev", "info", "warn", "warning", "note", "ok", "debug", "trace":
		return true
	}
	return false
}

// FilesBehindRuntimeFailure finds the generated files a startup failure is about.
//
// A runtime failure names no file. This one —
//
//	orchestrator: startup: reserve namespace "system": namespace "system" is reserved
//
// is a sentence assembled from format strings in two different files: server.go
// asks for the namespace and namespaces.go refuses it. The fix could belong to
// either, so a repair shown only one would be guessing.
//
// # Turning a rendered message back into its format string
//
// Searching the source for the rendered text finds nothing, because the source
// holds `namespace %q is reserved` and the output holds `namespace "system" is
// reserved`. So each quoted run is substituted with the verbs that could have
// produced it — %q, %s, %v, %d — and the candidates are matched literally. A
// file either contains that format string or it does not.
func FilesBehindRuntimeFailure(output string, content map[string]string) []string {
	var candidates []string
	for _, line := range failureLines(output) {
		// Each colon-separated clause is usually one Errorf. Wrapping means one
		// line carries several, and the clause is the unit that matches.
		for _, clause := range strings.Split(line, ": ") {
			clause = strings.TrimSpace(clause)
			if len(clause) < 12 {
				continue
			}
			if !reQuotedRun.MatchString(clause) {
				candidates = append(candidates, clause)
				continue
			}
			for _, verb := range []string{"%q", "%s", "%v", "%d"} {
				candidates = append(candidates, reQuotedRun.ReplaceAllString(clause, verb))
			}
			// And the literal parts either side of the quotes, which survive any
			// verb choice.
			for _, part := range reQuotedRun.Split(clause, -1) {
				if part = strings.TrimSpace(part); len(part) >= 12 {
					candidates = append(candidates, part)
				}
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	var out []string
	for path, body := range content {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		for _, c := range candidates {
			if strings.Contains(body, c) {
				if !containsString(out, path) {
					out = append(out, path)
				}
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// runtimeRepairPrompt asks for a file back, given what happened when the
// component was started.
func runtimeRepairPrompt(f PlannedFile, current, startupOutput, platBP, module string) string {
	var b strings.Builder
	b.WriteString("The component compiles but does not run. It was started and failed.\\n\\n")
	b.WriteString("What it printed:\\n\\n")
	b.WriteString(indentBlock(startupOutput, "    "))
	b.WriteString("\\n\\nThis is a logic fault, not a compile error — two requirements " +
		"implemented in two places without agreeing. Read the message and the " +
		"blueprints, and correct whichever side of the contradiction is wrong.\\n\\n")
	fmt.Fprintf(&b, "File: %s\\nPurpose: %s\\n", f.Path, f.Purpose)
	if module != "" {
		fmt.Fprintf(&b, "Module path: %s\\n", module)
	}
	if keep := currentDeclarations(f.Path, current); keep != "" {
		b.WriteString("\\nThis file currently declares the following, and other files call " +
			"them. Keep every one:\\n")
		b.WriteString(keep)
	}
	b.WriteString("\\nCurrent content:\\n\\n")
	b.WriteString(current)
	b.WriteString("\\n\\nReturn the corrected file and nothing else.\\n")
	if platBP != "" {
		b.WriteString("\\nPlatform blueprint:\\n\\n")
		b.WriteString(platBP)
	}
	return b.String()
}
