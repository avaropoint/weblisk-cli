package protocol

// Cryptographic Identity
//
// ML-DSA-65 key pairs for agent identity. Every agent generates a
// keypair on first run and stores it. All messages are signed.
//
// Token format (FIPS 204 compliant):
//   base64url(header).base64url(payload).base64url(signature)
//   header:    {"alg":"ML-DSA-65","typ":"WLT"}
//   payload:   JSON claims
//   signature: ML-DSA-65 sign(header.payload)

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
)

// Key Management

// Identity holds an agent's ML-DSA-65 key pair.
type Identity struct {
	PublicKey  *mldsa65.PublicKey
	PrivateKey *mldsa65.PrivateKey
	Name       string
}

// GenerateIdentity creates a new ML-DSA-65 key pair.
func GenerateIdentity(name string) (*Identity, error) {
	pub, priv, err := mldsa65.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating key pair: %w", err)
	}
	return &Identity{PublicKey: pub, PrivateKey: priv, Name: name}, nil
}

// PublicKeyB64 returns the base64url-encoded public key for protocol exchange.
func (id *Identity) PublicKeyB64() string {
	packed, _ := id.PublicKey.MarshalBinary()
	return b64.EncodeToString(packed)
}

// PublicKeyHex returns the hex-encoded public key (legacy compat helper).
func (id *Identity) PublicKeyHex() string {
	packed, _ := id.PublicKey.MarshalBinary()
	return hex.EncodeToString(packed)
}

// SaveKeys writes the key pair to disk in a secure directory.
func (id *Identity) SaveKeys(dir string) error {
	keysDir := filepath.Join(dir, ".weblisk", "keys")
	if err := os.MkdirAll(keysDir, 0700); err != nil {
		return err
	}
	privPath := filepath.Join(keysDir, id.Name+".key")
	pubPath := filepath.Join(keysDir, id.Name+".pub")

	privBytes, _ := id.PrivateKey.MarshalBinary()
	pubBytes, _ := id.PublicKey.MarshalBinary()

	if err := os.WriteFile(privPath, []byte(b64.EncodeToString(privBytes)), 0600); err != nil {
		return err
	}
	return os.WriteFile(pubPath, []byte(b64.EncodeToString(pubBytes)), 0644)
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

	privBytes, err := b64.DecodeString(string(data))
	if err != nil {
		return nil, fmt.Errorf("decoding private key: %w", err)
	}
	var priv mldsa65.PrivateKey
	if err := priv.UnmarshalBinary(privBytes); err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}
	pub := priv.Public().(*mldsa65.PublicKey)
	return &Identity{PublicKey: pub, PrivateKey: &priv, Name: name}, nil
}

// Message Signing

// Sign produces an ML-DSA-65 signature of the given data (base64url encoded).
func (id *Identity) Sign(data []byte) string {
	var sig [mldsa65.SignatureSize]byte
	_ = mldsa65.SignTo(id.PrivateKey, data, nil, false, sig[:])
	return b64.EncodeToString(sig[:])
}

// SignJSON signs a JSON-serializable payload and returns the base64url signature.
func (id *Identity) SignJSON(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return id.Sign(data), nil
}

// VerifySignature checks an ML-DSA-65 signature against a public key.
func VerifySignature(pubKeyB64, signatureB64 string, data []byte) bool {
	pubBytes, err := b64.DecodeString(pubKeyB64)
	if err != nil || len(pubBytes) != mldsa65.PublicKeySize {
		return false
	}
	var pub mldsa65.PublicKey
	if err := pub.UnmarshalBinary(pubBytes); err != nil {
		return false
	}
	sigBytes, err := b64.DecodeString(signatureB64)
	if err != nil || len(sigBytes) != mldsa65.SignatureSize {
		return false
	}
	return mldsa65.Verify(&pub, data, nil, sigBytes)
}

// Token System

var b64 = base64.RawURLEncoding

type tokenHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// TokenClaims are the payload of an auth token.
type TokenClaims struct {
	Subject      string   `json:"sub"` // agent name
	Issuer       string   `json:"iss"` // "orchestrator" or agent name
	IssuedAt     int64    `json:"iat"`
	ExpiresAt    int64    `json:"exp"`
	Capabilities []string `json:"cap,omitempty"` // granted capabilities
	ChannelID    string   `json:"cid,omitempty"` // for channel-scoped tokens
}

// CreateToken issues a signed token with the given claims.
func (id *Identity) CreateToken(claims TokenClaims) (string, error) {
	header := tokenHeader{Alg: "ML-DSA-65", Typ: "WLT"}

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

	var sig [mldsa65.SignatureSize]byte
	_ = mldsa65.SignTo(id.PrivateKey, []byte(signingInput), nil, false, sig[:])
	sigB64 := b64.EncodeToString(sig[:])

	return signingInput + "." + sigB64, nil
}

// VerifyToken validates a token's signature and expiry.
// Returns the claims if valid, error otherwise.
func VerifyToken(token, issuerPubKeyB64 string) (*TokenClaims, error) {
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
	if header.Alg != "ML-DSA-65" {
		return nil, fmt.Errorf("unsupported algorithm: %s", header.Alg)
	}

	// Verify signature.
	signingInput := parts[0] + "." + parts[1]
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decoding signature: %w", err)
	}
	pubBytes, err := b64.DecodeString(issuerPubKeyB64)
	if err != nil || len(pubBytes) != mldsa65.PublicKeySize {
		return nil, fmt.Errorf("invalid public key")
	}
	var pub mldsa65.PublicKey
	if err := pub.UnmarshalBinary(pubBytes); err != nil {
		return nil, fmt.Errorf("invalid public key format")
	}
	if !mldsa65.Verify(&pub, []byte(signingInput), nil, sig) {
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

// Helpers

// GenerateID creates a random hex ID for tasks, channels, etc.
func GenerateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TokenTTL is the default token lifetime.
const TokenTTL = 24 * time.Hour
