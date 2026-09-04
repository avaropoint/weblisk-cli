package operator

// A programmatic interface to operator identity, for a caller that is not a
// person at a terminal.
//
// architecture/admin's bootstrap flow reads "Run: weblisk operator init &&
// weblisk operator register". That is the right instruction for a shell and no
// instruction at all for a console provisioning a tenant on behalf of a
// signed-in user — the case protocol/identity names when it says "a runtime
// without [a terminal] cannot, and a specification that says prompt has
// excluded it".
//
// The passphrase is a PARAMETER here, supplied by the caller from a non-echoing
// channel it operates. It is never read from argv — see RefusePassphraseInArgv
// — and never written to disk.

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
)

// ErrAlreadyRegistered reports that this orchestrator already holds a record
// for the operator.
//
// Distinguished because provisioning must be RE-RUNNABLE: a console retrying
// after a network error must not be told the tenant is broken by the step that
// already succeeded.
var ErrAlreadyRegistered = fmt.Errorf("operator already registered with this orchestrator")

// ErrNeedsExistingAdmin reports a hub that already has operators.
//
// architecture/admin auto-approves the FIRST operator and requires an existing
// admin for every later one. So this is the specification working, not a fault,
// and the message says who can act rather than what went wrong.
var ErrNeedsExistingAdmin = fmt.Errorf("this hub already has operators")

// IsAlreadyRegistered reports whether an error is that condition.
func IsAlreadyRegistered(err error) bool {
	return err != nil && (strings.Contains(err.Error(), ErrAlreadyRegistered.Error()) ||
		strings.Contains(strings.ToLower(err.Error()), "already exists") ||
		strings.Contains(strings.ToLower(err.Error()), "already registered"))
}

// decryptWith opens an encrypted key file with a supplied passphrase, and
// caches the result so a later signing call does not prompt.
//
// The programmatic path must never prompt: a console has no terminal to prompt
// on, and decryptPrivateKey's interactive read would block or consume a caller's
// stdin.
func decryptWith(privPath, passphrase string) (*mldsa65.PrivateKey, error) {
	data, err := os.ReadFile(privPath)
	if err != nil {
		return nil, fmt.Errorf("reading the private key: %w", err)
	}
	privBytes, err := decryptKey(data, passphrase)
	if err != nil {
		return nil, fmt.Errorf("wrong passphrase, or the key file is corrupt: %w", err)
	}
	var key mldsa65.PrivateKey
	if err := key.UnmarshalBinary(privBytes); err != nil {
		return nil, fmt.Errorf("parsing the private key: %w", err)
	}
	// Seed the process cache so RequestToken signs without asking.
	cachedOnce.Do(func() { cachedKey, cachedKeyE = &key, nil })
	return &key, nil
}

// EnsureIdentity creates an operator identity if none exists, and verifies the
// passphrase opens it if one does. Reports whether it created one.
//
// It does NOT regenerate over an existing identity. A subject holds one
// identity and a separate credential attested by each hub, so provisioning a
// second tenant must reuse the identity — minting a new key would silently
// orphan the grant every earlier tenant recorded.
func EnsureIdentity(name, passphrase string) (created bool, err error) {
	if len(passphrase) < 12 {
		return false, fmt.Errorf("passphrase must be at least 12 characters")
	}
	dir := keysDirectory()
	privPath := filepath.Join(dir, "operator.key")

	if _, statErr := os.Stat(privPath); statErr == nil {
		// Present: prove the passphrase opens it, so a wrong one fails here
		// rather than three steps later as a signature that will not verify.
		if _, derr := decryptWith(privPath, passphrase); derr != nil {
			return false, fmt.Errorf("an identity exists at %s and the passphrase does not open it: %w", dir, derr)
		}
		return false, nil
	}

	// Seeded below via decryptWith, so a caller that creates an identity and
	// then signs with it is not asked for the passphrase twice. It was: the
	// prompt appeared, then RequestToken's loadPrivateKey found an empty cache
	// and asked again — and with input piped the second read found nothing.
	pub, priv, gerr := mldsa65.GenerateKey(rand.Reader)
	if gerr != nil {
		return false, fmt.Errorf("generating ML-DSA-65 key pair: %w", gerr)
	}
	privBytes, merr := priv.MarshalBinary()
	if merr != nil {
		return false, merr
	}
	pubBytes, merr := pub.MarshalBinary()
	if merr != nil {
		return false, merr
	}
	enc, eerr := encryptKey(privBytes, passphrase)
	if eerr != nil {
		return false, fmt.Errorf("encrypting the private key: %w", eerr)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	if err := os.WriteFile(privPath, enc, 0o600); err != nil {
		return false, err
	}
	pubB64 := base64.RawURLEncoding.EncodeToString(pubBytes)
	if err := os.WriteFile(filepath.Join(dir, "operator.pub"), []byte(pubB64), 0o644); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(dir, "operator.name"), []byte(name), 0o644); err != nil {
		return false, err
	}
	if _, derr := decryptWith(privPath, passphrase); derr != nil {
		return true, fmt.Errorf("the identity was written and could not be read back: %w", derr)
	}
	return true, nil
}

// RegisterWith registers the operator identity with an orchestrator.
//
// Per architecture/admin the first operator is auto-approved and every later
// one requires an existing admin. The response may or may not carry a token,
// which is why obtaining one is a separate call.
func RegisterWith(orchURL, name, passphrase string) error {
	if terr := CheckCredentialTransport(orchURL); terr != nil {
		return terr
	}
	dir := keysDirectory()
	pubB64, err := os.ReadFile(filepath.Join(dir, "operator.pub"))
	if err != nil {
		return fmt.Errorf("reading the public key: %w", err)
	}
	privKey, err := decryptWith(filepath.Join(dir, "operator.key"), passphrase)
	if err != nil {
		return fmt.Errorf("unlocking the private key: %w", err)
	}

	// The registration is SIGNED. architecture/admin requires
	// name, public_key and signature = sign(canonicalize({name, timestamp})),
	// and the orchestrator verifies the signature against the submitted key
	// before it records anything.
	//
	// This sent name and public_key only, and a conformant hub refused it:
	//
	//	400 operator registration missing required field: signature
	//
	// Without the signature the request proves nothing — anyone who can reach
	// the endpoint could register any public key under any name, and the first
	// such registration at a fresh hub is auto-approved as its bootstrap admin.
	// The blueprint has always said so; this code did not read it.
	//
	// Signed the same way and over the same payload as RequestToken, so a hub
	// verifies both with one code path and neither can drift from the other.
	pub := strings.TrimSpace(string(pubB64))
	const role = "admin"
	challenge, err := registrationChallenge(name, pub, role)
	if err != nil {
		return err
	}
	var sig [mldsa65.SignatureSize]byte
	if err := mldsa65.SignTo(privKey, challenge.canonical, nil, false, sig[:]); err != nil {
		return fmt.Errorf("signing the registration: %w", err)
	}
	body, err := json.Marshal(map[string]any{
		"name":       name,
		"public_key": pub,
		"role":       role,
		"timestamp":  challenge.timestamp,
		"signature":  base64.RawURLEncoding.EncodeToString(sig[:]),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(orchURL, "/")+"/v1/admin/operators/register", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == 200 || resp.StatusCode == 201:
		return nil
	case resp.StatusCode == 409:
		return ErrAlreadyRegistered
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		// The status alone does not say WHICH refusal this is, and guessing was
		// wrong in the field: an invalid signature also answers 403, and this
		// reported "this hub already has operators, an existing admin must add
		// you" about a hub with no operators at all. The person then waits for
		// an approval nobody needs to give.
		//
		// The orchestrator sends a structured ErrorResponse with a code. Read
		// it, and only claim the authorization case when the hub says so.
		if code := errorCodeOf(raw); code == "INVALID_SIGNATURE" || code == "INVALID_REQUEST" {
			return fmt.Errorf("the orchestrator refused the registration: %s", errorMessageOf(raw))
		}
		return ErrNeedsExistingAdmin
	default:
		return fmt.Errorf("registration refused (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
}

// signedChallenge is the payload an operator signs, and the timestamp inside it.
//
// The timestamp is returned alongside so the caller sends the SAME value it
// signed. Reading the clock twice — once to build the payload and once to build
// the request — produces a signature over a payload the hub never sees, and it
// verifies only when both reads land in the same second.
type signedChallenge struct {
	canonical []byte
	timestamp int64
}

// registrationChallenge builds
// canonicalize({name, public_key, role, timestamp}).
//
// Canonical form is RFC 8785 (JCS): a Go map marshals with sorted keys, which
// is the ordering JCS requires for an object of scalars.
//
// # Why the whole payload and not the {name, timestamp} challenge
//
// architecture/admin now states it exactly, and the reason is that the
// signature must BIND THE PUBLIC KEY TO THE NAME. Over {name, timestamp} the
// key is unsigned data in a signed request, so anyone able to reach the endpoint
// could submit their own key under any name — and at a hub with no operators
// yet, the first such registration is auto-approved as its bootstrap admin.
// `role` is in for the same reason: it is a privilege claim and must not be
// substitutable in flight.
//
// This signed the two-field form while a conformant orchestrator verified the
// four-field one, and the connection failed with "operator signature
// verification failed". The blueprint said "<signed registration payload>" —
// prose where the token request beside it had a formula — so both sides read it
// and neither was wrong.
func registrationChallenge(name, publicKey, role string) (signedChallenge, error) {
	ts := time.Now().Unix()
	b, err := json.Marshal(map[string]any{
		"name":       name,
		"public_key": publicKey,
		"role":       role,
		"timestamp":  ts,
	})
	if err != nil {
		return signedChallenge{}, err
	}
	return signedChallenge{canonical: b, timestamp: ts}, nil
}

// errorResponse is the shape protocol/types specifies for a refusal.
type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// errorCodeOf reads the code a hub gave for a refusal, "" if it gave none.
//
// A status code says what kind of answer this is; the error code says WHICH
// answer. Reporting a cause the server did not state is worse than reporting
// none — the reader acts on it.
func errorCodeOf(body []byte) string {
	var e errorResponse
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(e.Code))
}

// errorMessageOf reads the sentence a hub gave, falling back to the raw body.
func errorMessageOf(body []byte) string {
	var e errorResponse
	if json.Unmarshal(body, &e) == nil && strings.TrimSpace(e.Error) != "" {
		return strings.TrimSpace(e.Error)
	}
	return strings.TrimSpace(string(body))
}
