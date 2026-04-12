package protocol

// ── Cryptographic Identity ──────────────────────────────────
//
// Ed25519 key pairs for agent identity. Every agent generates a
// keypair on first run and stores it. All messages are signed.
//
// Token format (simple, secure, no dependencies):
//   base64url(header).base64url(payload).base64url(signature)
//   header:    {"alg":"Ed25519","typ":"WLT"}
//   payload:   JSON claims
//   signature: Ed25519 sign(header.payload)

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ── Key Management ──────────────────────────────────────────

// Identity holds an agent's Ed25519 key pair.
type Identity struct {
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
	Name       string
}

// GenerateIdentity creates a new Ed25519 key pair.
func GenerateIdentity(name string) (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating key pair: %w", err)
	}
	return &Identity{PublicKey: pub, PrivateKey: priv, Name: name}, nil
}

// PublicKeyHex returns the hex-encoded public key for protocol exchange.
func (id *Identity) PublicKeyHex() string {
	return hex.EncodeToString(id.PublicKey)
}

// SaveKeys writes the key pair to disk in a secure directory.
func (id *Identity) SaveKeys(dir string) error {
	keysDir := filepath.Join(dir, ".weblisk", "keys")
	if err := os.MkdirAll(keysDir, 0700); err != nil {
		return err
	}
	privPath := filepath.Join(keysDir, id.Name+".key")
	pubPath := filepath.Join(keysDir, id.Name+".pub")

	if err := os.WriteFile(privPath, []byte(hex.EncodeToString(id.PrivateKey)), 0600); err != nil {
		return err
	}
	return os.WriteFile(pubPath, []byte(hex.EncodeToString(id.PublicKey)), 0644)
}

// LoadIdentity loads a key pair from disk, or generates a new one if absent.
func LoadIdentity(name, dir string) (*Identity, error) {
	keysDir := filepath.Join(dir, ".weblisk", "keys")
	privPath := filepath.Join(keysDir, name+".key")

	data, err := os.ReadFile(privPath)
	if err != nil {
		if os.IsNotExist(err) {
			id, genErr := GenerateIdentity(name)
			if genErr != nil {
				return nil, genErr
			}
			if saveErr := id.SaveKeys(dir); saveErr != nil {
				return nil, saveErr
			}
			return id, nil
		}
		return nil, err
	}

	privBytes, err := hex.DecodeString(string(data))
	if err != nil {
		return nil, fmt.Errorf("decoding private key: %w", err)
	}
	priv := ed25519.PrivateKey(privBytes)
	pub := priv.Public().(ed25519.PublicKey)
	return &Identity{PublicKey: pub, PrivateKey: priv, Name: name}, nil
}

// ── Message Signing ─────────────────────────────────────────

// Sign produces an Ed25519 signature of the given data.
func (id *Identity) Sign(data []byte) string {
	sig := ed25519.Sign(id.PrivateKey, data)
	return hex.EncodeToString(sig)
}

// SignJSON signs a JSON-serializable payload and returns the hex signature.
func (id *Identity) SignJSON(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return id.Sign(data), nil
}

// VerifySignature checks an Ed25519 signature against a public key.
func VerifySignature(pubKeyHex, signatureHex string, data []byte) bool {
	pubKey, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(pubKey) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(signatureHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pubKey, data, sig)
}

// ── Token System ────────────────────────────────────────────

var b64 = base64.RawURLEncoding

type tokenHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// TokenClaims are the payload of an auth token.
type TokenClaims struct {
	Subject      string   `json:"sub"`       // agent name
	Issuer       string   `json:"iss"`       // "orchestrator" or agent name
	IssuedAt     int64    `json:"iat"`
	ExpiresAt    int64    `json:"exp"`
	Capabilities []string `json:"cap,omitempty"` // granted capabilities
	ChannelID    string   `json:"cid,omitempty"` // for channel-scoped tokens
}

// CreateToken issues a signed token with the given claims.
func (id *Identity) CreateToken(claims TokenClaims) (string, error) {
	header := tokenHeader{Alg: "Ed25519", Typ: "WLT"}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	headerB64 := b64.EncodeToString(headerJSON)
	payloadB64 := b64.EncodeToString(payloadJSON)
	signingInput := headerB64 + "." + payloadB64

	sig := ed25519.Sign(id.PrivateKey, []byte(signingInput))
	sigB64 := b64.EncodeToString(sig)

	return signingInput + "." + sigB64, nil
}

// VerifyToken validates a token's signature and expiry.
// Returns the claims if valid, error otherwise.
func VerifyToken(token, issuerPubKeyHex string) (*TokenClaims, error) {
	// Split into 3 parts.
	parts := splitToken(token)
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed token: expected 3 parts, got %d", len(parts))
	}

	// Decode header.
	headerJSON, err := b64.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decoding header: %w", err)
	}
	var header tokenHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("parsing header: %w", err)
	}
	if header.Alg != "Ed25519" {
		return nil, fmt.Errorf("unsupported algorithm: %s", header.Alg)
	}

	// Verify signature.
	signingInput := parts[0] + "." + parts[1]
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decoding signature: %w", err)
	}
	pubKey, err := hex.DecodeString(issuerPubKeyHex)
	if err != nil || len(pubKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key")
	}
	if !ed25519.Verify(pubKey, []byte(signingInput), sig) {
		return nil, fmt.Errorf("invalid signature")
	}

	// Decode and validate claims.
	payloadJSON, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding payload: %w", err)
	}
	var claims TokenClaims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, fmt.Errorf("parsing claims: %w", err)
	}
	if claims.ExpiresAt > 0 && time.Now().Unix() > claims.ExpiresAt {
		return nil, fmt.Errorf("token expired")
	}

	return &claims, nil
}

// splitToken splits a "x.y.z" token into parts without strings import.
func splitToken(token string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			parts = append(parts, token[start:i])
			start = i + 1
		}
	}
	parts = append(parts, token[start:])
	return parts
}

// ── Helpers ─────────────────────────────────────────────────

// GenerateID creates a random hex ID for tasks, channels, etc.
func GenerateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TokenTTL is the default token lifetime.
const TokenTTL = 24 * time.Hour
