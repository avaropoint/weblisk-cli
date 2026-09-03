package dispatch

// Level 4 — Interoperability.
//
// architecture/testing, "Level 4: Interoperability": levels 1 to 3 run one
// component against fixtures, and a component can satisfy every assertion about
// itself while being unable to join a mesh.
//
// This level exists because it was missing. The first time two independently
// generated components of one tenant were asked to interoperate, the second
// could not register — eight consecutive refusals, and none of them visible to
// a suite that starts one binary alone:
//
//	404 at /register            — the path lacked its /v1
//	UNSUPPORTED_VERSION         — semver where the wire version belongs
//	not a standard capability   — an invented capability family
//	scope must be * or self     — a ScopeLevel where an audience belongs
//	signature failed            — timestamp appended to the signed bytes
//	signature failed            — a manifest field the verifier's type drops
//	CONTRACT_VERSION_MISMATCH   — a semver check on the protocol version
//	exits before serving        — the issuer key required before registration
//
// The mock orchestrator cannot catch any of them: it verifies with the key the
// caller supplied and accepts any valid registration. So these tests use the
// REAL orchestrator of the tenant being built.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/avaropoint/weblisk-cli/internal/protocol"
)

// interopTest is one L4 test. It receives both ends of the conversation.
type interopTest struct {
	id   string
	name string
	// standalone tests run the component with NO orchestrator, so they are given
	// only the component's base URL.
	standalone bool
	run        func(env interopEnv) (bool, string, string)
}

// interopEnv is what an L4 test is given.
type interopEnv struct {
	componentBase string
	orchBase      string
	// StandardCapabilities comes from protocol/types.md, not from a list in this
	// file. The vocabulary is the blueprint's, and hardcoding it here would put
	// the tooling back in charge of what a component may ask for.
	standardCapabilities map[string]bool
	// blueprints is the corpus, so a test reads what a component declares rather
	// than what this file assumes about it.
	blueprints map[string]string
	component  string
}

// RunInterop starts a real orchestrator and the component under test against it.
//
// Returns Unrun results, never passes, when the tenant has no orchestrator
// binary to register with — an untested claim reported as untested.
func RunInterop(root, binary, component string, blueprints map[string]string,
	onProgress ProgressFunc) ([]ConformanceResult, string, error) {

	if onProgress == nil {
		onProgress = func(Progress) {}
	}
	tests := l4Tests(component)
	if len(tests) == 0 {
		return nil, "", nil
	}

	caps := standardCapabilitiesFrom(blueprints)

	orchBin := findOrchestratorBinary(root)
	if orchBin == "" {
		var out []ConformanceResult
		for _, t := range tests {
			out = append(out, ConformanceResult{ID: t.id, Name: t.name, Unrun: true,
				Detail: "no orchestrator binary in this tenant — build the orchestrator first"})
		}
		return out, "", nil
	}

	// The orchestrator, once, for every test that needs a peer.
	orchPort, err := freePort()
	if err != nil {
		return nil, "", err
	}
	orchBase := fmt.Sprintf("http://127.0.0.1:%d", orchPort)
	orchCmd, orchLog, err := startComponent(orchBin, root, orchPort, "")
	if err != nil {
		return nil, "", err
	}
	defer stopComponent(orchCmd)
	if werr := waitForListen(orchCmd, orchBase, startupTimeout); werr != nil {
		return nil, orchLog.String(), fmt.Errorf("the tenant's orchestrator did not start: %w", werr)
	}

	var results []ConformanceResult
	var combined strings.Builder
	combined.WriteString(orchLog.String())

	for i, t := range tests {
		onProgress(Progress{Step: i + 1, Total: len(tests), Path: t.id, Status: "checking"})

		port, perr := freePort()
		if perr != nil {
			return results, combined.String(), perr
		}
		base := fmt.Sprintf("http://127.0.0.1:%d", port)
		orchFor := orchBase
		if t.standalone {
			orchFor = "" // deliberately unconfigured
		}
		cmd, log, serr := startComponent(binary, root, port, orchFor)
		if serr != nil {
			return results, combined.String(), serr
		}
		werr := waitForListen(cmd, base, startupTimeout)
		res := ConformanceResult{ID: t.id, Name: t.name}
		if werr != nil {
			res.Detail = "the component never answered: " + werr.Error()
		} else {
			ok, detail, evidence := t.run(interopEnv{
				componentBase: base, orchBase: orchBase, standardCapabilities: caps,
				blueprints: blueprints, component: component,
			})
			res.Passed, res.Detail, res.Evidence = ok, detail, evidence
		}
		stopComponent(cmd)
		combined.WriteString(log.String())
		results = append(results, res)
	}
	return results, combined.String(), nil
}

func l4Tests(component string) []interopTest {
	if component == "orchestrator" {
		// It is the registry. It does not register with itself.
		return nil
	}
	return []interopTest{
		{
			id: "L4-01", name: "Registers With A Real Orchestrator",
			run: func(e interopEnv) (bool, string, string) {
				code, body, err := get(e.orchBase, "/v1/health")
				if err != nil || code != 200 {
					return false, fmt.Sprintf("orchestrator health: %d %v", code, err), ""
				}
				var oh struct {
					Metrics map[string]any `json:"metrics"`
				}
				_ = json.Unmarshal(body, &oh)
				agents := numberOf(oh.Metrics["agents"])
				if agents < 1 {
					return false, "the orchestrator reports 0 agents — the component did not register", ""
				}
				code, cbody, cerr := get(e.componentBase, "/v1/health")
				if cerr != nil || code != 200 {
					return false, fmt.Sprintf("component health: %d %v", code, cerr), ""
				}
				if !mentionsRegistered(cbody) {
					return false, "the component's health does not report a healthy registration", ""
				}
				return true, "", fmt.Sprintf("orchestrator counts %d agent(s); the component reports its registration healthy", agents)
			},
		},
		{
			id: "L4-02", name: "Manifest Is Accepted On Its Own Terms",
			run: func(e interopEnv) (bool, string, string) {
				dir, err := probeDirectory(e.orchBase)
				if err != nil {
					return false, err.Error(), ""
				}
				var faults []string
				checked := 0
				for _, ag := range dir {
					if ag.Name == "weblisk-interop-probe" {
						continue
					}
					checked++
					if ag.ProtocolVersion != "" && ag.ProtocolVersion != "1" {
						faults = append(faults, fmt.Sprintf("%s: protocol_version %q is not the wire version", ag.Name, ag.ProtocolVersion))
					}
					for _, c := range ag.Capabilities {
						if !strings.Contains(c.Name, ":") {
							faults = append(faults, fmt.Sprintf("%s: capability %q is not family:verb", ag.Name, c.Name))
							continue
						}
						if len(e.standardCapabilities) > 0 && !e.standardCapabilities[c.Name] {
							faults = append(faults, fmt.Sprintf("%s: capability %q is not declared by protocol/types", ag.Name, c.Name))
						}
					}
					for _, sub := range ag.Subscriptions {
						if sub.Scope == "" || sub.Scope == "self" || sub.Scope == "*" {
							continue
						}
						if isScopeLevel(sub.Scope) {
							faults = append(faults, fmt.Sprintf("%s: subscription scope %q is a ScopeLevel, not an audience", ag.Name, sub.Scope))
						}
					}
				}
				if checked == 0 {
					return false, "no registered component to inspect in the service directory", ""
				}
				if len(faults) > 0 {
					return false, strings.Join(faults, "; "), ""
				}
				return true, "", fmt.Sprintf("%d registered manifest(s) use declared capabilities, audience scopes and the wire version", checked)
			},
		},
		{
			id: "L4-03", name: "Signature Survives A Foreign Verifier",
			run: func(e interopEnv) (bool, string, string) {
				// A field the verifier's own type does not define. Deliberate: it is
				// the case a verifier re-serializing its own parse gets wrong, and
				// the case a shared struct definition hides.
				code, body, err := registerProbe(e.orchBase, "weblisk-interop-probe", map[string]any{
					"x_unknown_to_the_verifier": "present on purpose",
				})
				if err != nil {
					return false, err.Error(), ""
				}
				if code == 200 {
					return true, "", "a manifest carrying an undefined field registered — canonicalization preserved it"
				}
				if strings.Contains(strings.ToLower(string(body)), "signature") {
					return false, "a correct signature was refused because the manifest carried a field the " +
						"verifier's type does not define — the verifier is canonicalizing its own parse, " +
						"not the payload as received (protocol/spec, Signing input)", ""
				}
				return false, fmt.Sprintf("status %d: %s", code, firstBodyLine(string(body))), ""
			},
		},
		{
			id: "L4-04", name: "Standalone Operation", standalone: true,
			run: func(e interopEnv) (bool, string, string) {
				code, body, err := get(e.componentBase, "/v1/health")
				if err != nil || code != 200 {
					return false, fmt.Sprintf("health with no orchestrator: %d %v", code, err), ""
				}
				var h struct {
					Status string `json:"status"`
				}
				_ = json.Unmarshal(body, &h)
				if h.Status != "degraded" {
					return false, fmt.Sprintf("status is %q with no orchestrator; it must be degraded — "+
						"not healthy, because it cannot serve its purpose, and not unhealthy, "+
						"because nothing has failed", h.Status), ""
				}
				var wrong []string
				checked := 0
				for _, p := range protectedPaths(e.component, e.blueprints) {
					c, b2, e2 := get(e.componentBase, p)
					if e2 != nil || c == 404 {
						continue
					}
					checked++
					if c != 401 && c != 403 {
						wrong = append(wrong, fmt.Sprintf("%s answered %d with no issuer key", p, c))
						continue
					}
					var er map[string]any
					if json.Unmarshal(b2, &er) != nil || er["error"] == nil {
						wrong = append(wrong, p+" refused without a structured ErrorResponse")
					}
				}
				if checked == 0 {
					return false, "no protected endpoint answered at all", ""
				}
				if len(wrong) > 0 {
					return false, strings.Join(wrong, "; "), ""
				}
				return true, "", fmt.Sprintf("serves health as degraded and refuses %d protected endpoint(s) with a structured error", checked)
			},
		},
	}
}

// --- the probe -------------------------------------------------------------

type dirCapability struct {
	Name string `json:"name"`
}
type dirSubscription struct {
	Scope string `json:"scope"`
}
type dirAgent struct {
	Name            string            `json:"name"`
	ProtocolVersion string            `json:"protocol_version"`
	Capabilities    []dirCapability   `json:"capabilities"`
	Subscriptions   []dirSubscription `json:"subscriptions"`
}

// registerProbe registers a throwaway agent, optionally with extra manifest
// fields, and returns the orchestrator's response.
//
// The manifest is built as a map so encoding/json sorts its keys, which is the
// key ordering RFC 8785 requires — the same bytes a compliant implementation
// produces.
func registerProbe(orchBase, name string, extra map[string]any) (int, []byte, error) {
	id, err := protocol.GenerateIdentity(name)
	if err != nil {
		return 0, nil, err
	}
	manifest := map[string]any{
		"name":             name,
		"version":          "1.0.0",
		"protocol_version": "1",
		"description":      "Level 4 interoperability probe",
		"url":              "http://127.0.0.1:1",
		"public_key":       id.PublicKeyB64(),
		"capabilities":     []map[string]any{{"name": "agent:message"}},
	}
	for k, v := range extra {
		manifest[k] = v
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return 0, nil, err
	}
	body, err := json.Marshal(map[string]any{
		"manifest":  manifest,
		"signature": id.Sign(canonical),
		"timestamp": time.Now().Unix(),
	})
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, orchBase+"/v1/register", strings.NewReader(string(body)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out := make([]byte, 64*1024)
	n, _ := resp.Body.Read(out)
	return resp.StatusCode, out[:n], nil
}

// probeDirectory registers a probe and reads the service directory with its
// token, so the component under test is inspected through the protocol rather
// than by reading its source.
func probeDirectory(orchBase string) ([]dirAgent, error) {
	code, body, err := registerProbe(orchBase, "weblisk-interop-probe", nil)
	if err != nil {
		return nil, fmt.Errorf("probe registration: %w", err)
	}
	if code != 200 {
		return nil, fmt.Errorf("probe could not register (status %d: %s) — L4-02 cannot be established",
			code, firstBodyLine(string(body)))
	}
	var reg struct {
		Token string `json:"token"`
		// ServiceDirectory.services, per protocol/types. Not "agents" — five
		// blueprints bound it as agents and this probe copied the same error.
		Services struct {
			Services []dirAgent `json:"services"`
		} `json:"services"`
	}
	if uerr := json.Unmarshal(body, &reg); uerr != nil {
		return nil, fmt.Errorf("probe registration response: %w", uerr)
	}
	if len(reg.Services.Services) > 0 {
		return reg.Services.Services, nil
	}
	req, _ := http.NewRequest(http.MethodGet, orchBase+"/v1/services", nil)
	req.Header.Set("Authorization", "Bearer "+reg.Token)
	resp, derr := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if derr != nil {
		return nil, derr
	}
	defer resp.Body.Close()
	var dir struct {
		Services []dirAgent `json:"services"`
	}
	if json.NewDecoder(resp.Body).Decode(&dir) != nil {
		return nil, fmt.Errorf("service directory could not be read")
	}
	return dir.Services, nil
}

// --- helpers ---------------------------------------------------------------

// findOrchestratorBinary locates the tenant's orchestrator, by the manifest
// that recorded it where possible.
func findOrchestratorBinary(root string) string {
	for _, p := range []string{
		filepath.Join(root, "bin", "orchestrator"),
		filepath.Join(root, "orchestrator"),
		filepath.Join(root, "server", "orchestrator"),
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

func startComponent(binary, root string, port int, orchURL string) (*exec.Cmd, *strings.Builder, error) {
	args := []string{"--port", fmt.Sprintf("%d", port)}
	if orchURL != "" {
		args = append(args, "--orch", orchURL)
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = root
	env := append(os.Environ(), "WL_DEV=1", fmt.Sprintf("WL_PORT=%d", port))
	if orchURL != "" {
		env = append(env, "WL_ORCH_URL="+orchURL)
	}
	cmd.Env = env
	var log strings.Builder
	cmd.Stdout = &log
	cmd.Stderr = &log
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, &log, nil
}

func stopComponent(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

// standardCapabilitiesFrom reads the capability vocabulary out of
// protocol/types.md rather than holding a copy of it.
func standardCapabilitiesFrom(blueprints map[string]string) map[string]bool {
	out := map[string]bool{}
	body, ok := blueprints["protocol/types.md"]
	if !ok {
		return out
	}
	for _, m := range reCapabilityBullet.FindAllStringSubmatch(body, -1) {
		out[m[1]] = true
	}
	return out
}

func isScopeLevel(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "public", "internal", "confidential", "restricted", "critical":
		return true
	}
	return false
}

func mentionsRegistered(body []byte) bool {
	var h struct {
		Components []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"components"`
		Metrics map[string]any `json:"metrics"`
	}
	if json.Unmarshal(body, &h) != nil {
		return false
	}
	for _, c := range h.Components {
		if strings.Contains(strings.ToLower(c.Name), "registration") {
			return c.Status == "healthy" || c.Status == "ok"
		}
	}
	if v, ok := h.Metrics["registered"].(bool); ok {
		return v
	}
	return false
}

func numberOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func firstBodyLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
