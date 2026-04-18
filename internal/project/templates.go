package project

// Resolves templates from multiple sources with fallthrough:
//   1. Local project:  ./templates/ in the user's project
//   2. Custom sources: WL_TEMPLATE_SOURCES (comma-separated Git URLs)
//   3. Core:           github.com/avaropoint/weblisk-templates (always)
//
// Templates are plain HTML/CSS/JS files — no template engine.
// The CLI does simple string replacement for project name and CDN base.

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

const defaultName = "My App"
const defaultCDN = "https://cdn.weblisk.dev/"

// Manifest represents the manifest.json structure.
type Manifest struct {
	Version  string                    `json:"version"`
	Defaults ManifestDefaults          `json:"defaults"`
	Scaffold map[string]ManifestEntry  `json:"scaffold"`
	Pages    map[string]ManifestFile   `json:"pages"`
	Islands  map[string]ManifestFile   `json:"islands"`
	Init     map[string]ManifestInit   `json:"init"`
}

// ManifestDefaults holds the placeholder values used in templates.
type ManifestDefaults struct {
	Name string `json:"name"`
	CDN  string `json:"cdn"`
}

// ManifestEntry describes a scaffold set directory.
type ManifestEntry struct {
	Description string `json:"description"`
	Path        string `json:"path"`
}

// ManifestFile describes a single template file.
type ManifestFile struct {
	Description string `json:"description"`
	File        string `json:"file"`
}

// ManifestInit describes an init config file and its output destination.
type ManifestInit struct {
	Dest string `json:"dest"`
	File string `json:"file"`
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

// LoadManifest reads manifest.json from the first source that has one.
func LoadManifest(root string) (*Manifest, error) {
	dirs := resolvedTemplateSources(root)
	for _, dir := range dirs {
		path := filepath.Join(dir, "manifest.json")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			fmt.Fprintf(os.Stderr, "  [warn] Invalid manifest.json in %s: %v\n", dir, err)
			continue
		}
		return &m, nil
	}
	return nil, fmt.Errorf("no manifest.json found in any template source")
}

// ResolveScaffoldDir finds the scaffold set directory, checking sources in order.
// Returns the absolute path to the scaffold set directory.
func ResolveScaffoldDir(root, setName string) (string, error) {
	dirs := resolvedTemplateSources(root)

	// Try the manifest-declared path first.
	manifest, _ := LoadManifest(root)
	path := "scaffold/" + setName + "/"
	if manifest != nil {
		if entry, ok := manifest.Scaffold[setName]; ok {
			path = entry.Path
		} else if entry, ok := manifest.Scaffold["default"]; ok {
			path = entry.Path
		}
	}

	for _, dir := range dirs {
		scaffoldDir := filepath.Join(dir, path)
		if info, err := os.Stat(scaffoldDir); err == nil && info.IsDir() {
			return scaffoldDir, nil
		}
	}
	return "", fmt.Errorf("scaffold set %q not found in any source", setName)
}

// ResolveFile reads a single file by relative path, checking sources in order.
func ResolveFile(root, relPath string) (string, error) {
	dirs := resolvedTemplateSources(root)
	for _, dir := range dirs {
		data, err := os.ReadFile(filepath.Join(dir, relPath))
		if err == nil {
			return string(data), nil
		}
	}
	return "", fmt.Errorf("template file %q not found in any source", relPath)
}

// ApplyReplacements performs project-specific string substitutions on content.
// Replaces the default placeholder name and optionally swaps CDN for local path.
func ApplyReplacements(content, projectName string, local bool, lib string) string {
	title := toScaffoldTitle(projectName)
	slug := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(projectName, " ", "-"), "_", "-"))

	content = strings.ReplaceAll(content, defaultName, title)
	content = strings.ReplaceAll(content, "my-app", slug)

	if local {
		content = strings.ReplaceAll(content, defaultCDN, "/"+lib+"/")
	}

	return content
}

// CopyScaffoldDir copies a scaffold set into the project directory,
// applying string replacements to all text files.
func CopyScaffoldDir(scaffoldDir, projectDir, projectName string, local bool, lib string) (int, error) {
	count := 0

	err := filepath.WalkDir(scaffoldDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(scaffoldDir, path)
		if rel == "." {
			return nil
		}
		dest := filepath.Join(projectDir, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0755)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		// Apply replacements to text files.
		content := ApplyReplacements(string(data), projectName, local, lib)

		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		count++
		return os.WriteFile(dest, []byte(content), 0644)
	})

	return count, err
}

// CopyInitFiles copies init config files (env, gitignore) into the project.
func CopyInitFiles(root, projectDir, projectName string) error {
	manifest, err := LoadManifest(root)
	if err != nil {
		return nil // No manifest — skip init files.
	}

	for _, entry := range manifest.Init {
		content, err := ResolveFile(root, entry.File)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [warn] Init file %s: %v\n", entry.File, err)
			continue
		}
		content = ApplyReplacements(content, projectName, false, "")
		dest := filepath.Join(projectDir, entry.Dest)
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, []byte(content), 0644); err != nil {
			return err
		}
	}

	return nil
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
