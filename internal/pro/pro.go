package pro

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const proValidateURL = "https://cdn.weblisk.dev/pro/validate"
const proRegisterURL = "https://cdn.weblisk.dev/pro/register"
const proCDNBase = "https://cdn.weblisk.dev/pro/"

type validateResponse struct {
	Valid   bool     `json:"valid"`
	Tier    string   `json:"tier"`
	Error   string   `json:"error,omitempty"`
	Modules []string `json:"modules,omitempty"`
	Domains []string `json:"domains,omitempty"`
}

// Activate validates a license key, downloads pro modules, and optionally
// registers a production domain for CDN-mode serving.
func Activate(root, key, domain, version string) error {
	if key == "" {
		return fmt.Errorf("license key required. Usage: weblisk license --key=WL-XXXX-XXXX-XXXX-XXXX [--domain=example.com]")
	}

	fmt.Println()
	fmt.Println("  Weblisk License")
	fmt.Println()
	fmt.Println("  Validating license key...")

	modules, err := validateKey(key, version)
	if err != nil {
		return err
	}

	fmt.Printf("  [ok] License valid -- %d pro modules available\n\n", len(modules))

	// Download pro modules
	proDir := filepath.Join(root, "lib", "weblisk", "pro")
	if err := os.MkdirAll(proDir, 0755); err != nil {
		return fmt.Errorf("create pro directory: %w", err)
	}

	downloaded := 0
	for _, mod := range modules {
		if err := DownloadModule(key, mod, proDir, version); err != nil {
			fmt.Fprintf(os.Stderr, "  [error] %s -- %v\n", mod, err)
			continue
		}
		fmt.Printf("  [ok] pro/%s\n", mod)
		downloaded++

		dts := strings.TrimSuffix(mod, ".js") + ".d.ts"
		if err := DownloadModule(key, dts, proDir, version); err == nil {
			fmt.Printf("  [ok] pro/%s\n", dts)
		}
	}

	// Save key to .env
	if err := saveEnvVar(root, "WL_LICENSE", key); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: could not save key to .env: %v\n", err)
	}

	// Register domain if provided — enables CDN-mode pro module serving.
	// When a domain is registered, the CDN worker allows browsers from
	// that domain (and its subdomains) to load pro modules directly.
	if domain != "" {
		if err := registerDomain(key, domain, version); err != nil {
			fmt.Fprintf(os.Stderr, "  [warn] domain registration failed: %v\n", err)
		} else {
			fmt.Printf("\n  [ok] Domain registered: %s (+ subdomains)\n", domain)
		}
	}

	fmt.Printf("\n  Done. %d pro modules installed to lib/weblisk/pro/\n", downloaded)
	fmt.Println()
	fmt.Println("  Pro modules are plain ES modules — import directly:")
	fmt.Println()
	fmt.Println("    import { track } from 'weblisk/pro/analytics.js';")
	fmt.Println("    import { toast } from 'weblisk/pro/toast.js';")
	fmt.Println()
	if domain != "" {
		fmt.Println("  CDN mode: pro modules will load from cdn.weblisk.dev for", domain)
		fmt.Println("  Set WL_CDN=https://cdn.weblisk.dev in .env to enable CDN builds.")
	} else {
		fmt.Println("  Local mode: pro modules ship in your build output as static files.")
		fmt.Println("  To enable CDN mode, re-run with --domain=yourdomain.com")
	}
	fmt.Println()
	fmt.Println("  To refresh modules later, run: weblisk update")
	fmt.Println()

	return nil
}

func validateKey(key, version string) ([]string, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	req, err := http.NewRequest("GET", proValidateURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", "weblisk-cli/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("network error: %w\n  Check your internet connection and try again.", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == 403 || resp.StatusCode == 401 {
		return nil, fmt.Errorf("invalid license key. Check your key at https://weblisk.dev/pro")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("server error (%d). Try again later.", resp.StatusCode)
	}

	var result validateResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unexpected response from server")
	}

	if !result.Valid {
		msg := "license key is not valid"
		if result.Error != "" {
			msg = result.Error
		}
		return nil, fmt.Errorf("%s", msg)
	}

	modules := result.Modules
	if len(modules) == 0 {
		return nil, fmt.Errorf("server returned no modules for this license")
	}

	return modules, nil
}

// registerDomain associates a domain with a license key for tracking.
// This is optional and non-blocking — failure doesn't affect activation.
func registerDomain(key, domain, version string) error {
	client := &http.Client{Timeout: 10 * time.Second}

	payload, _ := json.Marshal(map[string]string{
		"key":    key,
		"domain": domain,
	})

	req, err := http.NewRequest("POST", proRegisterURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "weblisk-cli/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("%s", errResp.Error)
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// DownloadModule fetches a single pro module from the CDN.
func DownloadModule(key, filename, destDir, version string) error {
	client := &http.Client{Timeout: 15 * time.Second}

	req, err := http.NewRequest("GET", proCDNBase+filename, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", "weblisk-cli/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return fmt.Errorf("not found")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	return os.WriteFile(filepath.Join(destDir, filename), data, 0644)
}

// ParseArgs extracts --key and --domain from pro command arguments.
func ParseArgs(args []string) (string, string) {
	var key, domain string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--key=") {
			key = strings.SplitN(a, "=", 2)[1]
		} else if a == "--key" && i+1 < len(args) {
			i++
			key = args[i]
		} else if strings.HasPrefix(a, "--domain=") {
			domain = strings.SplitN(a, "=", 2)[1]
		} else if a == "--domain" && i+1 < len(args) {
			i++
			domain = args[i]
		}
	}
	return key, domain
}

// saveEnvVar writes or updates a single variable in the project .env file.
func saveEnvVar(root, varName, value string) error {
	envPath := filepath.Join(root, ".env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		return os.WriteFile(envPath, []byte(varName+"="+value+"\n"), 0644)
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, varName+"=") || strings.HasPrefix(trimmed, "# "+varName+"=") {
			lines[i] = varName + "=" + value
			found = true
			break
		}
	}

	if !found {
		lines = append(lines, varName+"="+value)
	}

	return os.WriteFile(envPath, []byte(strings.Join(lines, "\n")), 0644)
}
