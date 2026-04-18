package project

// Resolves templates from multiple sources with fallthrough:
//   1. Local project:  ./templates/ in the user's project
//   2. Custom sources: WL_TEMPLATE_SOURCES (comma-separated Git URLs)
//   3. Core:           github.com/avaropoint/weblisk-templates (always)

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/avaropoint/weblisk-cli/internal/config"
)

const coreTemplateRepo = "https://github.com/avaropoint/weblisk-templates.git"

// TemplateManifest represents the templates.json structure.
type TemplateManifest struct {
	Version    string                       `json:"version"`
	Categories map[string]TemplateCategory  `json:"categories"`
	Sets       map[string]TemplateSet       `json:"sets"`
	Core       []string                     `json:"core"`
}

// TemplateCategory describes a template directory.
type TemplateCategory struct {
	Description string `json:"description"`
	Path        string `json:"path"`
}

// TemplateSet defines which scaffold templates to use for a project type.
type TemplateSet struct {
	Description string   `json:"description"`
	Scaffold    []string `json:"scaffold"`
}

// templateSourceDir returns a deterministic cache directory name for a repo URL.
func templateSourceDir(repoURL string) string {
	name := repoURL
	name = strings.TrimSuffix(name, ".git")
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	h := sha256.Sum256([]byte(repoURL))
	return fmt.Sprintf("%s-%x", name, h[:4])
}

// resolvedTemplateSources returns the ordered list of template directories to search.
// Order: local project → custom sources → core.
func resolvedTemplateSources(root string) []string {
	var dirs []string

	// 1. Local project templates (highest priority).
	localDir := filepath.Join(root, "templates")
	if info, err := os.Stat(localDir); err == nil && info.IsDir() {
		dirs = append(dirs, localDir)
	}

	// 2. Custom sources from WL_TEMPLATE_SOURCES.
	cfg := config.Resolve()
	for _, repo := range cfg.TemplateSources {
		cacheDir := filepath.Join(root, ".weblisk", "templates", templateSourceDir(repo))
		if err := ensureTemplatesCloned(repo, cacheDir); err != nil {
			fmt.Fprintf(os.Stderr, "  [warn] Template source %s: %v\n", repo, err)
			continue
		}
		dirs = append(dirs, cacheDir)
	}

	// 3. Core templates (always present as fallback).
	coreDir := filepath.Join(root, ".weblisk", "templates", templateSourceDir(coreTemplateRepo))
	if err := ensureTemplatesCloned(coreTemplateRepo, coreDir); err != nil {
		fmt.Fprintf(os.Stderr, "  [warn] Core templates: %v\n", err)
	} else {
		dirs = append(dirs, coreDir)
	}

	return dirs
}

// ensureTemplatesCloned clones a template repo if it hasn't been cached yet.
func ensureTemplatesCloned(repoURL, cacheDir string) error {
	if entries, err := os.ReadDir(cacheDir); err == nil && len(entries) > 0 {
		return nil
	}

	fmt.Printf("  Fetching templates from %s...\n", repoURL)
	if err := os.MkdirAll(filepath.Dir(cacheDir), 0755); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}

	os.RemoveAll(cacheDir)
	cmd := exec.Command("git", "clone", "--depth=1", repoURL, cacheDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cloning %s: %w", repoURL, err)
	}

	fmt.Printf("  [ok] Cached %s\n", filepath.Base(cacheDir))
	return nil
}

// LoadManifest reads templates.json from the first source that has one.
func LoadManifest(root string) (*TemplateManifest, error) {
	dirs := resolvedTemplateSources(root)
	for _, dir := range dirs {
		path := filepath.Join(dir, "templates.json")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m TemplateManifest
		if err := json.Unmarshal(data, &m); err != nil {
			fmt.Fprintf(os.Stderr, "  [warn] Invalid templates.json in %s: %v\n", dir, err)
			continue
		}
		return &m, nil
	}
	return nil, fmt.Errorf("no templates.json found in any template source")
}

// ResolveTemplate reads a template file by category and name, checking sources in order.
// For example: ResolveTemplate(root, "scaffold", "home.html.tpl")
func ResolveTemplate(root, category, name string) (string, error) {
	dirs := resolvedTemplateSources(root)
	for _, dir := range dirs {
		path := filepath.Join(dir, category, name)
		data, err := os.ReadFile(path)
		if err == nil {
			return string(data), nil
		}
	}
	return "", fmt.Errorf("template %s/%s not found in any source (%d sources checked)", category, name, len(dirs))
}

// ResolveTemplateSet returns the list of scaffold template names for a given set.
// Falls back to the "default" set if the requested set doesn't exist.
func ResolveTemplateSet(root, setName string) ([]string, error) {
	manifest, err := LoadManifest(root)
	if err != nil {
		// Fallback: hardcoded default if no manifest is available.
		return []string{"home.html.tpl", "404.html.tpl"}, nil
	}

	if set, ok := manifest.Sets[setName]; ok {
		return set.Scaffold, nil
	}
	if set, ok := manifest.Sets["default"]; ok {
		return set.Scaffold, nil
	}
	return []string{"home.html.tpl", "404.html.tpl"}, nil
}

// ResolveCoreTemplates returns the list of core template names that are always included.
func ResolveCoreTemplates(root string) []string {
	manifest, err := LoadManifest(root)
	if err != nil {
		return []string{"styles.css.tpl", "sw.js.tpl", "shell.js.tpl", "env.tpl", "gitignore.tpl"}
	}
	return manifest.Core
}

// UpdateTemplates removes all cached template sources, forcing a re-fetch.
func UpdateTemplates(root string) error {
	cacheBase := filepath.Join(root, ".weblisk", "templates")
	if err := os.RemoveAll(cacheBase); err != nil {
		return fmt.Errorf("clearing template cache: %w", err)
	}
	fmt.Println("  Cleared template cache.")

	dirs := resolvedTemplateSources(root)
	if len(dirs) == 0 {
		return fmt.Errorf("no template sources available after refresh")
	}
	fmt.Printf("  [ok] %d template source(s) ready\n", len(dirs))
	return nil
}
