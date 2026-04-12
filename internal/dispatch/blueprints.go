package dispatch

// ── Blueprint Loader ────────────────────────────────────────
//
// Resolves blueprints from multiple sources with fallthrough:
//
//   1. Local project:  ./blueprints/ in the user's project
//   2. Custom sources: WL_BLUEPRINT_SOURCES (comma-separated Git URLs)
//   3. Core:           github.com/avaropoint/weblisk-blueprints (always)
//
// Each remote source is cloned into .weblisk/blueprints/<source-name>/
// and cached. When loading a blueprint, sources are checked in order;
// the first match wins. This lets customers override core blueprints
// and pull from private/partner repositories.

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/avaropoint/weblisk-cli/internal/config"
)

const coreRepo = "https://github.com/avaropoint/weblisk-blueprints.git"

// BlueprintSets defines which blueprints are loaded for each generation target.
var BlueprintSets = map[string][]string{
	"orchestrator": {"protocol/spec.md", "architecture/orchestrator.md", "protocol/identity.md"},
	"agent":        {"protocol/spec.md", "architecture/agent.md", "protocol/identity.md"},
}

// sourceDir returns a deterministic cache directory name for a repo URL.
func sourceDir(repoURL string) string {
	// Use the repo name if parseable, otherwise hash the URL.
	name := repoURL
	name = strings.TrimSuffix(name, ".git")
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	// Append a short hash to avoid collisions between repos with the same name.
	h := sha256.Sum256([]byte(repoURL))
	return fmt.Sprintf("%s-%x", name, h[:4])
}

// resolvedSources returns the ordered list of blueprint directories to search.
// Order: local project → custom sources → core.
func resolvedSources(root string) []string {
	var dirs []string

	// 1. Local project blueprints (highest priority).
	localDir := filepath.Join(root, "blueprints")
	if info, err := os.Stat(localDir); err == nil && info.IsDir() {
		dirs = append(dirs, localDir)
	}

	// 2. Custom sources from WL_BLUEPRINT_SOURCES.
	cfg := config.Resolve()
	for _, repo := range cfg.BlueprintSources {
		cacheDir := filepath.Join(root, ".weblisk", "blueprints", sourceDir(repo))
		if err := ensureCloned(repo, cacheDir); err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ Blueprint source %s: %v\n", repo, err)
			continue
		}
		dirs = append(dirs, cacheDir)
	}

	// 3. Core blueprints (always present as fallback).
	coreDir := filepath.Join(root, ".weblisk", "blueprints", sourceDir(coreRepo))
	if err := ensureCloned(coreRepo, coreDir); err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ Core blueprints: %v\n", err)
	} else {
		dirs = append(dirs, coreDir)
	}

	return dirs
}

// ensureCloned clones a repo if it hasn't been cached yet.
func ensureCloned(repoURL, cacheDir string) error {
	// If the cache exists and has content, skip.
	if entries, err := os.ReadDir(cacheDir); err == nil && len(entries) > 0 {
		return nil
	}

	fmt.Printf("  Fetching blueprints from %s...\n", repoURL)
	if err := os.MkdirAll(filepath.Dir(cacheDir), 0755); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}

	os.RemoveAll(cacheDir)
	cmd := exec.Command("git", "clone", "--depth=1", repoURL, cacheDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cloning %s: %w\n  If this is a private repo, ensure your Git credentials have access.", repoURL, err)
	}

	fmt.Printf("  ✓ Cached %s\n", filepath.Base(cacheDir))
	return nil
}

// EnsureBlueprints resolves all blueprint sources and returns the list
// of cache directories. For backward compatibility, it returns the core
// cache directory as the primary path.
func EnsureBlueprints(root string) (string, error) {
	dirs := resolvedSources(root)
	if len(dirs) == 0 {
		return "", fmt.Errorf("no blueprint sources available — check your internet connection")
	}
	// Return the last dir (core) for callers that expect a single path.
	return dirs[len(dirs)-1], nil
}

// LoadBlueprint reads a single blueprint by name, checking sources in order.
func LoadBlueprint(root, name string) (string, error) {
	dirs := resolvedSources(root)
	for _, dir := range dirs {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err == nil {
			return string(data), nil
		}
	}
	return "", fmt.Errorf("blueprint %q not found in any source (%d sources checked)", name, len(dirs))
}

// LoadBlueprints loads and concatenates multiple blueprints.
func LoadBlueprints(root string, names ...string) (string, error) {
	var parts []string
	for _, name := range names {
		content, err := LoadBlueprint(root, name)
		if err != nil {
			return "", err
		}
		parts = append(parts, content)
	}
	return strings.Join(parts, "\n\n---\n\n"), nil
}

// UpdateBlueprints removes all cached blueprint sources, forcing a re-fetch.
func UpdateBlueprints(root string) error {
	cacheBase := filepath.Join(root, ".weblisk", "blueprints")
	if err := os.RemoveAll(cacheBase); err != nil {
		return fmt.Errorf("clearing blueprint cache: %w", err)
	}
	fmt.Println("  Cleared blueprint cache.")

	// Re-fetch all sources.
	dirs := resolvedSources(root)
	if len(dirs) == 0 {
		return fmt.Errorf("no blueprint sources available after refresh")
	}
	fmt.Printf("  ✓ %d blueprint source(s) ready\n", len(dirs))
	return nil
}

// PlatformBlueprint returns the blueprint path for a platform.
func PlatformBlueprint(platform string) string {
	switch platform {
	case "cloudflare":
		return "platforms/cloudflare.md"
	default:
		return "platforms/go.md"
	}
}

// DomainBlueprint returns the blueprint path for an agent domain.
func DomainBlueprint(name string) string {
	return "domains/" + name + ".md"
}
