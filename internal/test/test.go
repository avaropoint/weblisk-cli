package test

// Test commands — protocol conformance tests and mock orchestrator.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/avaropoint/weblisk-cli/internal/operator"
)

// Handle dispatches test subcommands.
func Handle(args []string) error {
	if len(args) == 0 {
		PrintHelp()
		return nil
	}

	switch args[0] {
	case "conformance":
		return handleConformance(args[1:])
	case "mock-orchestrator":
		return handleMockOrchestrator(args[1:])
	case "help", "--help", "-h":
		PrintHelp()
		return nil
	default:
		return fmt.Errorf("unknown test command: %s\n  Try: weblisk test conformance|mock-orchestrator", args[0])
	}
}

func handleConformance(args []string) error {
	orchURL := ""
	level := 0
	testID := ""
	verbose := false
	jsonOut := false

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--orch" && i+1 < len(args):
			i++
			orchURL = args[i]
		case strings.HasPrefix(args[i], "--orch="):
			orchURL = strings.SplitN(args[i], "=", 2)[1]
		case args[i] == "--level" && i+1 < len(args):
			i++
			fmt.Sscanf(args[i], "%d", &level)
		case strings.HasPrefix(args[i], "--level="):
			fmt.Sscanf(strings.SplitN(args[i], "=", 2)[1], "%d", &level)
		case args[i] == "--test" && i+1 < len(args):
			i++
			testID = args[i]
		case strings.HasPrefix(args[i], "--test="):
			testID = strings.SplitN(args[i], "=", 2)[1]
		case args[i] == "--verbose":
			verbose = true
		case args[i] == "--json":
			jsonOut = true
		}
	}

	if orchURL == "" {
		orchURL = os.Getenv("WL_ORCH")
	}
	if orchURL == "" {
		orchURL = "http://localhost:9800"
	}

	fmt.Printf("Running conformance suite against %s...\n\n", orchURL)

	type testCase struct {
		ID    string
		Level int
		Name  string
	}

	tests := []testCase{
		{"L1-01", 1, "POST /v1/register accepts valid manifest"},
		{"L1-02", 1, "POST /v1/register rejects unsigned request"},
		{"L1-03", 1, "agent_id is 32 hex chars"},
		{"L1-04", 1, "POST /v1/health returns status"},
		{"L1-05", 1, "GET /v1/services returns service directory"},
		{"L1-06", 1, "WLT token includes required claims"},
		{"L1-07", 1, "Expired token is rejected"},
		{"L1-08", 1, "Invalid signature is rejected"},
		{"L1-09", 1, "GET /v1/admin/overview requires auth"},
		{"L1-10", 1, "Capability scoping is enforced"},
		{"L1-11", 1, "Rate limiting headers are present"},
		{"L1-12", 1, "Health endpoint returns structured response"},
		{"L2-01", 2, "Events delivered to scoped subscribers"},
		{"L2-02", 2, "Task assignment respects capability scope"},
		{"L2-03", 2, "Observation creates audit trail entry"},
		{"L2-04", 2, "Recommendation links to strategy"},
		{"L2-05", 2, "Agent deregistration cascades cleanly"},
		{"L2-06", 2, "Workflow phases execute in order"},
		{"L2-07", 2, "Failed phase triggers rollback handler"},
		{"L2-08", 2, "Behavioral fingerprint change triggers alert"},
		{"L3-01", 3, "Full workflow execution end-to-end"},
		{"L3-02", 3, "Strategy progress tracking accumulates"},
		{"L3-03", 3, "Federation peering handshake completes"},
		{"L3-04", 3, "Data contract enforcement blocks violations"},
	}

	client := &http.Client{Timeout: 10 * time.Second}

	// Verify connectivity
	_, err := client.Get(orchURL + "/v1/health")
	if err != nil {
		return fmt.Errorf("connection failed: cannot reach %s\n  Ensure the orchestrator is running", orchURL)
	}

	// The operator's token, when there is one. Loaded rather than referenced:
	// this was `_ = operator.LoadToken`, a no-op written to keep an import,
	// which meant every check ran unauthenticated and no assertion that needs a
	// credential could ever be written.
	token, _, tokenErr := operator.LoadToken()
	if tokenErr != nil && !jsonOut {
		fmt.Printf("  note: no operator token (%v)\n"+
			"        Checks that need one are reported as not-checked, never as passed.\n\n", tokenErr)
	}

	passedN := 0
	failedN := 0
	notCheckedN := 0
	notSelected := 0
	currentLevel := 0

	type result struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail,omitempty"`
	}
	var results []result

	for _, tc := range tests {
		// Not selected is a different fact from not checked: the operator asked
		// for one level or one id. Counted apart so "20 not checked" cannot hide
		// inside a number that also means "you asked for level 1".
		if level > 0 && tc.Level != level {
			notSelected++
			continue
		}
		if testID != "" && tc.ID != testID {
			notSelected++
			continue
		}

		if tc.Level != currentLevel {
			currentLevel = tc.Level
			if !jsonOut {
				names := []string{"", "Protocol Basics", "Behavior", "Integration"}
				fmt.Printf("Level %d — %s\n", currentLevel, names[currentLevel])
			}
		}

		r := runConformanceTest(client, orchURL, tc.ID, token, verbose)

		switch r.outcome {
		case outcomePassed:
			passedN++
			if !jsonOut {
				fmt.Printf("  ✓ %-6s %s\n", tc.ID, tc.Name)
			}
			results = append(results, result{tc.ID, tc.Name, "passed", r.detail})
		case outcomeFailed:
			failedN++
			if !jsonOut {
				fmt.Printf("  ✗ %-6s %s\n      %s\n", tc.ID, tc.Name, r.detail)
			}
			results = append(results, result{tc.ID, tc.Name, "failed", r.detail})
		default:
			notCheckedN++
			if !jsonOut {
				// A middle dot, not a tick. Whatever this line is, it is not
				// evidence that the hub is conformant.
				fmt.Printf("  · %-6s %s\n      not checked — %s\n", tc.ID, tc.Name, r.detail)
			}
			results = append(results, result{tc.ID, tc.Name, "not-checked", r.detail})
		}
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{
			"passed": passedN,
			"failed": failedN,
			// Named so a reader cannot mistake it for a pass, and kept apart
			// from not_selected, which is the operator's own --level/--test.
			"not_checked":  notCheckedN,
			"not_selected": notSelected,
			"conformant":   failedN == 0 && notCheckedN == 0,
			"results":      results,
		})
	} else {
		fmt.Printf("\nRan %d: %d passed, %d failed, %d not checked",
			passedN+failedN+notCheckedN, passedN, failedN, notCheckedN)
		if notSelected > 0 {
			fmt.Printf(" (%d not selected)", notSelected)
		}
		fmt.Println()
		if notCheckedN > 0 {
			// Said outright. A suite that reports "4 passed" and stops has told
			// an operator their hub is conformant when it has established
			// nothing of the kind.
			fmt.Printf("\n  %d assertion(s) were NOT checked, so this run does not establish\n"+
				"  conformance. Each line above says what it would need.\n", notCheckedN)
		}
	}

	if failedN > 0 {
		return fmt.Errorf("%d test(s) failed", failedN)
	}
	return nil
}

// outcome is how a conformance assertion was settled.
//
// Three states, not two, and the third is the whole point. This returned a
// bool, and every assertion without a check reached a `default: return true`
// marked "pass by default until full test harness is implemented" — so 20 of
// the 24 declared tests printed a tick without contacting the orchestrator at
// all, and the run reported 24/24 against a hub that had answered one request.
//
// A suite that cannot tell "I checked this and it holds" from "I did not check
// this" is worse than one that checks nothing, because the second is honest.
// The same distinction is drawn in internal/dispatch/verify.go, for the same
// reason, under the name OutcomeInconclusive.
type outcome int

const (
	outcomePassed outcome = iota
	outcomeFailed
	outcomeNotChecked
)

// conformanceResult is one settled assertion, with the evidence.
type conformanceResult struct {
	outcome outcome
	// detail says what was observed, or — for outcomeNotChecked — what would
	// have to be built to settle it. Never empty for the last two.
	detail string
}

func passed(detail string) conformanceResult { return conformanceResult{outcomePassed, detail} }
func failed(detail string) conformanceResult { return conformanceResult{outcomeFailed, detail} }
func notChecked(reason string) conformanceResult {
	return conformanceResult{outcomeNotChecked, reason}
}

// healthStatusRequired are the fields protocol/types.md marks required: true on
// HealthStatus. `checks` and `metrics` are optional and are not demanded here.
var healthStatusRequired = []string{"name", "status", "version", "uptime", "timestamp"}

// validHealthStates are the three protocol/types.md names for HealthStatus.Status.
var validHealthStates = map[string]bool{"healthy": true, "degraded": true, "unhealthy": true}

func runConformanceTest(client *http.Client, orchURL, testID, token string, verbose bool) conformanceResult {
	switch testID {
	case "L1-02":
		// protocol/spec.md, POST /v1/register: the orchestrator MUST verify
		// `signature` against `manifest.public_key`, answering 401 for an
		// invalid signature and 400 for missing required fields. A request
		// carrying no signature at all must not be registered.
		body := `{"manifest":{"name":"conformance-probe","url":"http://localhost:1/","public_key":"not-a-key"},"timestamp":0}`
		resp, err := post(client, orchURL+"/v1/register", body, "", verbose)
		if err != nil {
			return failed("POST /v1/register: " + err.Error())
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return failed(fmt.Sprintf("an unsigned manifest was accepted (HTTP %d) — "+
				"spec requires 401 for an invalid signature, 400 for missing fields", resp.StatusCode))
		}
		return passed(fmt.Sprintf("rejected with HTTP %d", resp.StatusCode))

	case "L1-04":
		// POST, not GET. The assertion says POST /v1/health and the check did a
		// GET — and the spec has BOTH, meaning different things: POST /v1/health
		// is the agent's own health endpoint (spec.md line 213), GET /v1/health
		// is the orchestrator's (line 448). Checking the wrong one passed
		// against any hub that answered either.
		resp, err := post(client, orchURL+"/v1/health", "", token, verbose)
		if err != nil {
			return failed("POST /v1/health: " + err.Error())
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return failed(fmt.Sprintf("HTTP %d", resp.StatusCode))
		}
		var h map[string]any
		if json.NewDecoder(resp.Body).Decode(&h) != nil {
			return failed("200, but the body is not JSON")
		}
		if st, _ := h["status"].(string); st == "" {
			return failed("200, but no `status` field — spec requires one")
		}
		return passed("reports status")

	case "L1-05":
		resp, err := client.Get(orchURL + "/v1/services")
		if err != nil {
			return failed("GET /v1/services: " + err.Error())
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return failed(fmt.Sprintf("HTTP %d", resp.StatusCode))
		}
		return passed("HTTP 200")

	case "L1-09":
		// Deliberately unauthenticated: the assertion is that auth is REQUIRED,
		// so sending a token would test the opposite.
		resp, err := client.Get(orchURL + "/v1/admin/overview")
		if err != nil {
			return failed("GET /v1/admin/overview: " + err.Error())
		}
		defer resp.Body.Close()
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return passed(fmt.Sprintf("HTTP %d without a token", resp.StatusCode))
		}
		return failed(fmt.Sprintf("HTTP %d without a token — admin data is unprotected", resp.StatusCode))

	case "L1-12":
		// "Structured" is not "answered 200". protocol/types.md marks five
		// HealthStatus fields required, and names the three legal states.
		resp, err := client.Get(orchURL + "/v1/health")
		if err != nil {
			return failed("GET /v1/health: " + err.Error())
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return failed(fmt.Sprintf("HTTP %d", resp.StatusCode))
		}
		var h map[string]any
		if json.NewDecoder(resp.Body).Decode(&h) != nil {
			return failed("200, but the body is not JSON")
		}
		var missing []string
		for _, f := range healthStatusRequired {
			if _, ok := h[f]; !ok {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			return failed("HealthStatus is missing required field(s): " + strings.Join(missing, ", "))
		}
		if st, _ := h["status"].(string); !validHealthStates[st] {
			return failed(fmt.Sprintf("status is %q — spec allows healthy, degraded, unhealthy", st))
		}
		return passed("all required HealthStatus fields present")

	default:
		// NOT a pass. Each of these needs something this harness does not yet
		// have — a signed ML-DSA-65 manifest, a second agent to scope events
		// between, a workflow to run — and saying so is the honest report.
		return notChecked(notCheckedReason(testID))
	}
}

// notCheckedReason says what is actually missing, per assertion, so the list is
// a work queue rather than an apology.
func notCheckedReason(testID string) string {
	switch testID {
	case "L1-01", "L1-03":
		return "needs a signed ML-DSA-65 manifest; the harness cannot yet mint one"
	case "L1-06", "L1-07", "L1-08":
		return "needs WLT token minting, including an expired and a mis-signed one"
	case "L1-10":
		return "needs two agents with different capabilities to scope between"
	case "L1-11":
		return "needs enough requests to trip a rate limit"
	default:
		if strings.HasPrefix(testID, "L2-") {
			return "needs a registered agent and a live event/task exchange"
		}
		if strings.HasPrefix(testID, "L3-") {
			return "needs a full workflow, a peer hub, or a data contract in force"
		}
		return "no check is implemented"
	}
}

// post issues a POST, optionally authenticated, and echoes it when --verbose.
func post(client *http.Client, url, body, token string, verbose bool) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if verbose {
		fmt.Printf("      → POST %s %s\n", url, body)
	}
	resp, err := client.Do(req)
	if verbose && resp != nil {
		fmt.Printf("      ← HTTP %d\n", resp.StatusCode)
	}
	return resp, err
}

func handleMockOrchestrator(args []string) error {
	port := "19800"
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--port" && i+1 < len(args):
			i++
			port = args[i]
		case strings.HasPrefix(args[i], "--port="):
			port = strings.SplitN(args[i], "=", 2)[1]
		}
	}

	mux := http.NewServeMux()

	// In-memory registration store
	type registration struct {
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
		Time      string `json:"registered_at"`
	}
	var registrations []registration

	started := time.Now()
	// GET is the orchestrator's own health; POST is the agent health endpoint.
	// protocol/spec.md defines both under the same path and they are not the
	// same thing. The mock answers both, because the conformance suite asks for
	// both and a target that 404s one of them fails a hub for the mock's gap.
	//
	// The shape is HealthStatus from protocol/types.md, whose required fields
	// are name, status, version, uptime and timestamp. This used to send
	// status/version/uptime/protocol — no name, no timestamp, and uptime as the
	// STRING "0s" where the type says int64. It passed the old suite because
	// the old suite only checked for HTTP 200.
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"name":      "mock-orchestrator",
			"status":    "healthy",
			"version":   "mock-1.0.0",
			"uptime":    int64(time.Since(started).Seconds()),
			"timestamp": time.Now().Unix(),
			"metrics": map[string]any{
				"agents": len(registrations),
			},
		})
	})

	mux.HandleFunc("/v1/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		// protocol/spec.md, POST /v1/register: the orchestrator MUST verify the
		// signature over the manifest, answering 401 for an invalid one and 400
		// for missing required fields.
		//
		// The mock verified nothing and registered everything, which made it a
		// target that cannot fail the conformance suite's security assertions —
		// so a real hub with the same hole would look no different. It does not
		// do ML-DSA-65 here (it is a mock), but a request carrying NO signature
		// is refused, which is the assertion L1-02 actually makes.
		manifest, _ := body["manifest"].(map[string]any)
		if manifest == nil {
			// Older shape: fields at the top level.
			manifest = body
		}
		name, _ := manifest["name"].(string)
		pubKey, _ := manifest["public_key"].(string)
		if name == "" || pubKey == "" {
			http.Error(w, "missing required manifest fields", 400)
			return
		}
		if sig, _ := body["signature"].(string); strings.TrimSpace(sig) == "" {
			http.Error(w, "unsigned registration", 401)
			return
		}

		registrations = append(registrations, registration{
			Name:      name,
			PublicKey: pubKey,
			Time:      time.Now().Format(time.RFC3339),
		})

		json.NewEncoder(w).Encode(map[string]any{
			"agent_id": fmt.Sprintf("%032x", len(registrations)),
			"token":    "mock-token-" + name,
			"status":   "registered",
		})
	})

	mux.HandleFunc("/v1/services", func(w http.ResponseWriter, r *http.Request) {
		var agents []map[string]string
		for _, reg := range registrations {
			agents = append(agents, map[string]string{
				"name":   reg.Name,
				"status": "online",
			})
		}
		json.NewEncoder(w).Encode(map[string]any{"agents": agents})
	})

	mux.HandleFunc("/v1/admin/operators/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"token":   "mock-operator-token",
			"role":    "admin",
			"expires": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		})
	})

	mux.HandleFunc("/v1/admin/overview", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			http.Error(w, "unauthorized", 401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"agents_online":     len(registrations),
			"agents_degraded":   0,
			"agents_offline":    0,
			"domains_online":    0,
			"workflows_today":   0,
			"approvals_pending": 0,
			"federation_peers":  0,
			"health_score":      100,
		})
	})

	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return fmt.Errorf("cannot listen on port %s: %w", port, err)
	}

	fmt.Printf("Mock orchestrator running on http://localhost:%s\n", port)
	fmt.Println("  - Rejects unsigned or incomplete registrations (401/400)")
	fmt.Println("  - Accepts signed registrations")
	fmt.Println("  - Issues test tokens (24h TTL)")
	fmt.Println("  - Stores registrations in memory")
	fmt.Println("  Press Ctrl+C to stop.")

	return http.Serve(listener, mux)
}

// PrintHelp prints test command usage.
func PrintHelp() {
	fmt.Print(`
  Test Commands:
    weblisk test conformance        Run protocol conformance tests
      --orch <url>                  Orchestrator URL (default: http://localhost:9800)
      --level <n>                   Run specific level only (1, 2, or 3)
      --test <id>                   Run a single test by ID
      --verbose                     Show request/response details
      --json                        Machine-readable output
    weblisk test mock-orchestrator  Start a lightweight mock orchestrator
      --port <n>                    Port (default: 19800)

`)
}
