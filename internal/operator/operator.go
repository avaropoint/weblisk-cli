package operator

// Operator identity management — Ed25519 key generation, orchestrator
// registration, and token lifecycle.

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
		fmt.Printf("  Key ID:  %s\n", strings.TrimSpace(string(pub)))
		fmt.Println()
		fmt.Println("  Use --force to regenerate (this changes your identity).")
		fmt.Println()
		return nil
	}

	if force {
		fmt.Println()
		fmt.Println("  ⚠  Regenerating keys will change your operator identity.")
		fmt.Println("     You will need to re-register with the orchestrator.")
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

	// Generate Ed25519 key pair
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generating key pair: %w", err)
	}

	// Create keys directory with secure permissions
	if err := os.MkdirAll(keysDir, 0700); err != nil {
		return fmt.Errorf("creating keys directory: %w", err)
	}

	// Encrypt private key with passphrase (iterated SHA-512 KDF + AES-256-GCM)
	encryptedKey, err := encryptKey(priv, pass1)
	if err != nil {
		return fmt.Errorf("encrypting private key: %w", err)
	}

	// Write encrypted private key (0600)
	if err := os.WriteFile(privPath, encryptedKey, 0600); err != nil {
		return fmt.Errorf("writing private key: %w", err)
	}

	// Write public key (0644)
	pubHex := hex.EncodeToString(pub)
	if err := os.WriteFile(pubPath, []byte(pubHex), 0644); err != nil {
		return fmt.Errorf("writing public key: %w", err)
	}

	// Write name file
	namePath := filepath.Join(keysDir, "operator.name")
	os.WriteFile(namePath, []byte(name), 0644)

	fmt.Println()
	fmt.Println("  Generated operator key pair:")
	fmt.Printf("  Private: %s (encrypted, AES-256-GCM)\n", privPath)
	fmt.Printf("  Public:  %s\n", pubPath)
	fmt.Printf("  Key ID:  %s\n", pubHex[:16]+"...")
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
	pubHex := hex.EncodeToString(privKey.Public().(ed25519.PublicKey))

	// Build registration payload
	payload := map[string]string{
		"name":       name,
		"public_key": pubHex,
	}
	if role != "" {
		payload["role"] = role
	}
	payloadBytes, _ := json.Marshal(payload)

	// Sign the payload
	sig := ed25519.Sign(privKey, payloadBytes)
	sigHex := hex.EncodeToString(sig)

	fmt.Println()
	fmt.Printf("  Registering operator '%s' with %s...\n", name, orchURL)

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("POST", orchURL+"/v1/admin/operators/register", strings.NewReader(string(payloadBytes)))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", sigHex)

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
	if err := json.Unmarshal(body, &result); err == nil && result.Token != "" {
		// Store token
		tokenPath := tokenFilePath()
		tokenData, _ := json.Marshal(map[string]string{
			"token":   result.Token,
			"orch":    orchURL,
			"role":    result.Role,
			"expires": result.Expires,
		})
		os.MkdirAll(filepath.Dir(tokenPath), 0700)
		os.WriteFile(tokenPath, tokenData, 0600)

		fmt.Printf("  [ok] Registered as %s\n", result.Role)
		fmt.Printf("  Token stored: %s\n", tokenPath)
		if result.Expires != "" {
			fmt.Printf("  Expires: %s\n", result.Expires)
		}
	} else if result.Status == "pending" {
		fmt.Println("  Registration pending admin approval.")
	} else {
		fmt.Println("  [ok] Registered.")
	}
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

		pubHex := hex.EncodeToString(privKey.Public().(ed25519.PublicKey))
		req.Header.Set("X-Public-Key", pubHex)

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

	content := strings.TrimSpace(string(data))
	var oldKey ed25519.PrivateKey
	if strings.HasPrefix(content, "weblisk-key-v1:") {
		privBytes, err := decryptKey(data, oldPass)
		if err != nil {
			return fmt.Errorf("decrypting key (wrong passphrase?): %w", err)
		}
		oldKey = ed25519.PrivateKey(privBytes)
	} else {
		// Legacy unencrypted key
		privBytes, err := hex.DecodeString(content)
		if err != nil {
			return fmt.Errorf("decoding private key: %w", err)
		}
		oldKey = ed25519.PrivateKey(privBytes)
	}

	fmt.Println("  Generating new key pair...")

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
	newPub, newPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generating new key pair: %w", err)
	}

	// Register new public key with orchestrator (signed by old key for proof of continuity)
	orchURL := resolveOrchURL(args)
	if orchURL != "" {
		fmt.Println("  Registering new public key with orchestrator (signed by old key)...")

		newPubHex := hex.EncodeToString(newPub)
		oldPubHex := hex.EncodeToString(oldKey.Public().(ed25519.PublicKey))

		// Sign the new public key with the old key
		signature := ed25519.Sign(oldKey, newPub)
		sigHex := hex.EncodeToString(signature)

		payload, _ := json.Marshal(map[string]string{
			"new_public_key": newPubHex,
			"old_public_key": oldPubHex,
			"signature":      sigHex,
		})

		client := &http.Client{Timeout: 10 * time.Second}
		req, _ := http.NewRequest("POST", orchURL+"/v1/admin/operators/rotate", strings.NewReader(string(payload)))
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
	encryptedKey, err := encryptKey(newPriv, pass1)
	if err != nil {
		return fmt.Errorf("encrypting new key: %w", err)
	}
	if err := os.WriteFile(privPath, encryptedKey, 0600); err != nil {
		return fmt.Errorf("writing new private key: %w", err)
	}

	// Write new public key
	newPubHex := hex.EncodeToString(newPub)
	if err := os.WriteFile(pubPath, []byte(newPubHex), 0644); err != nil {
		return fmt.Errorf("writing new public key: %w", err)
	}

	fmt.Println()
	fmt.Println("✓ Key rotated successfully.")
	fmt.Printf("  New Key ID: %s\n", newPubHex[:16]+"...")
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

func loadPrivateKey() (ed25519.PrivateKey, error) {
	privPath := filepath.Join(keysDirectory(), "operator.key")
	data, err := os.ReadFile(privPath)
	if err != nil {
		return nil, fmt.Errorf("reading private key: %w", err)
	}

	content := strings.TrimSpace(string(data))

	// Check if key is encrypted (weblisk-key-v1 format)
	if strings.HasPrefix(content, "weblisk-key-v1:") {
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
		return ed25519.PrivateKey(privBytes), nil
	}

	// Legacy: unencrypted hex key (pre-passphrase)
	privBytes, err := hex.DecodeString(content)
	if err != nil {
		return nil, fmt.Errorf("decoding private key: %w", err)
	}
	return ed25519.PrivateKey(privBytes), nil
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
// Returns the new token on success.
func RefreshToken() (string, error) {
	data, err := os.ReadFile(tokenFilePath())
	if err != nil {
		return "", fmt.Errorf("no token file")
	}
	var info map[string]string
	if json.Unmarshal(data, &info) != nil {
		return "", fmt.Errorf("invalid token file")
	}

	orchURL := info["orch"]
	if orchURL == "" {
		return "", fmt.Errorf("no orchestrator URL in token file")
	}

	privKey, err := loadPrivateKey()
	if err != nil {
		return "", err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("POST", orchURL+"/v1/admin/operators/refresh", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+info["token"])
	pubHex := hex.EncodeToString(privKey.Public().(ed25519.PublicKey))
	req.Header.Set("X-Public-Key", pubHex)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("refresh failed (HTTP %d)", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		Token   string `json:"token"`
		Expires string `json:"expires"`
	}
	if json.Unmarshal(body, &result) != nil || result.Token == "" {
		return "", fmt.Errorf("invalid refresh response")
	}

	// Update stored token
	info["token"] = result.Token
	info["expires"] = result.Expires
	newData, _ := json.Marshal(info)
	os.WriteFile(tokenFilePath(), newData, 0600)

	return result.Token, nil
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
	// Check project config
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

func PrintHelp() {
	fmt.Print(`
  Operator Commands:
    weblisk operator init           Generate an Ed25519 operator key pair
      --name <name>                 Operator name (default: system username)
      --force                       Regenerate keys (changes identity)
    weblisk operator register       Register with an orchestrator
      --orch <url>                  Orchestrator URL (required)
      --role <role>                 Request a specific role
    weblisk operator token          Inspect or refresh operator token
      --refresh                     Force token refresh
    weblisk operator rotate         Rotate Ed25519 key pair

`)
}

// ── Key Encryption (weblisk-key-v1 format) ───────────────────

// deriveKey uses iterated SHA-512 to derive a 32-byte key from passphrase + salt.
// This is a zero-dependency KDF providing key stretching.
// Iterations: 100,000 rounds of SHA-512.
func deriveKey(passphrase string, salt []byte) []byte {
	iterations := 100000
	h := sha512.Sum512(append([]byte(passphrase), salt...))
	for i := 1; i < iterations; i++ {
		h = sha512.Sum512(h[:])
	}
	return h[:32] // AES-256 key
}

// encryptKey encrypts a private key with AES-256-GCM using a passphrase-derived key.
// Output format: "weblisk-key-v1:<salt-hex>:<nonce-hex>:<ciphertext-hex>\n"
func encryptKey(privKey ed25519.PrivateKey, passphrase string) ([]byte, error) {
	// Generate random salt (32 bytes)
	salt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}

	// Derive encryption key
	key := deriveKey(passphrase, salt)

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

	ciphertext := gcm.Seal(nil, nonce, []byte(privKey), nil)

	// Format: weblisk-key-v1:<salt>:<nonce>:<ciphertext>
	line := fmt.Sprintf("weblisk-key-v1:%s:%s:%s\n",
		hex.EncodeToString(salt),
		hex.EncodeToString(nonce),
		hex.EncodeToString(ciphertext))

	return []byte(line), nil
}

// decryptKey decrypts a weblisk-key-v1 formatted key file using the passphrase.
func decryptKey(data []byte, passphrase string) ([]byte, error) {
	content := strings.TrimSpace(string(data))
	parts := strings.SplitN(content, ":", 4)
	if len(parts) != 4 || parts[0] != "weblisk-key-v1" {
		return nil, fmt.Errorf("invalid key format")
	}

	salt, err := hex.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid salt")
	}
	nonce, err := hex.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("invalid nonce")
	}
	ciphertext, err := hex.DecodeString(parts[3])
	if err != nil {
		return nil, fmt.Errorf("invalid ciphertext")
	}

	// Derive key from passphrase
	key := deriveKey(passphrase, salt)

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
		return nil, fmt.Errorf("decryption failed")
	}

	return plaintext, nil
}

// readPassphrase reads a passphrase from stdin without echoing.
func readPassphrase() (string, error) {
	// Disable echo
	stty := exec.Command("stty", "-echo")
	stty.Stdin = os.Stdin
	stty.Run()

	defer func() {
		restore := exec.Command("stty", "echo")
		restore.Stdin = os.Stdin
		restore.Run()
	}()

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
