package admin

// Admin client for operations commands — communicates with the
// orchestrator's admin API using operator Ed25519 authentication.

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/avaropoint/weblisk-cli/internal/operator"
)

// Client communicates with the orchestrator admin API.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
	JSONOutput bool
}

// NewClient creates an authenticated admin client.
// It checks token expiry and auto-refreshes if < 1 hour remaining.
func NewClient(args []string) (*Client, error) {
	token, orchURL, err := operator.LoadToken()
	if err != nil {
		return nil, err
	}

	// Allow override via args
	for i := 0; i < len(args); i++ {
		if args[i] == "--orch" && i+1 < len(args) {
			orchURL = args[i+1]
		} else if strings.HasPrefix(args[i], "--orch=") {
			orchURL = strings.SplitN(args[i], "=", 2)[1]
		}
	}

	jsonOutput := false
	for _, a := range args {
		if a == "--json" {
			jsonOutput = true
		}
	}

	c := &Client{
		BaseURL:    strings.TrimRight(orchURL, "/"),
		Token:      token,
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
		JSONOutput: jsonOutput,
	}

	// Auto-refresh if token expires within 1 hour
	if expires := operator.TokenExpiry(); !expires.IsZero() {
		if time.Until(expires) < time.Hour {
			if newToken, err := operator.RefreshToken(); err == nil {
				c.Token = newToken
			}
		}
	}

	return c, nil
}

func (c *Client) get(path string) ([]byte, error) {
	body, err := c.doGet(path)
	if err != nil && strings.Contains(err.Error(), "authentication failed") {
		// 401 retry: attempt token refresh and retry once
		if newToken, refreshErr := operator.RefreshToken(); refreshErr == nil {
			c.Token = newToken
			return c.doGet(path)
		}
		return nil, fmt.Errorf("session expired. Run: weblisk operator register")
	}
	return body, err
}

func (c *Client) doGet(path string) ([]byte, error) {
	req, err := http.NewRequest("GET", c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w\n  Is the orchestrator running at %s?", err, c.BaseURL)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case 200:
		return body, nil
	case 401:
		return nil, fmt.Errorf("authentication failed. Run: weblisk operator token --refresh")
	case 403:
		return nil, fmt.Errorf("permission denied (insufficient role)")
	case 404:
		return nil, fmt.Errorf("not found")
	default:
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
}

func (c *Client) post(path string, payload any) ([]byte, error) {
	body, err := c.doPost(path, payload)
	if err != nil && strings.Contains(err.Error(), "authentication failed") {
		// 401 retry: attempt token refresh and retry once
		if newToken, refreshErr := operator.RefreshToken(); refreshErr == nil {
			c.Token = newToken
			return c.doPost(path, payload)
		}
		return nil, fmt.Errorf("session expired. Run: weblisk operator register")
	}
	return body, err
}

func (c *Client) doPost(path string, payload any) ([]byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		data, _ := json.Marshal(payload)
		bodyReader = strings.NewReader(string(data))
	}

	req, err := http.NewRequest("POST", c.BaseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case 200, 201:
		return body, nil
	case 401:
		return nil, fmt.Errorf("authentication failed. Run: weblisk operator token --refresh")
	case 403:
		return nil, fmt.Errorf("permission denied (insufficient role)")
	case 404:
		return nil, fmt.Errorf("not found")
	default:
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
}

// stream opens a long-lived GET for streaming endpoints (e.g. audit --follow).
func (c *Client) stream(path string) (*http.Response, error) {
	req, err := http.NewRequest("GET", c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/x-ndjson")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w\n  Is the orchestrator running at %s?", err, c.BaseURL)
	}

	if resp.StatusCode != 200 {
		resp.Body.Close()
		switch resp.StatusCode {
		case 401:
			return nil, fmt.Errorf("authentication failed. Run: weblisk operator token --refresh")
		case 403:
			return nil, fmt.Errorf("permission denied (insufficient role)")
		default:
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
	}

	return resp, nil
}

// Get is the exported version of get for use by other packages.
func (c *Client) Get(path string) ([]byte, error) {
	return c.get(path)
}

// Post is the exported version of post for use by other packages.
func (c *Client) Post(path string, payload any) ([]byte, error) {
	return c.post(path, payload)
}

// ── Status ───────────────────────────────────────────────────

// Status shows the system overview. Supports --watch for continuous refresh.
func Status(args []string) error {
	watch := false
	for _, a := range args {
		if a == "--watch" {
			watch = true
		}
	}

	if watch {
		for {
			fmt.Print("\033[H\033[2J") // clear terminal
			if err := statusOnce(args); err != nil {
				return err
			}
			time.Sleep(5 * time.Second)
		}
	}

	return statusOnce(args)
}

func statusOnce(args []string) error {
	client, err := NewClient(args)
	if err != nil {
		return err
	}

	body, err := client.get("/v1/admin/overview")
	if err != nil {
		return err
	}

	if client.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var overview map[string]any
	if err := json.Unmarshal(body, &overview); err != nil {
		return fmt.Errorf("unexpected response")
	}

	fmt.Println()
	fmt.Printf("  Weblisk — %s\n", client.BaseURL)
	fmt.Println()
	printMap(overview, "  ")
	fmt.Println()
	return nil
}

// ── Agents ───────────────────────────────────────────────────

// AgentsList lists registered agents. Supports --type and --status filters.
func AgentsList(args []string) error {
	client, err := NewClient(args)
	if err != nil {
		return err
	}

	params := []string{}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--type" && i+1 < len(args):
			i++
			params = append(params, "type="+args[i])
		case strings.HasPrefix(args[i], "--type="):
			params = append(params, "type="+strings.SplitN(args[i], "=", 2)[1])
		case args[i] == "--status" && i+1 < len(args):
			i++
			params = append(params, "status="+args[i])
		case strings.HasPrefix(args[i], "--status="):
			params = append(params, "status="+strings.SplitN(args[i], "=", 2)[1])
		}
	}

	path := "/v1/admin/agents"
	if len(params) > 0 {
		path += "?" + strings.Join(params, "&")
	}

	body, err := client.get(path)
	if err != nil {
		return err
	}

	if client.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var agents []map[string]any
	if err := json.Unmarshal(body, &agents); err != nil {
		fmt.Println(string(body))
		return nil
	}

	fmt.Println()
	fmt.Printf("  %-18s %-14s %-10s %-10s\n", "NAME", "TYPE", "STATUS", "VERSION")
	for _, a := range agents {
		fmt.Printf("  %-18s %-14s %-10s %-10s\n",
			getString(a, "name"), getString(a, "type"),
			getString(a, "status"), getString(a, "version"))
	}
	fmt.Printf("\n  %d agents\n\n", len(agents))
	return nil
}

// AgentsDescribe shows detail for a single agent. Supports --metrics-range.
func AgentsDescribe(name string, args []string) error {
	client, err := NewClient(args)
	if err != nil {
		return err
	}

	params := []string{}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--metrics-range" && i+1 < len(args):
			i++
			params = append(params, "metrics_range="+args[i])
		case strings.HasPrefix(args[i], "--metrics-range="):
			params = append(params, "metrics_range="+strings.SplitN(args[i], "=", 2)[1])
		}
	}

	path := "/v1/admin/agents/" + name
	if len(params) > 0 {
		path += "?" + strings.Join(params, "&")
	}

	body, err := client.get(path)
	if err != nil {
		return err
	}

	if client.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var agent map[string]any
	if err := json.Unmarshal(body, &agent); err != nil {
		fmt.Println(string(body))
		return nil
	}

	fmt.Println()
	printMap(agent, "  ")
	fmt.Println()
	return nil
}

// AgentsDeregister removes an agent.
func AgentsDeregister(name string, args []string) error {
	confirm := false
	for _, a := range args {
		if a == "--confirm" {
			confirm = true
		}
	}

	if !confirm {
		fmt.Printf("  This will deregister agent '%s' and remove it from all workflows.\n", name)
		fmt.Printf("  Use --confirm to proceed.\n")
		return nil
	}

	client, err := NewClient(args)
	if err != nil {
		return err
	}

	_, err = client.post("/v1/admin/agents/"+name+"/deregister", nil)
	if err != nil {
		return err
	}

	fmt.Printf("  [ok] Agent '%s' deregistered.\n", name)
	return nil
}

// ── Domains ──────────────────────────────────────────────────

// DomainsList lists domain controllers.
func DomainsList(args []string) error {
	client, err := NewClient(args)
	if err != nil {
		return err
	}

	body, err := client.get("/v1/admin/domains")
	if err != nil {
		return err
	}

	if client.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var domains []map[string]any
	if err := json.Unmarshal(body, &domains); err != nil {
		fmt.Println(string(body))
		return nil
	}

	fmt.Println()
	fmt.Printf("  %-14s %-10s %-20s\n", "NAME", "STATUS", "AGENTS")
	for _, d := range domains {
		fmt.Printf("  %-14s %-10s %-20s\n",
			getString(d, "name"), getString(d, "status"), getString(d, "agents"))
	}
	fmt.Printf("\n  %d domains\n\n", len(domains))
	return nil
}

// DomainsDescribe shows detail for a single domain.
func DomainsDescribe(name string, args []string) error {
	client, err := NewClient(args)
	if err != nil {
		return err
	}

	body, err := client.get("/v1/admin/domains/" + name)
	if err != nil {
		return err
	}

	if client.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var domain map[string]any
	json.Unmarshal(body, &domain)
	fmt.Println()
	printMap(domain, "  ")
	fmt.Println()
	return nil
}

// ── Workflows ────────────────────────────────────────────────

// WorkflowsList lists recent workflow executions.
// Supports --domain, --status, and --limit filters.
func WorkflowsList(args []string) error {
	client, err := NewClient(args)
	if err != nil {
		return err
	}

	params := []string{}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--domain" && i+1 < len(args):
			i++
			params = append(params, "domain="+args[i])
		case strings.HasPrefix(args[i], "--domain="):
			params = append(params, "domain="+strings.SplitN(args[i], "=", 2)[1])
		case args[i] == "--status" && i+1 < len(args):
			i++
			params = append(params, "status="+args[i])
		case strings.HasPrefix(args[i], "--status="):
			params = append(params, "status="+strings.SplitN(args[i], "=", 2)[1])
		case args[i] == "--limit" && i+1 < len(args):
			i++
			params = append(params, "limit="+args[i])
		case strings.HasPrefix(args[i], "--limit="):
			params = append(params, "limit="+strings.SplitN(args[i], "=", 2)[1])
		}
	}

	path := "/v1/admin/workflows"
	if len(params) > 0 {
		path += "?" + strings.Join(params, "&")
	}

	body, err := client.get(path)
	if err != nil {
		return err
	}

	if client.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var workflows []map[string]any
	if err := json.Unmarshal(body, &workflows); err != nil {
		fmt.Println(string(body))
		return nil
	}

	fmt.Println()
	fmt.Printf("  %-14s %-16s %-10s %-10s %-10s\n", "ID", "WORKFLOW", "DOMAIN", "STATUS", "DURATION")
	for _, w := range workflows {
		fmt.Printf("  %-14s %-16s %-10s %-10s %-10s\n",
			getString(w, "id"), getString(w, "workflow"),
			getString(w, "domain"), getString(w, "status"), getString(w, "duration"))
	}
	fmt.Printf("\n  %d workflows\n\n", len(workflows))
	return nil
}

// WorkflowsDescribe shows detail for a single workflow execution.
func WorkflowsDescribe(id string, args []string) error {
	client, err := NewClient(args)
	if err != nil {
		return err
	}

	body, err := client.get("/v1/admin/workflows/" + id)
	if err != nil {
		return err
	}

	if client.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var wf map[string]any
	json.Unmarshal(body, &wf)
	fmt.Println()
	printMap(wf, "  ")
	fmt.Println()
	return nil
}

// ── Approvals ────────────────────────────────────────────────

// ApprovalsList lists pending recommendations.
// Supports --priority and --agent filters.
func ApprovalsList(args []string) error {
	client, err := NewClient(args)
	if err != nil {
		return err
	}

	params := []string{}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--priority" && i+1 < len(args):
			i++
			params = append(params, "priority="+args[i])
		case strings.HasPrefix(args[i], "--priority="):
			params = append(params, "priority="+strings.SplitN(args[i], "=", 2)[1])
		case args[i] == "--agent" && i+1 < len(args):
			i++
			params = append(params, "agent="+args[i])
		case strings.HasPrefix(args[i], "--agent="):
			params = append(params, "agent="+strings.SplitN(args[i], "=", 2)[1])
		}
	}

	path := "/v1/admin/approvals"
	if len(params) > 0 {
		path += "?" + strings.Join(params, "&")
	}

	body, err := client.get(path)
	if err != nil {
		return err
	}

	if client.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var approvals []map[string]any
	if err := json.Unmarshal(body, &approvals); err != nil {
		fmt.Println(string(body))
		return nil
	}

	fmt.Println()
	fmt.Printf("  %-10s %-10s %-16s %-16s %s\n", "ID", "PRIORITY", "AGENT", "TARGET", "SUMMARY")
	for _, a := range approvals {
		fmt.Printf("  %-10s %-10s %-16s %-16s %s\n",
			getString(a, "id"), getString(a, "priority"),
			getString(a, "agent"), getString(a, "target"), getString(a, "summary"))
	}
	fmt.Printf("\n  %d pending\n\n", len(approvals))
	return nil
}

// ApprovalsDescribe shows a single recommendation.
func ApprovalsDescribe(id string, args []string) error {
	client, err := NewClient(args)
	if err != nil {
		return err
	}

	body, err := client.get("/v1/admin/approvals/" + id)
	if err != nil {
		return err
	}

	if client.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var approval map[string]any
	json.Unmarshal(body, &approval)
	fmt.Println()
	printMap(approval, "  ")
	fmt.Println()
	return nil
}

// ApprovalsAccept accepts one or more recommendations.
// Supports --all, --priority (batch accept), and --stdin.
func ApprovalsAccept(ids []string, args []string) error {
	acceptAll := false
	priorityFilter := ""
	fromStdin := false
	confirm := false

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--all":
			acceptAll = true
		case args[i] == "--confirm":
			confirm = true
		case args[i] == "--stdin":
			fromStdin = true
		case args[i] == "--priority" && i+1 < len(args):
			i++
			priorityFilter = args[i]
		case strings.HasPrefix(args[i], "--priority="):
			priorityFilter = strings.SplitN(args[i], "=", 2)[1]
		}
	}

	client, err := NewClient(args)
	if err != nil {
		return err
	}

	// Batch accept by --all or --priority
	if acceptAll || priorityFilter != "" {
		if !confirm {
			if acceptAll {
				fmt.Println("  This will accept ALL pending approvals.")
			} else {
				fmt.Printf("  This will accept all %s priority approvals.\n", priorityFilter)
			}
			fmt.Println("  Use --confirm to proceed.")
			return nil
		}

		payload := map[string]string{}
		if priorityFilter != "" {
			payload["priority"] = priorityFilter
		}
		_, err := client.post("/v1/admin/approvals/accept-batch", payload)
		if err != nil {
			return err
		}
		fmt.Println("  [ok] Batch accept completed.")
		return nil
	}

	// Read IDs from stdin if --stdin
	if fromStdin {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				ids = append(ids, line)
			}
		}
	}

	for _, id := range ids {
		_, err := client.post("/v1/admin/approvals/"+id+"/accept", nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [error] %s: %v\n", id, err)
			continue
		}
		fmt.Printf("  [ok] Accepted %s\n", id)
	}
	return nil
}

// ApprovalsReject rejects a recommendation.
func ApprovalsReject(id, reason string, args []string) error {
	if reason == "" {
		return fmt.Errorf("--reason is required for rejections")
	}

	client, err := NewClient(args)
	if err != nil {
		return err
	}

	_, err = client.post("/v1/admin/approvals/"+id+"/reject", map[string]string{"reason": reason})
	if err != nil {
		return err
	}

	fmt.Printf("  [ok] Rejected %s\n", id)
	return nil
}

// ── Strategies ───────────────────────────────────────────────

// StrategiesList lists strategies.
func StrategiesList(args []string) error {
	client, err := NewClient(args)
	if err != nil {
		return err
	}

	body, err := client.get("/v1/admin/strategies")
	if err != nil {
		return err
	}

	if client.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var strategies []map[string]any
	json.Unmarshal(body, &strategies)
	fmt.Println()
	fmt.Printf("  %-10s %-30s %-10s %-10s %s\n", "ID", "NAME", "PRIORITY", "STATUS", "PROGRESS")
	for _, s := range strategies {
		fmt.Printf("  %-10s %-30s %-10s %-10s %s\n",
			getString(s, "id"), getString(s, "name"),
			getString(s, "priority"), getString(s, "status"), getString(s, "progress"))
	}
	fmt.Println()
	return nil
}

// StrategiesDescribe shows a single strategy.
func StrategiesDescribe(id string, args []string) error {
	client, err := NewClient(args)
	if err != nil {
		return err
	}

	body, err := client.get("/v1/admin/strategies/" + id)
	if err != nil {
		return err
	}

	if client.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var strategy map[string]any
	json.Unmarshal(body, &strategy)
	fmt.Println()
	printMap(strategy, "  ")
	fmt.Println()
	return nil
}

// StrategiesCreate creates a new strategy.
// With --json, reads the strategy definition from stdin.
// Otherwise, prompts interactively.
func StrategiesCreate(args []string) error {
	fromJSON := false
	for _, a := range args {
		if a == "--json" {
			fromJSON = true
		}
	}

	client, err := NewClient(args)
	if err != nil {
		return err
	}

	var payload map[string]string

	if fromJSON {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
	} else {
		payload = map[string]string{}
		reader := bufio.NewReader(os.Stdin)

		fmt.Print("  Name: ")
		name, _ := reader.ReadString('\n')
		payload["name"] = strings.TrimSpace(name)

		fmt.Print("  Objective: ")
		obj, _ := reader.ReadString('\n')
		payload["objective"] = strings.TrimSpace(obj)

		fmt.Print("  Priority (critical|high|medium|low): ")
		prio, _ := reader.ReadString('\n')
		payload["priority"] = strings.TrimSpace(prio)
		if payload["priority"] == "" {
			payload["priority"] = "medium"
		}

		fmt.Print("  Agents (comma-separated, blank for all): ")
		agents, _ := reader.ReadString('\n')
		payload["agents"] = strings.TrimSpace(agents)
	}

	body, err := client.post("/v1/admin/strategies", payload)
	if err != nil {
		return err
	}

	if client.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var result map[string]any
	if json.Unmarshal(body, &result) == nil {
		fmt.Printf("\n  [ok] Created strategy %s: %s\n\n", getString(result, "id"), getString(result, "name"))
	} else {
		fmt.Println("  [ok] Strategy created.")
	}
	return nil
}

// ── Federation ───────────────────────────────────────────────

// FederationPeers lists federation peers.
func FederationPeers(args []string) error {
	client, err := NewClient(args)
	if err != nil {
		return err
	}

	body, err := client.get("/v1/admin/federation/peers")
	if err != nil {
		return err
	}

	if client.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var peers []map[string]any
	json.Unmarshal(body, &peers)
	fmt.Println()
	fmt.Printf("  %-16s %-10s %-14s %-10s %s\n", "NAME", "TIER", "JURISDICTION", "STATUS", "CAPABILITIES")
	for _, p := range peers {
		fmt.Printf("  %-16s %-10s %-14s %-10s %s\n",
			getString(p, "name"), getString(p, "tier"),
			getString(p, "jurisdiction"), getString(p, "status"), getString(p, "capabilities"))
	}
	fmt.Println()
	return nil
}

// FederationPending lists pending peering requests.
func FederationPending(args []string) error {
	client, err := NewClient(args)
	if err != nil {
		return err
	}

	body, err := client.get("/v1/admin/federation/pending")
	if err != nil {
		return err
	}

	if client.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var pending []map[string]any
	json.Unmarshal(body, &pending)
	fmt.Println()
	for _, p := range pending {
		fmt.Printf("  %-10s %-16s %s\n", getString(p, "id"), getString(p, "from"), getString(p, "capabilities"))
	}
	fmt.Println()
	return nil
}

// FederationAccept accepts a peering request.
func FederationAccept(id string, args []string) error {
	client, err := NewClient(args)
	if err != nil {
		return err
	}

	_, err = client.post("/v1/admin/federation/"+id+"/accept", nil)
	if err != nil {
		return err
	}

	fmt.Printf("  [ok] Accepted peering request %s\n", id)
	return nil
}

// FederationReject rejects a peering request.
func FederationReject(id string, args []string) error {
	client, err := NewClient(args)
	if err != nil {
		return err
	}

	_, err = client.post("/v1/admin/federation/"+id+"/reject", nil)
	if err != nil {
		return err
	}

	fmt.Printf("  [ok] Rejected peering request %s\n", id)
	return nil
}

// FederationRevoke revokes trust with a peer.
func FederationRevoke(peer string, args []string) error {
	confirm := false
	for _, a := range args {
		if a == "--confirm" {
			confirm = true
		}
	}

	if !confirm {
		fmt.Printf("  This will revoke all trust with '%s' and terminate active tasks.\n", peer)
		fmt.Printf("  Use --confirm to proceed.\n")
		return nil
	}

	client, err := NewClient(args)
	if err != nil {
		return err
	}

	_, err = client.post("/v1/admin/federation/"+peer+"/revoke", nil)
	if err != nil {
		return err
	}

	fmt.Printf("  [ok] Trust revoked for %s\n", peer)
	return nil
}

// ── Audit ────────────────────────────────────────────────────

// Audit streams or queries the audit log.
// Supports --follow (live tail) and --export (json|csv).
func Audit(args []string) error {
	follow := false
	export := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--follow":
			follow = true
		case args[i] == "--export" && i+1 < len(args):
			i++
			export = args[i]
		case strings.HasPrefix(args[i], "--export="):
			export = strings.SplitN(args[i], "=", 2)[1]
		}
	}

	client, err := NewClient(args)
	if err != nil {
		return err
	}

	// Build query params from args
	params := []string{}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--actor" && i+1 < len(args):
			i++
			params = append(params, "actor="+args[i])
		case args[i] == "--action" && i+1 < len(args):
			i++
			params = append(params, "action="+args[i])
		case args[i] == "--since" && i+1 < len(args):
			i++
			params = append(params, "since="+args[i])
		case args[i] == "--limit" && i+1 < len(args):
			i++
			params = append(params, "limit="+args[i])
		}
	}

	if follow {
		params = append(params, "follow=true")
	}

	path := "/v1/admin/audit"
	if len(params) > 0 {
		path += "?" + strings.Join(params, "&")
	}

	// Streaming mode
	if follow {
		resp, err := client.stream(path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if export == "json" {
				fmt.Println(line)
			} else {
				var e map[string]any
				if json.Unmarshal([]byte(line), &e) == nil {
					fmt.Printf("  %-12s %-16s %-12s %-16s %s\n",
						getString(e, "time"), getString(e, "actor"),
						getString(e, "action"), getString(e, "target"), getString(e, "detail"))
				}
			}
		}
		return nil
	}

	body, err := client.get(path)
	if err != nil {
		return err
	}

	// Export modes
	if export == "json" {
		fmt.Println(string(body))
		return nil
	}

	var entries []map[string]any
	if json.Unmarshal(body, &entries) != nil {
		fmt.Println(string(body))
		return nil
	}

	if export == "csv" {
		w := csv.NewWriter(os.Stdout)
		w.Write([]string{"time", "actor", "action", "target", "detail"})
		for _, e := range entries {
			w.Write([]string{
				getString(e, "time"), getString(e, "actor"),
				getString(e, "action"), getString(e, "target"), getString(e, "detail"),
			})
		}
		w.Flush()
		return nil
	}

	if client.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	fmt.Println()
	fmt.Printf("  %-12s %-16s %-12s %-16s %s\n", "TIME", "ACTOR", "ACTION", "TARGET", "DETAIL")
	for _, e := range entries {
		fmt.Printf("  %-12s %-16s %-12s %-16s %s\n",
			getString(e, "time"), getString(e, "actor"),
			getString(e, "action"), getString(e, "target"), getString(e, "detail"))
	}
	fmt.Println()
	return nil
}

// ── Helpers ──────────────────────────────────────────────────

func getString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return fmt.Sprintf("%.0f", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func printMap(m map[string]any, indent string) {
	for k, v := range m {
		switch val := v.(type) {
		case map[string]any:
			fmt.Printf("%s%s:\n", indent, k)
			printMap(val, indent+"  ")
		case []any:
			fmt.Printf("%s%s: [%d items]\n", indent, k, len(val))
		default:
			fmt.Printf("%s%-16s %v\n", indent, k+":", v)
		}
	}
}

// ── Operators ────────────────────────────────────────────────

// OperatorsList lists all registered operators.
func OperatorsList(args []string) error {
	c, err := NewClient(args)
	if err != nil {
		return err
	}

	body, err := c.get("/v1/admin/operators")
	if err != nil {
		return err
	}

	if c.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var result struct {
		Operators []map[string]any `json:"operators"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("unexpected response")
	}

	fmt.Println()
	fmt.Printf("  %-12s %-10s %-10s %s\n", "NAME", "ROLE", "STATUS", "REGISTERED")
	for _, op := range result.Operators {
		fmt.Printf("  %-12s %-10s %-10s %s\n",
			getString(op, "name"),
			getString(op, "role"),
			getString(op, "status"),
			getString(op, "registered"))
	}
	fmt.Println()
	return nil
}

// OperatorsDescribe shows full detail for a single operator.
func OperatorsDescribe(name string, args []string) error {
	c, err := NewClient(args)
	if err != nil {
		return err
	}

	body, err := c.get("/v1/admin/operators/" + name)
	if err != nil {
		return err
	}

	if c.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var op map[string]any
	if err := json.Unmarshal(body, &op); err != nil {
		return fmt.Errorf("unexpected response")
	}

	fmt.Println()
	fmt.Printf("  Operator:    %s\n", getString(op, "name"))
	fmt.Printf("  Role:        %s\n", getString(op, "role"))
	fmt.Printf("  Status:      %s\n", getString(op, "status"))
	fmt.Printf("  Registered:  %s\n", getString(op, "registered"))
	fmt.Printf("  Last active: %s\n", getString(op, "last_active"))
	fmt.Printf("  Key ID:      %s\n", getString(op, "key_id"))
	fmt.Println()
	return nil
}

// OperatorsRevoke revokes another operator's access (admin only).
func OperatorsRevoke(name string, args []string) error {
	confirm := false
	for _, a := range args {
		if a == "--confirm" {
			confirm = true
		}
	}

	if !confirm {
		fmt.Printf("Revoking operator '%s'...\n", name)
		fmt.Printf("Type operator name to confirm: ")
		var input string
		fmt.Scanln(&input)
		if strings.TrimSpace(input) != name {
			return fmt.Errorf("confirmation failed — name did not match")
		}
	}

	c, err := NewClient(args)
	if err != nil {
		return err
	}

	_, err = c.post("/v1/admin/operators/"+name+"/revoke", nil)
	if err != nil {
		return err
	}

	fmt.Printf("✓ Operator '%s' revoked. Public key invalidated, token expired.\n", name)
	return nil
}

// OperatorsRole changes an operator's role (admin only).
func OperatorsRole(name, role string, args []string) error {
	validRoles := map[string]bool{"admin": true, "operator": true, "auditor": true, "viewer": true}
	if !validRoles[role] {
		return fmt.Errorf("invalid role %q. Valid roles: admin, operator, auditor, viewer", role)
	}

	c, err := NewClient(args)
	if err != nil {
		return err
	}

	payload := map[string]string{"role": role}
	_, err = c.post("/v1/admin/operators/"+name+"/role", payload)
	if err != nil {
		return err
	}

	fmt.Printf("✓ Operator '%s' role changed to %s\n", name, role)
	return nil
}

// ── Strategies (update/delete) ───────────────────────────────

// StrategiesUpdate updates strategy targets or priority.
func StrategiesUpdate(id string, args []string) error {
	c, err := NewClient(args)
	if err != nil {
		return err
	}

	payload := map[string]any{}
	jsonInput := false

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--priority" && i+1 < len(args):
			i++
			var p int
			fmt.Sscanf(args[i], "%d", &p)
			payload["priority"] = p
		case strings.HasPrefix(args[i], "--priority="):
			var p int
			fmt.Sscanf(strings.SplitN(args[i], "=", 2)[1], "%d", &p)
			payload["priority"] = p
		case args[i] == "--deadline" && i+1 < len(args):
			i++
			payload["deadline"] = args[i]
		case strings.HasPrefix(args[i], "--deadline="):
			payload["deadline"] = strings.SplitN(args[i], "=", 2)[1]
		case args[i] == "--json":
			jsonInput = true
		}
	}

	if jsonInput {
		var stdinData map[string]any
		dec := json.NewDecoder(os.Stdin)
		if err := dec.Decode(&stdinData); err != nil {
			return fmt.Errorf("reading JSON from stdin: %w", err)
		}
		payload = stdinData
	}

	if len(payload) == 0 {
		return fmt.Errorf("no updates specified.\n  Usage: weblisk strategies update <id> --priority <n> | --deadline <date> | --json")
	}

	_, err = c.post("/v1/admin/strategies/"+id+"/update", payload)
	if err != nil {
		return err
	}

	fmt.Printf("✓ Strategy %s updated.\n", id)
	return nil
}

// StrategiesDelete archives a strategy (admin only, requires confirmation).
func StrategiesDelete(id string, args []string) error {
	confirm := false
	for _, a := range args {
		if a == "--confirm" {
			confirm = true
		}
	}

	if !confirm {
		fmt.Printf("⚠ This will archive strategy '%s'.\n", id)
		fmt.Printf("  Type strategy ID to confirm: ")
		var input string
		fmt.Scanln(&input)
		if strings.TrimSpace(input) != id {
			return fmt.Errorf("confirmation failed — ID did not match")
		}
	}

	c, err := NewClient(args)
	if err != nil {
		return err
	}

	_, err = c.post("/v1/admin/strategies/"+id+"/delete", nil)
	if err != nil {
		return err
	}

	fmt.Printf("✓ Strategy %s archived.\n", id)
	return nil
}

// ── Federations (describe/contracts) ─────────────────────────

// FederationsDescribe shows full detail for a federation peer.
func FederationsDescribe(name string, args []string) error {
	c, err := NewClient(args)
	if err != nil {
		return err
	}

	body, err := c.get("/v1/admin/federation/peers/" + name)
	if err != nil {
		return err
	}

	if c.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var peer map[string]any
	if err := json.Unmarshal(body, &peer); err != nil {
		return fmt.Errorf("unexpected response")
	}

	fmt.Println()
	fmt.Printf("  Peer:          %s\n", getString(peer, "name"))
	fmt.Printf("  Tier:          %s\n", getString(peer, "tier"))
	fmt.Printf("  Jurisdiction:  %s\n", getString(peer, "jurisdiction"))
	fmt.Printf("  Status:        %s\n", getString(peer, "status"))
	fmt.Printf("  Expires:       %s\n", getString(peer, "expires"))
	fmt.Println()

	if caps, ok := peer["capabilities"].([]any); ok && len(caps) > 0 {
		fmt.Println("  Capabilities:")
		for _, cap := range caps {
			fmt.Printf("    %v\n", cap)
		}
		fmt.Println()
	}

	if contract, ok := peer["data_contract"].(map[string]any); ok {
		fmt.Println("  Data Contract:")
		printMap(contract, "    ")
		fmt.Println()
	}

	return nil
}

// FederationsContracts lists all active data contracts across federation peers.
func FederationsContracts(args []string) error {
	c, err := NewClient(args)
	if err != nil {
		return err
	}

	body, err := c.get("/v1/admin/federation/contracts")
	if err != nil {
		return err
	}

	if c.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var result struct {
		Contracts []map[string]any `json:"contracts"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("unexpected response")
	}

	fmt.Println()
	fmt.Printf("  %-14s %-18s %-18s %-18s %-14s %s\n",
		"PEER", "CAPABILITY", "INBOUND FIELDS", "OUTBOUND FIELDS", "JURISDICTION", "RETENTION")
	for _, c := range result.Contracts {
		fmt.Printf("  %-14s %-18s %-18s %-18s %-14s %s\n",
			getString(c, "peer"),
			getString(c, "capability"),
			getString(c, "inbound_fields"),
			getString(c, "outbound_fields"),
			getString(c, "jurisdiction"),
			getString(c, "retention"))
	}
	fmt.Println()
	return nil
}

// ── Observations ─────────────────────────────────────────────

// ObservationsList shows observation history from the lifecycle loop.
func ObservationsList(args []string) error {
	c, err := NewClient(args)
	if err != nil {
		return err
	}

	query := "/v1/admin/observations?"
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--strategy" && i+1 < len(args):
			i++
			query += "strategy=" + args[i] + "&"
		case strings.HasPrefix(args[i], "--strategy="):
			query += "strategy=" + strings.SplitN(args[i], "=", 2)[1] + "&"
		case args[i] == "--agent" && i+1 < len(args):
			i++
			query += "agent=" + args[i] + "&"
		case strings.HasPrefix(args[i], "--agent="):
			query += "agent=" + strings.SplitN(args[i], "=", 2)[1] + "&"
		case args[i] == "--since" && i+1 < len(args):
			i++
			query += "since=" + args[i] + "&"
		case strings.HasPrefix(args[i], "--since="):
			query += "since=" + strings.SplitN(args[i], "=", 2)[1] + "&"
		case args[i] == "--limit" && i+1 < len(args):
			i++
			query += "limit=" + args[i] + "&"
		case strings.HasPrefix(args[i], "--limit="):
			query += "limit=" + strings.SplitN(args[i], "=", 2)[1] + "&"
		}
	}

	body, err := c.get(strings.TrimRight(query, "&?"))
	if err != nil {
		return err
	}

	if c.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var result struct {
		Observations []map[string]any `json:"observations"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("unexpected response")
	}

	fmt.Println()
	fmt.Printf("  %-12s %-18s %-12s %-18s %s\n", "TIME", "AGENT", "STRATEGY", "TARGET", "FINDING")
	for _, obs := range result.Observations {
		fmt.Printf("  %-12s %-18s %-12s %-18s %s\n",
			getString(obs, "time"),
			getString(obs, "agent"),
			getString(obs, "strategy"),
			getString(obs, "target"),
			getString(obs, "finding"))
	}
	fmt.Println()
	return nil
}

// ObservationsTrends displays metric trend data for a strategy.
func ObservationsTrends(args []string) error {
	strategy := ""
	rangeVal := "30d"

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--strategy" && i+1 < len(args):
			i++
			strategy = args[i]
		case strings.HasPrefix(args[i], "--strategy="):
			strategy = strings.SplitN(args[i], "=", 2)[1]
		case args[i] == "--range" && i+1 < len(args):
			i++
			rangeVal = args[i]
		case strings.HasPrefix(args[i], "--range="):
			rangeVal = strings.SplitN(args[i], "=", 2)[1]
		}
	}

	if strategy == "" {
		return fmt.Errorf("--strategy is required.\n  Usage: weblisk observations trends --strategy <id> [--range 7d|30d|90d]")
	}

	c, err := NewClient(args)
	if err != nil {
		return err
	}

	body, err := c.get(fmt.Sprintf("/v1/admin/observations/trends?strategy=%s&range=%s", strategy, rangeVal))
	if err != nil {
		return err
	}

	if c.JSONOutput {
		fmt.Println(string(body))
		return nil
	}

	var result struct {
		Strategy string           `json:"strategy"`
		Name     string           `json:"name"`
		Metrics  []map[string]any `json:"metrics"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("unexpected response")
	}

	fmt.Println()
	fmt.Printf("  Strategy: %s (%s)\n\n", result.Strategy, result.Name)
	fmt.Printf("  %-20s %-10s %-10s %-10s %s\n", "METRIC", "PREVIOUS", "CURRENT", "TREND", "GOAL")
	for _, m := range result.Metrics {
		fmt.Printf("  %-20s %-10s %-10s %-10s %s\n",
			getString(m, "name"),
			getString(m, "previous"),
			getString(m, "current"),
			getString(m, "trend"),
			getString(m, "goal"))
	}
	fmt.Println()
	return nil
}
