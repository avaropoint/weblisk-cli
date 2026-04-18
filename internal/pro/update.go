package pro

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/avaropoint/weblisk-cli/internal/config"
)

const frameworkCDNBase = "https://cdn.weblisk.dev/"
const frameworkManifestURL = "https://cdn.weblisk.dev/manifest.json"

// frameworkManifest is the response from the CDN manifest endpoint.
type frameworkManifest struct {
	Files []string `json:"files"`
}

// fetchFrameworkFiles retrieves the list of framework files from the CDN manifest.
// Falls back to a minimal set if the manifest is unavailable.
func fetchFrameworkFiles() []string {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(frameworkManifestURL)
	if err != nil {
		return []string{"weblisk.js"}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return []string{"weblisk.js"}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return []string{"weblisk.js"}
	}

	var manifest frameworkManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return []string{"weblisk.js"}
	}

	if len(manifest.Files) == 0 {
		return []string{"weblisk.js"}
	}
	return manifest.Files
}

// FrameworkFiles returns the current list of framework files.
// This fetches from the CDN manifest to avoid hardcoding.
func FrameworkFiles() []string {
	return fetchFrameworkFiles()
}

// Update re-downloads framework modules from CDN.
// If a pro license is configured, also refreshes pro modules.
func Update(root, version string) error {
	fmt.Println()
	fmt.Println("  Weblisk Update")
	fmt.Println()

	libDir := filepath.Join(root, "lib", "weblisk")
	if _, err := os.Stat(libDir); os.IsNotExist(err) {
		return fmt.Errorf("no lib/weblisk/ found -- are you in a --local project?")
	}

	fmt.Println("  Downloading latest framework modules...")
	fmt.Println()

	files := fetchFrameworkFiles()
	client := &http.Client{Timeout: 15 * time.Second}
	updated := 0
	failed := 0

	for _, file := range files {
		dest := filepath.Join(libDir, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "  [error] %s -- %v\n", file, err)
			failed++
			continue
		}

		if err := downloadFile(client, frameworkCDNBase+file, dest); err != nil {
			if strings.HasSuffix(file, ".js") {
				fmt.Fprintf(os.Stderr, "  [error] %s -- %v\n", file, err)
				failed++
			}
			continue
		}
		fmt.Printf("  [ok] %s\n", file)
		updated++
	}

	cfg := config.Resolve()
	if cfg.License != "" {
		fmt.Println()
		fmt.Println("  Updating pro modules...")
		fmt.Println()
		proDir := filepath.Join(libDir, "pro")

		proModules, err := validateKey(cfg.License, version)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [warn] could not validate license: %v\n", err)
		} else if err := os.MkdirAll(proDir, 0755); err == nil {
			for _, mod := range proModules {
				if err := DownloadModule(cfg.License, mod, proDir, version); err != nil {
					fmt.Fprintf(os.Stderr, "  [error] pro/%s -- %v\n", mod, err)
					failed++
					continue
				}
				fmt.Printf("  [ok] pro/%s\n", mod)
				updated++

				dts := strings.TrimSuffix(mod, ".js") + ".d.ts"
				if err := DownloadModule(cfg.License, dts, proDir, version); err == nil {
					fmt.Printf("  [ok] pro/%s\n", dts)
					updated++
				}
			}
		}
	}

	fmt.Println()
	if failed > 0 {
		fmt.Printf("  Updated %d files (%d failed).\n\n", updated, failed)
	} else {
		fmt.Printf("  Updated %d files.\n\n", updated)
	}

	return nil
}

func downloadFile(client *http.Client, url, dest string) error {
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("network error: %w", err)
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

	return os.WriteFile(dest, data, 0644)
}
