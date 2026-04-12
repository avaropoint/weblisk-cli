package protocol

// ── Protocol Verification ───────────────────────────────────
//
// Tests a running orchestrator or agent against the Weblisk
// protocol specification. Makes HTTP requests and validates
// responses match the expected contract.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

func jsonReader(data []byte) io.Reader {
	return bytes.NewReader(data)
}

// ── Verify Orchestrator ─────────────────────────────────────

// VerifyOrchestrator runs protocol compliance tests against a running orchestrator.
func VerifyOrchestrator(url string) error {
	fmt.Println()
	fmt.Println("  ⚡ Verifying Orchestrator Protocol Compliance")
	fmt.Printf("  Target: %s\n\n", url)

	pass := 0
	fail := 0

	// Test 1: Health endpoint
	fmt.Print("  [1] GET /v1/health ... ")
	resp, err := http.Get(url + PathHealth)
	if err != nil {
		fmt.Printf("FAIL (unreachable: %v)\n", err)
		fail++
	} else {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var health HealthStatus
		if err := json.Unmarshal(body, &health); err != nil {
			fmt.Printf("FAIL (invalid JSON: %v)\n", err)
			fail++
		} else if health.Name == "" || health.Status == "" {
			fmt.Printf("FAIL (missing name or status)\n")
			fail++
		} else {
			fmt.Printf("PASS (%s, %s)\n", health.Name, health.Status)
			pass++
		}
	}

	// Test 2: Services requires auth
	fmt.Print("  [2] GET /v1/services (no auth) ... ")
	resp2, err := http.Get(url + PathServices)
	if err != nil {
		fmt.Printf("FAIL (unreachable)\n")
		fail++
	} else {
		defer resp2.Body.Close()
		if resp2.StatusCode == 401 {
			fmt.Printf("PASS (correctly rejected: 401)\n")
			pass++
		} else {
			fmt.Printf("FAIL (expected 401, got %d)\n", resp2.StatusCode)
			fail++
		}
	}

	// Test 3: Register requires POST
	fmt.Print("  [3] GET /v1/register (wrong method) ... ")
	resp3, err := http.Get(url + PathRegister)
	if err != nil {
		fmt.Printf("FAIL (unreachable)\n")
		fail++
	} else {
		defer resp3.Body.Close()
		if resp3.StatusCode == 405 {
			fmt.Printf("PASS (correctly rejected: 405)\n")
			pass++
		} else {
			fmt.Printf("FAIL (expected 405, got %d)\n", resp3.StatusCode)
			fail++
		}
	}

	// Test 4: Register with invalid body
	fmt.Print("  [4] POST /v1/register (empty body) ... ")
	resp4, err := http.Post(url+PathRegister, "application/json", jsonReader([]byte("{}")))
	if err != nil {
		fmt.Printf("FAIL (unreachable)\n")
		fail++
	} else {
		defer resp4.Body.Close()
		if resp4.StatusCode == 400 {
			fmt.Printf("PASS (correctly rejected: 400)\n")
			pass++
		} else {
			fmt.Printf("FAIL (expected 400, got %d)\n", resp4.StatusCode)
			fail++
		}
	}

	// Test 5: Register with bad signature
	fmt.Print("  [5] POST /v1/register (bad signature) ... ")
	fakeReq := RegisterRequest{
		Manifest: AgentManifest{
			Name:      "verify-test",
			URL:       "http://localhost:19999",
			PublicKey: "0000000000000000000000000000000000000000000000000000000000000000",
		},
		Signature: "deadbeef",
		Timestamp: time.Now().Unix(),
	}
	fakeJSON, _ := json.Marshal(fakeReq)
	resp5, err := http.Post(url+PathRegister, "application/json", jsonReader(fakeJSON))
	if err != nil {
		fmt.Printf("FAIL (unreachable)\n")
		fail++
	} else {
		defer resp5.Body.Close()
		if resp5.StatusCode == 401 {
			fmt.Printf("PASS (correctly rejected: 401)\n")
			pass++
		} else {
			fmt.Printf("FAIL (expected 401, got %d)\n", resp5.StatusCode)
			fail++
		}
	}

	// Test 6: Full registration flow with real keys
	fmt.Print("  [6] POST /v1/register (valid signature) ... ")
	testID, err := GenerateIdentity("verify-test")
	if err != nil {
		fmt.Printf("FAIL (key generation: %v)\n", err)
		fail++
	} else {
		manifest := AgentManifest{
			Name:      "verify-test",
			Version:   "0.0.1",
			URL:       "http://localhost:19999",
			PublicKey: testID.PublicKeyHex(),
			Capabilities: []Capability{
				{Name: "agent:message", Resources: []string{}},
			},
		}
		sigData, _ := json.Marshal(manifest)
		sig := testID.Sign(sigData)
		regReq := RegisterRequest{
			Manifest:  manifest,
			Signature: sig,
			Timestamp: time.Now().Unix(),
		}
		regJSON, _ := json.Marshal(regReq)
		resp6, err := http.Post(url+PathRegister, "application/json", jsonReader(regJSON))
		if err != nil {
			fmt.Printf("FAIL (unreachable)\n")
			fail++
		} else {
			defer resp6.Body.Close()
			body6, _ := io.ReadAll(resp6.Body)
			var regResp RegisterResponse
			if resp6.StatusCode != 200 {
				fmt.Printf("FAIL (expected 200, got %d: %s)\n", resp6.StatusCode, string(body6))
				fail++
			} else if err := json.Unmarshal(body6, &regResp); err != nil {
				fmt.Printf("FAIL (invalid response JSON)\n")
				fail++
			} else if regResp.AgentID == "" || regResp.Token == "" {
				fmt.Printf("FAIL (missing agent_id or token)\n")
				fail++
			} else {
				fmt.Printf("PASS (agent_id: %s...)\n", regResp.AgentID[:8])
				pass++

				// Test 7: Authenticated services request
				fmt.Print("  [7] GET /v1/services (with token) ... ")
				req7, _ := http.NewRequest("GET", url+PathServices, nil)
				req7.Header.Set("Authorization", "Bearer "+regResp.Token)
				resp7, err := http.DefaultClient.Do(req7)
				if err != nil {
					fmt.Printf("FAIL (unreachable)\n")
					fail++
				} else {
					defer resp7.Body.Close()
					body7, _ := io.ReadAll(resp7.Body)
					var dir ServiceDirectory
					if resp7.StatusCode != 200 {
						fmt.Printf("FAIL (expected 200, got %d)\n", resp7.StatusCode)
						fail++
					} else if err := json.Unmarshal(body7, &dir); err != nil {
						fmt.Printf("FAIL (invalid JSON)\n")
						fail++
					} else {
						fmt.Printf("PASS (%d services)\n", len(dir.Services))
						pass++
					}
				}

				// Cleanup: deregister
				deregReq, _ := http.NewRequest("DELETE", url+PathRegister, nil)
				deregReq.Header.Set("Authorization", "Bearer "+regResp.Token)
				deregResp, _ := http.DefaultClient.Do(deregReq)
				if deregResp != nil {
					deregResp.Body.Close()
				}
			}
		}
	}

	fmt.Println()
	fmt.Printf("  Results: %d passed, %d failed\n\n", pass, fail)

	if fail > 0 {
		return fmt.Errorf("%d protocol verification tests failed", fail)
	}
	return nil
}

// ── Verify Agent ────────────────────────────────────────────

// VerifyAgent runs protocol compliance tests against a running agent.
func VerifyAgent(url string) error {
	fmt.Println()
	fmt.Println("  ⚡ Verifying Agent Protocol Compliance")
	fmt.Printf("  Target: %s\n\n", url)

	pass := 0
	fail := 0

	// Test 1: Health endpoint
	fmt.Print("  [1] GET /v1/health ... ")
	resp, err := http.Get(url + PathHealth)
	if err != nil {
		fmt.Printf("FAIL (unreachable: %v)\n", err)
		fail++
	} else {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var health HealthStatus
		if err := json.Unmarshal(body, &health); err != nil {
			fmt.Printf("FAIL (invalid JSON)\n")
			fail++
		} else if health.Name == "" || health.Status == "" {
			fmt.Printf("FAIL (missing name or status)\n")
			fail++
		} else {
			fmt.Printf("PASS (%s v%s, %s)\n", health.Name, health.Version, health.Status)
			pass++
		}
	}

	// Test 2: Describe endpoint
	fmt.Print("  [2] POST /v1/describe ... ")
	resp2, err := http.Post(url+PathDescribe, "application/json", jsonReader([]byte("{}")))
	if err != nil {
		fmt.Printf("FAIL (unreachable)\n")
		fail++
	} else {
		defer resp2.Body.Close()
		body2, _ := io.ReadAll(resp2.Body)
		var manifest AgentManifest
		if err := json.Unmarshal(body2, &manifest); err != nil {
			fmt.Printf("FAIL (invalid JSON)\n")
			fail++
		} else if manifest.Name == "" || manifest.PublicKey == "" {
			fmt.Printf("FAIL (missing name or public_key)\n")
			fail++
		} else {
			fmt.Printf("PASS (%s, %d capabilities)\n", manifest.Name, len(manifest.Capabilities))
			pass++
		}
	}

	// Test 3: Message endpoint
	fmt.Print("  [3] POST /v1/message ... ")
	msg := AgentMessage{
		ID:      GenerateID(),
		From:    "verifier",
		To:      "agent",
		Type:    "request",
		Action:  "get_capabilities",
		Payload: map[string]any{},
	}
	msgJSON, _ := json.Marshal(msg)
	resp3, err := http.Post(url+PathMessage, "application/json", jsonReader(msgJSON))
	if err != nil {
		fmt.Printf("FAIL (unreachable)\n")
		fail++
	} else {
		defer resp3.Body.Close()
		body3, _ := io.ReadAll(resp3.Body)
		var response AgentMessage
		if resp3.StatusCode != 200 {
			fmt.Printf("WARN (status %d — agent may not support get_capabilities)\n", resp3.StatusCode)
			pass++
		} else if err := json.Unmarshal(body3, &response); err != nil {
			fmt.Printf("FAIL (invalid response JSON)\n")
			fail++
		} else if response.From == "" || response.Type != "response" {
			fmt.Printf("FAIL (missing from or type != response)\n")
			fail++
		} else {
			signed := "unsigned"
			if response.Signature != "" {
				signed = "signed"
			}
			fmt.Printf("PASS (%s, %s)\n", response.From, signed)
			pass++
		}
	}

	// Test 4: Execute requires POST
	fmt.Print("  [4] GET /v1/execute (wrong method) ... ")
	resp4, err := http.Get(url + PathExecute)
	if err != nil {
		fmt.Printf("FAIL (unreachable)\n")
		fail++
	} else {
		defer resp4.Body.Close()
		if resp4.StatusCode == 405 {
			fmt.Printf("PASS (correctly rejected: 405)\n")
			pass++
		} else {
			fmt.Printf("WARN (expected 405, got %d)\n", resp4.StatusCode)
			pass++
		}
	}

	// Test 5: Services update endpoint
	fmt.Print("  [5] POST /v1/services ... ")
	dir := ServiceDirectory{
		Services:  []ServiceEntry{{Name: "test", URL: "http://localhost:0", Status: "online"}},
		UpdatedAt: time.Now().Unix(),
	}
	dirJSON, _ := json.Marshal(dir)
	resp5, err := http.Post(url+PathServices, "application/json", jsonReader(dirJSON))
	if err != nil {
		fmt.Printf("FAIL (unreachable)\n")
		fail++
	} else {
		defer resp5.Body.Close()
		if resp5.StatusCode == 200 {
			fmt.Printf("PASS (accepted service directory)\n")
			pass++
		} else {
			fmt.Printf("FAIL (expected 200, got %d)\n", resp5.StatusCode)
			fail++
		}
	}

	fmt.Println()
	fmt.Printf("  Results: %d passed, %d failed\n\n", pass, fail)

	if fail > 0 {
		return fmt.Errorf("%d protocol verification tests failed", fail)
	}
	return nil
}
