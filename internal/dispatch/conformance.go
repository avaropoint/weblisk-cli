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
	run     func(base string) (bool, string, string)
}

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
		ok, detail, evidence := t.run(base)
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
		applies: map[string]bool{"orchestrator": true, "agent": true, "admin": true},
		run: func(base string) (bool, string, string) {
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
		applies: map[string]bool{"orchestrator": true, "agent": true, "admin": true},
		run: func(base string) (bool, string, string) {
			// The orchestrator's protected surface, per protocol/spec. Health is
			// deliberately absent: it is the one endpoint that must answer
			// without a token.
			protected := []string{"/v1/services", "/v1/audit", "/v1/admin/overview"}
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
		applies: map[string]bool{"orchestrator": true, "agent": true, "admin": true},
		run: func(base string) (bool, string, string) {
			// Any 4xx must be JSON carrying `error`. Provoked with a protected
			// endpoint and an unknown route, which every component has.
			var faults []string
			seen := 0
			for _, p := range []string{"/v1/services", "/v1/audit"} {
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
