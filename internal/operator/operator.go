package operator

// Operator identity management — ML-DSA-65 (FIPS 204) key generation,
// Argon2id KDF, and orchestrator registration per identity.md spec.

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	"golang.org/x/crypto/argon2"
	"golang.org/x/term"
)

// Handle dispatches operator subcommands.
func Handle(args []string) error {
	if len(args) == 0 {
		PrintHelp()
		return nil
	}

	switch args[0] {
	case "init":
		return handleInit(args[1:])
	case "register":
		return handleRegister(args[1:])
	case "token":
		return handleToken(args[1:])
	case "rotate":
		return handleRotate(args[1:])
	case "help", "--help", "-h":
		PrintHelp()
		return nil
	default:
		return fmt.Errorf("unknown operator command: %s\n  Run 'weblisk operator help' for usage", args[0])
	}
}

func handleInit(args []string) error {
	force := false
	name := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--force":
			force = true
		case args[i] == "--name" && i+1 < len(args):
			i++
			name = args[i]
		case strings.HasPrefix(args[i], "--name="):
			name = strings.SplitN(args[i], "=", 2)[1]
		}
	}

	if name == "" {
		name = os.Getenv("USER")
		if name == "" {
			name = "operator"
		}
	}

	keysDir := keysDirectory()
	privPath := filepath.Join(keysDir, "operator.key")
	pubPath := filepath.Join(keysDir, "operator.pub")

	// Check existing keys
	if _, err := os.Stat(privPath); err == nil && !force {
		pub, _ := os.ReadFile(pubPath)
		fmt.Println()
		fmt.Println("  Operator key pair already exists:")
		fmt.Printf("  Public:  %s\n", pubPath)
		fmt.Printf("  Key ID:  %s\n", strings.TrimSpace(string(pub))[:24]+"...")
		fmt.Println()
		fmt.Println("  Use --force to regenerate (this changes your identity).")
		fmt.Println()
		return nil
	}

	if force {
		fmt.Println()
		fmt.Println("  Warning: Regenerating keys will change your operator identity.")
		fmt.Println("  You will need to re-register with the orchestrator.")
		fmt.Println()
	}

	// Prompt for passphrase (MUST be interactive, never from flags)
	fmt.Print("  Enter passphrase (min 12 characters): ")
	pass1, err := readPassphrase()
	if err != nil {
		return fmt.Errorf("reading passphrase: %w", err)
	}
	fmt.Println()

	if len(pass1) < 12 {
		return fmt.Errorf("passphrase must be at least 12 characters")
	}

	fmt.Print("  Confirm passphrase: ")
	pass2, err := readPassphrase()
	if err != nil {
		return fmt.Errorf("reading passphrase: %w", err)
	}
	fmt.Println()

	if pass1 != pass2 {
		return fmt.Errorf("passphrases do not match")
	}

	// Generate ML-DSA-65 key pair (FIPS 204)
	pub, priv, err := mldsa65.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generating ML-DSA-65 key pair: %w", err)
	}

	// Create keys directory with secure permissions
	if err := os.MkdirAll(keysDir, 0700); err != nil {
		return fmt.Errorf("creating keys directory: %w", err)
	}

	// Serialize keys
	privBytes, err := priv.MarshalBinary()
	if err != nil {
		return fmt.Errorf("marshaling private key: %w", err)
	}
	pubBytes, err := pub.MarshalBinary()
	if err != nil {
		return fmt.Errorf("marshaling public key: %w", err)
	}

	// Encrypt private key with Argon2id KDF + AES-256-GCM
	encryptedKey, err := encryptKey(privBytes, pass1)
	if err != nil {
		return fmt.Errorf("encrypting private key: %w", err)
	}

	// Write encrypted private key (0600)
	if err := os.WriteFile(privPath, encryptedKey, 0600); err != nil {
		return fmt.Errorf("writing private key: %w", err)
	}

	// Write public key as base64url (no padding) per spec
	pubB64 := base64.RawURLEncoding.EncodeToString(pubBytes)
	if err := os.WriteFile(pubPath, []byte(pubB64), 0644); err != nil {
		return fmt.Errorf("writing public key: %w", err)
	}

	// Write name file
	namePath := filepath.Join(keysDir, "operator.name")
	os.WriteFile(namePath, []byte(name), 0644)

	// Key ID is first 16 chars of base64url public key
	keyID := pubB64[:16]

	fmt.Println()
	fmt.Println("  Generated operator key pair (ML-DSA-65 / FIPS 204):")
	fmt.Printf("  Private: %s (encrypted, Argon2id + AES-256-GCM)\n", privPath)
	fmt.Printf("  Public:  %s\n", pubPath)
	fmt.Printf("  Key ID:  %s...\n", keyID)
	fmt.Printf("  Name:    %s\n", name)
	fmt.Println()
	fmt.Println("  Keep your private key safe. It is your identity.")
	fmt.Println("  The passphrase is NOT stored — you must remember it.")
	fmt.Println()

	return nil
}

func handleRegister(args []string) error {
	orchURL := resolveOrchURL(args)
	if orchURL == "" {
		return fmt.Errorf("orchestrator URL required.\n  Usage: weblisk operator register --orch http://localhost:9800")
	}

	role := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--role" && i+1 < len(args):
			i++
			role = args[i]
		case strings.HasPrefix(args[i], "--role="):
			role = strings.SplitN(args[i], "=", 2)[1]
		}
	}

	privKey, err := loadPrivateKey()
	if err != nil {
		return fmt.Errorf("no operator keys found.\n  Run 'weblisk operator init' first.\n  %w", err)
	}

	name := loadOperatorName()

	pubKey := privKey.Public().(*mldsa65.PublicKey)
	pubBytes, _ := pubKey.MarshalBinary()
	pubB64 := base64.RawURLEncoding.EncodeToString(pubBytes)

	// Build registration payload
	payload := map[string]string{
		"name":       name,
		"public_key": pubB64,
	}
	if role != "" {
		payload["role"] = role
	}
	payloadBytes, _ := json.Marshal(payload)

	// Sign the payload with ML-DSA-65
	sig, err := privKey.Sign(rand.Reader, payloadBytes, nil)
	if err != nil {
		return fmt.Errorf("signing registration: %w", err)
	}
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	fmt.Println()
	fmt.Printf("  Registering operator '%s' with %s...\n", name, orchURL)

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("POST", orchURL+"/v1/admin/operators/register", strings.NewReader(string(payloadBytes)))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", sigB64)
	req.Header.Set("X-Algorithm", "ml-dsa-65")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w\n  Is the orchestrator running at %s?", err, orchURL)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return fmt.Errorf("registration failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	// Parse response for token
	var result struct {
		Token   string `json:"token"`
		Role    string `json:"role"`
		Status  string `json:"status"`
		Expires string `json:"expires"`
	}
	_ = json.Unmarshal(body, &result)

	// architecture/admin, "The registration response": a token comes back only
	// when registration also APPROVED the operator — the first-operator
	// bootstrap. A client MUST NOT infer a token from a successful
	// registration, which is how "[ok] Registered" came to be printed beside an
	// empty token file, after which every /v1/admin call answered 401.
	if result.Token != "" {
		saveToken(orchURL, name, result.Token, 0)
		fmt.Printf("  [ok] Registered as %s\n", result.Role)
		fmt.Printf("  Token stored: %s\n", tokenFilePath())
		fmt.Println()
		return nil
	}
	if result.Status == "pending" {
		fmt.Println("  [ok] Registered — pending approval by an existing admin.")
		fmt.Println("      No token is issued until approved.")
		fmt.Println()
		return nil
	}
	// Approved but no token in the response: ask the endpoint that issues them.
	tok, exp, terr := RequestToken(orchURL, name)
	if terr != nil {
		fmt.Println("  [ok] Registered, and no token was issued.")
		fmt.Printf("      %v\n", terr)
		fmt.Println("      Run 'weblisk operator token --refresh' once approved.")
		fmt.Println()
		return nil
	}
	saveToken(orchURL, name, tok, exp)
	fmt.Println("  [ok] Registered.")
	fmt.Printf("  Token stored: %s\n", tokenFilePath())
	fmt.Println()
	return nil
}

func handleToken(args []string) error {
	refresh := false
	for _, a := range args {
		if a == "--refresh" {
			refresh = true
		}
	}

	tokenPath := tokenFilePath()
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return fmt.Errorf("no token found.\n  Run 'weblisk operator register --orch <url>' first.")
	}

	var tokenInfo map[string]string
	if err := json.Unmarshal(data, &tokenInfo); err != nil {
		return fmt.Errorf("invalid token file")
	}

	if refresh {
		orchURL := tokenInfo["orch"]
		if orchURL == "" {
			return fmt.Errorf("no orchestrator URL in token file")
		}

		privKey, err := loadPrivateKey()
		if err != nil {
			return fmt.Errorf("cannot refresh: %w", err)
		}

		client := &http.Client{Timeout: 10 * time.Second}
		req, err := http.NewRequest("POST", orchURL+"/v1/admin/operators/refresh", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tokenInfo["token"])

		pubKey := privKey.Public().(*mldsa65.PublicKey)
		pubBytes, _ := pubKey.MarshalBinary()
		req.Header.Set("X-Public-Key", base64.RawURLEncoding.EncodeToString(pubBytes))

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("connection failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return fmt.Errorf("refresh failed (HTTP %d). Run: weblisk operator register", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		var result struct {
			Token   string `json:"token"`
			Expires string `json:"expires"`
		}
		if json.Unmarshal(body, &result) == nil && result.Token != "" {
			tokenInfo["token"] = result.Token
			tokenInfo["expires"] = result.Expires
			newData, _ := json.Marshal(tokenInfo)
			os.WriteFile(tokenPath, newData, 0600)
			fmt.Printf("  [ok] Token refreshed. Expires: %s\n", result.Expires)
		}
		return nil
	}

	// Display token info
	fmt.Println()
	fmt.Printf("  Operator: %s\n", loadOperatorName())
	fmt.Printf("  Role:     %s\n", tokenInfo["role"])
	fmt.Printf("  Expires:  %s\n", tokenInfo["expires"])
	fmt.Printf("  Orch:     %s\n", tokenInfo["orch"])
	fmt.Println()

	return nil
}

func handleRotate(args []string) error {
	keysDir := keysDirectory()
	privPath := filepath.Join(keysDir, "operator.key")
	pubPath := filepath.Join(keysDir, "operator.pub")

	// Must have existing key
	if _, err := os.Stat(privPath); err != nil {
		return fmt.Errorf("no existing key to rotate. Run: weblisk operator init")
	}

	// Decrypt existing key with current passphrase
	fmt.Print("  Enter current passphrase: ")
	oldPass, err := readPassphrase()
	if err != nil {
		return fmt.Errorf("reading passphrase: %w", err)
	}
	fmt.Println()

	data, err := os.ReadFile(privPath)
	if err != nil {
		return fmt.Errorf("reading private key: %w", err)
	}

	oldPrivBytes, err := decryptKey(data, oldPass)
	if err != nil {
		return fmt.Errorf("decrypting key (wrong passphrase?): %w", err)
	}

	var oldKey mldsa65.PrivateKey
	if err := oldKey.UnmarshalBinary(oldPrivBytes); err != nil {
		return fmt.Errorf("parsing existing key: %w", err)
	}

	fmt.Println("  Generating new ML-DSA-65 key pair...")

	// Prompt for new passphrase
	fmt.Print("  Enter new passphrase (min 12 characters): ")
	pass1, err := readPassphrase()
	if err != nil {
		return fmt.Errorf("reading passphrase: %w", err)
	}
	fmt.Println()

	if len(pass1) < 12 {
		return fmt.Errorf("passphrase must be at least 12 characters")
	}

	fmt.Print("  Confirm new passphrase: ")
	pass2, err := readPassphrase()
	if err != nil {
		return fmt.Errorf("reading passphrase: %w", err)
	}
	fmt.Println()

	if pass1 != pass2 {
		return fmt.Errorf("passphrases do not match")
	}

	// Generate new key pair
	newPub, newPriv, err := mldsa65.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generating new key pair: %w", err)
	}

	newPubBytes, _ := newPub.MarshalBinary()
	newPrivBytes, _ := newPriv.MarshalBinary()
	newPubB64 := base64.RawURLEncoding.EncodeToString(newPubBytes)

	// Register new public key with orchestrator (dual-signed for proof of continuity)
	orchURL := resolveOrchURL(args)
	if orchURL != "" {
		fmt.Println("  Registering new public key with orchestrator (dual-signed)...")

		oldPubKey := oldKey.Public().(*mldsa65.PublicKey)
		oldPubBytes, _ := oldPubKey.MarshalBinary()
		oldPubB64 := base64.RawURLEncoding.EncodeToString(oldPubBytes)

		payload, _ := json.Marshal(map[string]string{
			"new_public_key": newPubB64,
			"old_public_key": oldPubB64,
		})

		// Sign with old key (current_signature)
		currentSig, err := oldKey.Sign(rand.Reader, payload, nil)
		if err != nil {
			return fmt.Errorf("signing with old key: %w", err)
		}

		// Sign with new key (new_signature)
		newSig, err := newPriv.Sign(rand.Reader, payload, nil)
		if err != nil {
			return fmt.Errorf("signing with new key: %w", err)
		}

		rotatePayload, _ := json.Marshal(map[string]string{
			"new_public_key":    newPubB64,
			"old_public_key":    oldPubB64,
			"current_signature": base64.RawURLEncoding.EncodeToString(currentSig),
			"new_signature":     base64.RawURLEncoding.EncodeToString(newSig),
		})

		client := &http.Client{Timeout: 10 * time.Second}
		req, _ := http.NewRequest("POST", orchURL+"/v1/admin/operators/rotate", strings.NewReader(string(rotatePayload)))
		req.Header.Set("Content-Type", "application/json")

		token, _, _ := LoadToken()
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("connection failed: %w", err)
		}
		resp.Body.Close()

		if resp.StatusCode != 200 && resp.StatusCode != 201 {
			return fmt.Errorf("orchestrator rejected key rotation (HTTP %d)", resp.StatusCode)
		}
	}

	// Archive old key
	revokedPath := privPath + ".revoked"
	os.Rename(privPath, revokedPath)

	// Encrypt and write new private key
	encryptedKey, err := encryptKey(newPrivBytes, pass1)
	if err != nil {
		return fmt.Errorf("encrypting new key: %w", err)
	}
	if err := os.WriteFile(privPath, encryptedKey, 0600); err != nil {
		return fmt.Errorf("writing new private key: %w", err)
	}

	// Write new public key
	if err := os.WriteFile(pubPath, []byte(newPubB64), 0644); err != nil {
		return fmt.Errorf("writing new public key: %w", err)
	}

	fmt.Println()
	fmt.Printf("  Key rotated successfully (ML-DSA-65).\n")
	fmt.Printf("  New Key ID: %s...\n", newPubB64[:16])
	fmt.Printf("  Old key archived: %s\n", revokedPath)
	fmt.Println()

	return nil
}

// ── Helpers ──────────────────────────────────────────────────

func keysDirectory() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".weblisk", "keys")
}

func tokenFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".weblisk", "token")
}

// cachedKey holds the decrypted operator key for the life of the process.
//
// protocol/identity rule 4 says the PASSPHRASE is never stored and exists only
// in memory during the decrypt operation. It does not say the decrypted key must
// be thrown away and the human asked again: decrypting per call made a command
// that needs two key operations prompt twice, and with input piped — Studio's
// password field, a container, CI — the second read found stdin exhausted and
// reported "wrong passphrase" for a correct one.
var (
	cachedKey  *mldsa65.PrivateKey
	cachedKeyE error
	cachedOnce sync.Once
)

func loadPrivateKey() (*mldsa65.PrivateKey, error) {
	cachedOnce.Do(func() { cachedKey, cachedKeyE = decryptPrivateKey() })
	return cachedKey, cachedKeyE
}

func decryptPrivateKey() (*mldsa65.PrivateKey, error) {
	privPath := filepath.Join(keysDirectory(), "operator.key")
	data, err := os.ReadFile(privPath)
	if err != nil {
		return nil, fmt.Errorf("reading private key: %w", err)
	}

	// Key must be in weblisk-key-v1 format
	fmt.Print("  Passphrase: ")
	pass, err := readPassphrase()
	if err != nil {
		return nil, fmt.Errorf("reading passphrase: %w", err)
	}
	fmt.Println()

	privBytes, err := decryptKey(data, pass)
	if err != nil {
		return nil, fmt.Errorf("decrypting key (wrong passphrase?): %w", err)
	}

	var key mldsa65.PrivateKey
	if err := key.UnmarshalBinary(privBytes); err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}

	return &key, nil
}

func loadOperatorName() string {
	namePath := filepath.Join(keysDirectory(), "operator.name")
	data, err := os.ReadFile(namePath)
	if err != nil {
		return os.Getenv("USER")
	}
	return strings.TrimSpace(string(data))
}

// LoadToken reads the stored operator token and orchestrator URL.
func LoadToken() (token, orchURL string, err error) {
	data, readErr := os.ReadFile(tokenFilePath())
	if readErr != nil {
		return "", "", fmt.Errorf("no token. Run: weblisk operator register --orch <url>")
	}
	var info map[string]string
	if json.Unmarshal(data, &info) != nil {
		return "", "", fmt.Errorf("invalid token file")
	}
	return info["token"], info["orch"], nil
}

// TokenExpiry returns the expiry time of the stored token, or zero if unknown.
func TokenExpiry() time.Time {
	data, err := os.ReadFile(tokenFilePath())
	if err != nil {
		return time.Time{}
	}
	var info map[string]string
	if json.Unmarshal(data, &info) != nil {
		return time.Time{}
	}
	if info["expires"] == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, info["expires"])
	if err != nil {
		return time.Time{}
	}
	return t
}

// RefreshToken attempts to refresh the operator token with the orchestrator.
// RefreshToken obtains a fresh operator token from the orchestrator.
//
// architecture/admin, "Obtaining a token": the operator's private key IS the
// credential. Sign {name, timestamp}, POST it, receive a token. The same call
// issues and refreshes.
//
// # What this replaces
//
// The previous implementation posted to /v1/admin/operators/refresh — an
// endpoint no blueprint declares, so no orchestrator serves it. It sent the
// EXPIRED token as a bearer credential, which fails precisely when a refresh is
// needed. It sent the public key in an X-Public-Key header, which the
// orchestrator must not trust: admin.md requires it to look the operator up in
// its OWN records, because accepting a key from the request makes any caller
// able to present any identity. And having loaded the private key, it signed
// nothing at all.
func RefreshToken() (string, error) {
	data, err := os.ReadFile(tokenFilePath())
	if err != nil {
		return "", fmt.Errorf("no token file — run 'weblisk operator register --orch <url>' first")
	}
	var info map[string]string
	if json.Unmarshal(data, &info) != nil {
		return "", fmt.Errorf("invalid token file")
	}
	orchURL := info["orch"]
	if orchURL == "" {
		return "", fmt.Errorf("no orchestrator URL in token file")
	}
	name := info["name"]
	if name == "" {
		name = loadOperatorName()
	}
	token, expires, err := RequestToken(orchURL, name)
	if err != nil {
		return "", err
	}
	saveToken(orchURL, name, token, expires)
	return token, nil
}

// RequestToken signs the challenge architecture/admin specifies and exchanges
// it for a token. Exported so a caller that holds no token file — a first
// connection from a console — can obtain one.
func RequestToken(orchURL, name string) (token string, expiresAt int64, err error) {
	privKey, err := loadPrivateKey()
	if err != nil {
		return "", 0, err
	}
	// canonicalize({name, timestamp}): a Go map marshals with sorted keys, which
	// is the ordering RFC 8785 requires.
	challenge, err := json.Marshal(map[string]any{
		"name":      name,
		"timestamp": time.Now().Unix(),
	})
	if err != nil {
		return "", 0, err
	}
	var payload map[string]any
	if err := json.Unmarshal(challenge, &payload); err != nil {
		return "", 0, err
	}
	var sig [mldsa65.SignatureSize]byte
	if err := mldsa65.SignTo(privKey, challenge, nil, false, sig[:]); err != nil {
		return "", 0, fmt.Errorf("signing the challenge: %w", err)
	}
	payload["signature"] = base64.RawURLEncoding.EncodeToString(sig[:])

	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, err
	}
	req, err := http.NewRequest("POST", strings.TrimRight(orchURL, "/")+"/v1/admin/operators/token",
		strings.NewReader(string(body)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return "", 0, fmt.Errorf("token request refused (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Token == "" {
		return "", 0, fmt.Errorf("orchestrator returned no token")
	}
	return out.Token, out.ExpiresAt, nil
}

// saveToken persists a token beside the operator identity, 0600.
//
// The token is stored; the PASSPHRASE never is — protocol/identity rule 4.
func saveToken(orchURL, name, token string, expiresAt int64) {
	b, err := json.Marshal(map[string]string{
		"token":      token,
		"orch":       orchURL,
		"name":       name,
		"expires_at": fmt.Sprintf("%d", expiresAt),
	})
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(tokenFilePath()), 0o700)
	_ = os.WriteFile(tokenFilePath(), b, 0o600)
}

func resolveOrchURL(args []string) string {
	// Check command-line args
	for i := 0; i < len(args); i++ {
		if args[i] == "--orch" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(args[i], "--orch=") {
			return strings.SplitN(args[i], "=", 2)[1]
		}
	}
	// Check env
	if url := os.Getenv("WL_ORCH"); url != "" {
		return url
	}
	// Check project config (YAML format — parse orchestrator.url or simple orch key)
	if data, err := os.ReadFile(".weblisk/config.yaml"); err == nil {
		if url := extractYAMLValue(string(data), "orchestrator_url"); url != "" {
			return url
		}
	}
	// Fallback: check legacy JSON config
	if data, err := os.ReadFile(".weblisk/config.json"); err == nil {
		var cfg struct {
			OrchestratorURL string `json:"orchestrator_url"`
		}
		if json.Unmarshal(data, &cfg) == nil && cfg.OrchestratorURL != "" {
			return cfg.OrchestratorURL
		}
	}
	// Check user config
	home, _ := os.UserHomeDir()
	if data, err := os.ReadFile(filepath.Join(home, ".weblisk", "config.yaml")); err == nil {
		if url := extractYAMLValue(string(data), "orchestrator_url"); url != "" {
			return url
		}
	}
	if data, err := os.ReadFile(filepath.Join(home, ".weblisk", "config.json")); err == nil {
		var cfg struct {
			OrchestratorURL string `json:"orchestrator_url"`
		}
		if json.Unmarshal(data, &cfg) == nil && cfg.OrchestratorURL != "" {
			return cfg.OrchestratorURL
		}
	}
	return ""
}

// extractYAMLValue does minimal YAML parsing for a top-level key: value.
// No external dependency required.
func extractYAMLValue(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+":") {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, key+":"))
			// Strip quotes
			if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
				val = val[1 : len(val)-1]
			}
			return val
		}
	}
	return ""
}

func PrintHelp() {
	fmt.Print(`
  Operator Commands:
    weblisk operator init           Generate an ML-DSA-65 operator key pair
      --name <name>                 Operator name (default: system username)
      --force                       Regenerate keys (changes identity)
    weblisk operator register       Register with an orchestrator
      --orch <url>                  Orchestrator URL (required)
      --role <role>                 Request a specific role
    weblisk operator token          Inspect or refresh operator token
      --refresh                     Force token refresh
    weblisk operator rotate         Rotate ML-DSA-65 key pair

  Key format: weblisk-key-v1 (ML-DSA-65, Argon2id KDF, AES-256-GCM)
  Encoding:   base64url (RFC 4648 Section 5, no padding)

`)
}

// ── Key Encryption (weblisk-key-v1 format) ───────────────────
//
// Format: JSON structure with fields:
//   header: "weblisk-key-v1"
//   algorithm: "ml-dsa-65"
//   kdf: "argon2id"
//   kdf_params: {salt, time, memory, parallelism}
//   ciphertext: base64url-encoded encrypted private key

// Argon2id parameters per RFC 9106 recommendations
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
	argonKeyLen  = 32 // AES-256
)

type keyFile struct {
	Header     string    `json:"header"`
	Algorithm  string    `json:"algorithm"`
	KDF        string    `json:"kdf"`
	KDFParams  kdfParams `json:"kdf_params"`
	Nonce      string    `json:"nonce"`
	Ciphertext string    `json:"ciphertext"`
}

type kdfParams struct {
	Salt        string `json:"salt"`
	Time        uint32 `json:"time"`
	Memory      uint32 `json:"memory"`
	Parallelism uint8  `json:"parallelism"`
}

// encryptKey encrypts a private key with Argon2id + AES-256-GCM.
func encryptKey(privBytes []byte, passphrase string) ([]byte, error) {
	// Generate random salt (32 bytes)
	salt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}

	// Derive encryption key using Argon2id
	key := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	// AES-256-GCM encrypt
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	ciphertext := gcm.Seal(nil, nonce, privBytes, nil)

	// Encode to weblisk-key-v1 JSON format
	kf := keyFile{
		Header:    "weblisk-key-v1",
		Algorithm: "ml-dsa-65",
		KDF:       "argon2id",
		KDFParams: kdfParams{
			Salt:        base64.RawURLEncoding.EncodeToString(salt),
			Time:        argonTime,
			Memory:      argonMemory,
			Parallelism: argonThreads,
		},
		Nonce:      base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	}

	return json.MarshalIndent(kf, "", "  ")
}

// decryptKey decrypts a weblisk-key-v1 formatted key file using the passphrase.
func decryptKey(data []byte, passphrase string) ([]byte, error) {
	var kf keyFile
	if err := json.Unmarshal(data, &kf); err != nil {
		return nil, fmt.Errorf("invalid key format: %w", err)
	}

	if kf.Header != "weblisk-key-v1" {
		return nil, fmt.Errorf("unsupported key format: %s", kf.Header)
	}
	if kf.Algorithm != "ml-dsa-65" {
		return nil, fmt.Errorf("unsupported algorithm: %s", kf.Algorithm)
	}
	if kf.KDF != "argon2id" {
		return nil, fmt.Errorf("unsupported KDF: %s", kf.KDF)
	}

	salt, err := base64.RawURLEncoding.DecodeString(kf.KDFParams.Salt)
	if err != nil {
		return nil, fmt.Errorf("invalid salt encoding")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(kf.Nonce)
	if err != nil {
		return nil, fmt.Errorf("invalid nonce encoding")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(kf.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("invalid ciphertext encoding")
	}

	// Derive key from passphrase using stored Argon2id parameters
	key := argon2.IDKey([]byte(passphrase), salt, kf.KDFParams.Time, kf.KDFParams.Memory, kf.KDFParams.Parallelism, argonKeyLen)

	// Decrypt
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed (wrong passphrase?)")
	}

	return plaintext, nil
}

// stdinReader is shared across passphrase reads.
//
// A new bufio.Reader per call reads ahead and discards what it buffered when it
// goes out of scope. On a terminal that is invisible — a human types one line,
// waits, types the next. Piped, the first read swallows BOTH lines and the
// second gets nothing, so `operator init` reported "passphrases do not match"
// for two identical lines and headless key generation could never work.
//
// protocol/identity requires this to work without a terminal: "A runtime with a
// terminal will prompt; a runtime without one — a Worker, a container, a
// scheduled task — cannot, and a specification that says prompt has excluded
// it." Studio's password field is such a channel.
var (
	stdinOnce   sync.Once
	stdinShared *bufio.Reader
)

func sharedStdin() *bufio.Reader {
	stdinOnce.Do(func() { stdinShared = bufio.NewReader(os.Stdin) })
	return stdinShared
}

// readPassphrase reads one line from stdin without echoing it.
//
// Echo suppression is best-effort: `stty -echo` fails harmlessly when stdin is
// not a terminal, which is the case this function must also serve. The
// passphrase is never echoed, never logged and never written to disk, and it is
// never accepted as a command-line argument — argv is readable by any process
// on the machine.
func readPassphrase() (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		stty := exec.Command("stty", "-echo")
		stty.Stdin = os.Stdin
		_ = stty.Run()
		defer func() {
			restore := exec.Command("stty", "echo")
			restore.Stdin = os.Stdin
			_ = restore.Run()
		}()
	}

	line, err := sharedStdin().ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
