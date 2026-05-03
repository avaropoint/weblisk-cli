package project

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

const vendorCDNBase = "https://cdn.weblisk.dev/"
const vendorManifestURL = "https://cdn.weblisk.dev/manifest.json"

// frameworkManifest is the response from the CDN manifest endpoint.
type frameworkManifest struct {
	Files []string `json:"files"`
}

// frameworkFiles retrieves the list of framework files from the CDN manifest.
func frameworkFiles() []string {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(vendorManifestURL)
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

// Vendor downloads framework files from CDN into a local directory.
// This enables adding Weblisk to any existing project without scaffolding.
func Vendor(root, dest string) error {
	if dest == "" {
		cfg := config.Resolve()
		dest = cfg.Lib
	}

	destDir := dest
	if !filepath.IsAbs(destDir) {
		destDir = filepath.Join(root, dest)
	}

	fmt.Println()
	fmt.Println("  Weblisk Vendor")
	fmt.Printf("  Downloading framework to %s/\n\n", dest)

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dest, err)
	}

	files := frameworkFiles()
	client := &http.Client{Timeout: 15 * time.Second}
	downloaded := 0
	failed := 0

	for _, file := range files {
		fileDest := filepath.Join(destDir, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(fileDest), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "  [error] %s -- %v\n", file, err)
			failed++
			continue
		}

		if err := vendorDownload(client, vendorCDNBase+file, fileDest); err != nil {
			if strings.HasSuffix(file, ".js") {
				fmt.Fprintf(os.Stderr, "  [error] %s -- %v\n", file, err)
				failed++
			}
			continue
		}
		fmt.Printf("  [ok] %s\n", file)
		downloaded++
	}

	fmt.Println()
	if failed > 0 {
		fmt.Printf("  Downloaded %d files (%d failed).\n\n", downloaded, failed)
	} else {
		fmt.Printf("  Downloaded %d files to %s/\n\n", downloaded, dest)
	}

	return nil
}

// UpdateFramework re-downloads framework modules from CDN for --local projects.
func UpdateFramework(root, version string) error {
	cfg := config.Resolve()
	libDir := filepath.Join(root, cfg.Lib)
	if _, err := os.Stat(libDir); os.IsNotExist(err) {
		return fmt.Errorf("no %s/ found — are you in a --local project?", cfg.Lib)
	}

	fmt.Println()
	fmt.Println("  Weblisk Update")
	fmt.Println()
	fmt.Println("  Downloading latest framework modules...")
	fmt.Println()

	files := frameworkFiles()
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

		if err := vendorDownload(client, vendorCDNBase+file, dest); err != nil {
			if strings.HasSuffix(file, ".js") {
				fmt.Fprintf(os.Stderr, "  [error] %s -- %v\n", file, err)
				failed++
			}
			continue
		}
		fmt.Printf("  [ok] %s\n", file)
		updated++
	}

	fmt.Println()
	if failed > 0 {
		fmt.Printf("  Updated %d files (%d failed).\n\n", updated, failed)
	} else {
		fmt.Printf("  Updated %d files.\n\n", updated)
	}

	return nil
}

func vendorDownload(client *http.Client, url, dest string) error {
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
