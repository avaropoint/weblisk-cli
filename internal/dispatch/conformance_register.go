package dispatch

// conformance_register.go — the mock agent, and the three registration tests.
//
// L1-03, L1-04 and L1-05 were reported `[unrun]` for as long as the runner has
// existed. Unrun is honest — it is not a pass — but three of the ten L1 tests
// having no harness means "conforms" has only ever meant "conforms in the
// seven we can check", and registration is the one flow every other capability
// depends on: an orchestrator that cannot admit an agent has nothing to route.
//
// All three need the same thing, which is why they were unrun together: an
// ML-DSA-65 key pair and a manifest signed with it. That is one harness.
//
// # Why the manifest bytes are built once and reused
//
// protocol/spec is normative that a verifier canonicalizes the manifest **as it
// arrived on the wire**, because parsing into a local struct drops unknown
// fields and then verifies a digest that was never signed. The mirror of that
// rule applies to a signer: if this harness signed one serialisation and sent
// another, every run would report a correct orchestrator as rejecting a valid
// signature.
//
// So the manifest is marshalled ONCE, those exact bytes are what is signed, and
// the same bytes go on the wire as a json.RawMessage. There is no second
// serialisation to disagree with the first.

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
)

// mockAgent is a throwaway identity that registers once and is never reused.
type mockAgent struct {
	name string
	priv *mldsa65.PrivateKey
	// manifest is the exact bytes signed and sent. Never re-marshalled.
	manifest json.RawMessage
}

// newMockAgent mints a key and builds the minimal manifest protocol/types
// requires: name, version, description, url, public_key and capabilities.
//
// Minimal on purpose. A manifest carrying extra fields tests the orchestrator's
// unknown-field handling as well as its signature check, and a failure would
// not say which of the two was wrong.
func newMockAgent(name string) (*mockAgent, error) {
	pub, priv, err := mldsa65.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating a key for the mock agent: %w", err)
	}
	pubBytes, err := pub.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("encoding the mock agent's public key: %w", err)
	}

	// A map, so encoding/json sorts the keys — which for these ASCII names is
	// the ordering RFC 8785 specifies. Values are strings and string arrays
	// only: JCS's number rules are where a hand-rolled canonicaliser goes
	// wrong, and this manifest has no need of a number.
	manifest := map[string]any{
		"name":        name,
		"type":        "agent",
		"version":     "1.0.0",
		"description": "conformance harness — registers once and is never used",
		"url":         "http://127.0.0.1:1/unreachable-by-design",
		"public_key":  base64.RawURLEncoding.EncodeToString(pubBytes),
		"capabilities": []any{
			map[string]any{"name": "conformance:probe"},
		},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encoding the mock agent's manifest: %w", err)
	}
	return &mockAgent{name: name, priv: priv, manifest: raw}, nil
}

// registerBody builds the wire body.
//
// sign says what to sign: the manifest as sent (a request that must be
// accepted), or something else (a request that must be refused). The bad
// signature is produced by signing DIFFERENT BYTES with the real key rather
// than by corrupting a signature — a corrupted one can fail as a malformed
// signature, which is a different refusal from an invalid one, and L1-04 is
// about the second.
func (m *mockAgent) registerBody(timestamp int64, sign []byte) ([]byte, error) {
	sig, err := m.priv.Sign(rand.Reader, sign, nil)
	if err != nil {
		return nil, fmt.Errorf("signing the registration: %w", err)
	}
	return json.Marshal(map[string]any{
		"manifest":  m.manifest,
		"signature": base64.RawURLEncoding.EncodeToString(sig),
		"timestamp": timestamp,
	})
}

// postRegister sends one registration and returns the status and body.
func postRegister(base string, body []byte) (int, []byte, error) {
	req, err := http.NewRequest("POST", strings.TrimRight(base, "/")+"/v1/register", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, out, nil
}

// hex32 is what protocol/spec means by an ID: 32 hex characters, 16 random bytes.
var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// wltFault reports why a token is not a WLT, or "" when it is one.
//
// # The first version of this was wrong, and wrong in the direction that fails
//
// It asserted strings.HasPrefix(tok, "WLT"). A WLT does not begin with "WLT":
// protocol/identity says the format is "structurally identical to JSON Web
// Tokens" — three base64url segments, no padding, with the type carried INSIDE
// the header as `typ`. So the check rejected every correct token, and its
// failure message printed a truncated base64 blob that looked like evidence of
// a malformed token when it was evidence of a malformed test.
//
// A conformance assertion invented from the name of a thing rather than read
// from its specification fails the implementations that are right. That is
// worse than no check: no check is an honest gap, and this refused a build.
//
// What it checks now comes from protocol/identity's Header section:
//
//	structure  exactly three non-empty dot-separated parts
//	encoding   base64url WITHOUT padding, per RFC 4648 §5
//	typ        exactly "WLT" — the field that prevents cross-system use
//	alg        exactly "ML-DSA-65", which verifiers MUST check
//
// The alg check earns its place. A generated orchestrator emitted
// `{"alg":"ml-dsa-65"}` from a lowercase constant and verified against the same
// constant, so it was self-consistent and would reject a conformant peer's
// token while having its own rejected — undetectable from inside that tenant,
// and exactly what a conformance suite is for.
func wltFault(tok string) string {
	if tok == "" {
		return "no token was issued"
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return fmt.Sprintf("has %d dot-separated part(s), and a WLT has 3", len(parts))
	}
	for i, p := range parts {
		if p == "" {
			return fmt.Sprintf("part %d is empty", i+1)
		}
		if strings.Contains(p, "=") {
			return "is base64url WITH padding; protocol/identity requires none"
		}
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "the header is not unpadded base64url: " + err.Error()
	}
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return "the header is not JSON: " + err.Error()
	}
	if header.Typ != "WLT" {
		return fmt.Sprintf("header typ is %q, and protocol/identity requires exactly \"WLT\"", header.Typ)
	}
	if header.Alg != "ML-DSA-65" {
		return fmt.Sprintf("header alg is %q, and protocol/identity requires exactly \"ML-DSA-65\" "+
			"— a verifier MUST reject anything else, so this tenant and a conformant peer would refuse each other", header.Alg)
	}
	return ""
}

// errorField pulls the error message out of a refusal, per protocol/spec's
// "at minimum {\"error\": \"message\"}".
func errorField(body []byte) string {
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	switch v := envelope["error"].(type) {
	case string:
		return v
	case map[string]any:
		// A richer ErrorResponse. The message is what a person reads.
		if msg, ok := v["message"].(string); ok {
			return msg
		}
		if code, ok := v["code"].(string); ok {
			return code
		}
	}
	return ""
}

// registrationTests are L1-03, L1-04 and L1-05.
//
// Each mints its own agent. Sharing one across the three would make L1-03's
// successful registration a precondition of the other two — a name already
// taken, a namespace already owned — so a failure in the first would report as
// three failures with two of them wrong.
var registrationTests = []conformanceTest{
	{
		id: "L1-03", name: "Registration",
		applies: map[string]bool{"orchestrator": true},
		run: func(base, component string, blueprints map[string]string) (bool, string, string) {
			agent, err := newMockAgent("conformance-l1-03")
			if err != nil {
				return false, err.Error(), ""
			}
			body, err := agent.registerBody(time.Now().Unix(), agent.manifest)
			if err != nil {
				return false, err.Error(), ""
			}
			code, resp, err := postRegister(base, body)
			if err != nil {
				return false, "the registration could not be sent: " + err.Error(), ""
			}
			if code != 200 {
				detail := fmt.Sprintf("POST /v1/register answered %d", code)
				if msg := errorField(resp); msg != "" {
					detail += ": " + msg
				}
				return false, detail, ""
			}
			var out struct {
				AgentID   string `json:"agent_id"`
				Token     string `json:"token"`
				ExpiresAt int64  `json:"expires_at"`
				Services  any    `json:"services"`
			}
			if err := json.Unmarshal(resp, &out); err != nil {
				return false, "the response is not JSON", ""
			}
			var missing []string
			if out.AgentID == "" {
				missing = append(missing, "agent_id")
			}
			if out.Token == "" {
				missing = append(missing, "token")
			}
			if out.ExpiresAt == 0 {
				missing = append(missing, "expires_at")
			}
			if out.Services == nil {
				missing = append(missing, "services")
			}
			if len(missing) > 0 {
				return false, "the response is missing " + strings.Join(missing, ", "), ""
			}
			// Reported with what was found, because "agent_id must be 32 hex
			// chars, got 28" is triageable and "L1-03 failed" is not.
			if !hex32.MatchString(out.AgentID) {
				return false, fmt.Sprintf("agent_id must be 32 hex chars, got %d (%q)", len(out.AgentID), out.AgentID), ""
			}
			if fault := wltFault(out.Token); fault != "" {
				return false, "token is not WLT format: " + fault, ""
			}
			return true, "", fmt.Sprintf("registered as %s with a WLT token expiring at %d", out.AgentID, out.ExpiresAt)
		},
	},
	{
		id: "L1-04", name: "Registration Rejects Bad Signature",
		applies: map[string]bool{"orchestrator": true},
		run: func(base, component string, blueprints map[string]string) (bool, string, string) {
			agent, err := newMockAgent("conformance-l1-04")
			if err != nil {
				return false, err.Error(), ""
			}
			// A real signature by the real key over the WRONG bytes. This is
			// what a signature that does not cover the manifest looks like, and
			// it is the thing the check must catch — not a mangled base64 blob,
			// which any parser rejects before verification is reached.
			body, err := agent.registerBody(time.Now().Unix(), []byte(`{"not":"the manifest"}`))
			if err != nil {
				return false, err.Error(), ""
			}
			code, resp, err := postRegister(base, body)
			if err != nil {
				return false, "the registration could not be sent: " + err.Error(), ""
			}
			if code == 200 {
				return false, "a registration signed over different bytes was ACCEPTED — the signature is not being verified", ""
			}
			if code != 401 {
				return false, fmt.Sprintf("a bad signature answered %d; architecture/testing requires 401", code), ""
			}
			if errorField(resp) == "" {
				return false, "the refusal carries no error field", ""
			}
			return true, "", "a signature over other bytes is refused with 401 and an error field"
		},
	},
	{
		id: "L1-05", name: "Registration Rejects Stale Timestamp",
		applies: map[string]bool{"orchestrator": true},
		run: func(base, component string, blueprints map[string]string) (bool, string, string) {
			agent, err := newMockAgent("conformance-l1-05")
			if err != nil {
				return false, err.Error(), ""
			}
			// 600 seconds, twice the 300-second window protocol/spec states.
			stale := time.Now().Unix() - 600
			body, err := agent.registerBody(stale, agent.manifest)
			if err != nil {
				return false, err.Error(), ""
			}
			code, resp, err := postRegister(base, body)
			if err != nil {
				return false, "the registration could not be sent: " + err.Error(), ""
			}
			if code == 200 {
				return false, "a registration 600 seconds old was ACCEPTED — there is no replay window", ""
			}
			if code != 401 {
				return false, fmt.Sprintf("a stale timestamp answered %d; architecture/testing requires 401", code), ""
			}
			msg := strings.ToLower(errorField(resp))
			if msg == "" {
				return false, "the refusal carries no error field", ""
			}
			// The test requires the reason be NAMED. A stale request refused
			// with "unauthorized" is indistinguishable from a bad key, and the
			// operator debugs the wrong thing.
			if !strings.Contains(msg, "replay") && !strings.Contains(msg, "timestamp") &&
				!strings.Contains(msg, "stale") && !strings.Contains(msg, "expired") {
				return false, fmt.Sprintf("the refusal does not mention replay or timestamp: %q", truncate(msg, 60)), ""
			}
			return true, "", "a 600-second-old registration is refused with 401 naming the replay window"
		},
	},
}
