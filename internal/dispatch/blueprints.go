package dispatch

// ── Blueprint Loader ────────────────────────────────────────
//
// Loads Markdown blueprints from the weblisk-blueprints repository.
// Blueprints are cloned on first use and cached in .weblisk/blueprints/.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const blueprintRepo = "https://github.com/avaropoint/weblisk-blueprints.git"

// BlueprintSets defines which blueprints are loaded for each generation target.
var BlueprintSets = map[string][]string{
	"orchestrator": {"protocol/spec.md", "architecture/orchestrator.md", "protocol/identity.md"},
	"agent":        {"protocol/spec.md", "architecture/agent.md", "protocol/identity.md"},
}

// EnsureBlueprints clones or updates the blueprint repository.
func EnsureBlueprints(root string) (string, error) {
	cacheDir := filepath.Join(root, ".weblisk", "blueprints")

	if _, err := os.Stat(filepath.Join(cacheDir, "SCHEMA.md")); err == nil {
		return cacheDir, nil
	}

	fmt.Println("  Fetching blueprints...")
	if err := os.MkdirAll(filepath.Dir(cacheDir), 0755); err != nil {
		return "", fmt.Errorf("creating cache directory: %w", err)
	}

	os.RemoveAll(cacheDir)
	cmd := exec.Command("git", "clone", "--depth=1", blueprintRepo, cacheDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("cloning blueprints: %w", err)
	}

	fmt.Println("  ✓ Blueprints cached in .weblisk/blueprints/")
	return cacheDir, nil
}

// LoadBlueprint reads a single blueprint by name from the cached repo.
func LoadBlueprint(root, name string) (string, error) {
	cacheDir, err := EnsureBlueprints(root)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(cacheDir, name))
	if err != nil {
		return "", fmt.Errorf("blueprint %q not found: %w", name, err)
	}
	return string(data), nil
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
