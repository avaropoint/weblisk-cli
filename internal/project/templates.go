package project

// Resolves templates from multiple sources with fallthrough:
//   1. Local project:  ./templates/ in the user's project
//   2. Custom sources: WL_TEMPLATE_SOURCES (comma-separated Git URLs)
//   3. Core:           github.com/avaropoint/weblisk-templates (always)
//
// Templates are plain HTML/CSS/JS files — no template engine.
// Files are copied as-is with sensible defaults.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/avaropoint/weblisk-cli/internal/config"
)

const coreTemplateRepo = "https://github.com/avaropoint/weblisk-templates.git"

// Manifest represents the manifest.json structure.
//
// Two key names are read for the template map. The templates repository moved
// to "templates" at manifest v3; this CLI only read "scaffold", so the map
// unmarshalled empty, resolution fell through to a literal "scaffold/default/"
// path, and `weblisk new` failed with "scaffold set \"default\" not found in
// any source" against a repository that contained perfectly good templates.
//
// Both are accepted rather than one being migrated, because a template source
// is a SEPARATE repository that may be pinned, forked or vendored — a CLI that
// only understands the current shape cannot read a source it shipped alongside
// six months ago.
type Manifest struct {
	Version   string                   `json:"version"`
	Templates map[string]ManifestEntry `json:"templates"`
	Scaffold  map[string]ManifestEntry `json:"scaffold"`
	Init      map[string]ManifestInit  `json:"init"`
}

// sets returns the template map, whichever key the source used.
func (m *Manifest) sets() map[string]ManifestEntry {
	if len(m.Templates) > 0 {
		return m.Templates
	}
	return m.Scaffold
}

// defaultSet returns the name of the set marked default, or "" if none is.
//
// The manifest marks its default with a flag rather than by naming a set
// "default" — client/starter is the default, and there is no set called
// "default" anywhere. Looking one up by that literal name finds nothing.
func (m *Manifest) defaultSet() string {
	for name, e := range m.sets() {
		if e.Default {
			return name
		}
	}
	return ""
}

// ManifestEntry describes a scaffold set directory.
type ManifestEntry struct {
	Description string `json:"description"`
	Path        string `json:"path"`
	// Default marks the set used when the caller names none.
	Default bool `json:"default"`
	// Extends names a set applied first, so this one layers over it. The server
	// template extends the client template rather than duplicating it.
	Extends string `json:"extends"`
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
		sets := manifest.sets()
		if entry, ok := sets[setName]; ok {
			path = entry.Path
		} else if def := manifest.defaultSet(); def != "" {
			path = sets[def].Path
		}
	}

	for _, dir := range dirs {
		scaffoldDir := filepath.Join(dir, path)
		if info, err := os.Stat(scaffoldDir); err == nil && info.IsDir() {
			return scaffoldDir, nil
		}
	}
	// Name what IS available. "not found" against a source holding three usable
	// templates sends somebody looking for a network problem.
	if manifest != nil {
		if available := setNames(manifest.sets()); len(available) > 0 {
			return "", fmt.Errorf("template %q not found. This source offers: %s",
				setName, strings.Join(available, ", "))
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

// CopyScaffoldDir copies a scaffold set into the project directory.
func CopyScaffoldDir(scaffoldDir, projectDir string) (int, error) {
	return CopyScaffoldDirWith(scaffoldDir, projectDir, nil)
}

// isBinaryContent reports whether data should be copied verbatim.
//
// Substitution is a text operation. Running it over a PNG or a font would
// corrupt bytes that happen to spell a placeholder, so binary files are copied
// untouched. A NUL byte in the first 8 KB is the usual, cheap signal.
func isBinaryContent(data []byte) bool {
	n := len(data)
	if n > 8192 {
		n = 8192
	}
	for i := 0; i < n; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// CopyScaffoldDirWith copies a scaffold set, substituting {{placeholder}} values.
//
// Without this a scaffolded project ships with literal {{name}} and {{domain}}
// in its content — 37 occurrences across five files in the server template,
// including .weblisk/config.yaml, which is the hub's own configuration. A
// project that cannot start because its config names a placeholder is not a
// scaffold, it is a puzzle.
//
// An UNKNOWN placeholder is left exactly as it is rather than blanked. A
// template may legitimately contain syntax this CLI does not know about, and
// silently emptying it would corrupt content in a way nobody could trace back
// to here.
func CopyScaffoldDirWith(scaffoldDir, projectDir string, vars map[string]string) (int, error) {
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

		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		if len(vars) > 0 && !isBinaryContent(data) {
			text := string(data)
			for k, v := range vars {
				text = strings.ReplaceAll(text, "{{"+k+"}}", v)
			}
			data = []byte(text)
		}
		count++
		return os.WriteFile(dest, data, 0644)
	})

	return count, err
}

// CopyInitFiles copies init config files (env, gitignore) into the project.
func CopyInitFiles(root, projectDir string) error {
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

// setNames lists template names, sorted so the message is stable.
func setNames(sets map[string]ManifestEntry) []string {
	out := make([]string, 0, len(sets))
	for name := range sets {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ResolveScaffoldChain returns the scaffold directories to apply in order,
// following `extends` so a layered template lands on top of its base.
//
// server/starter extends client/starter and contains only the additions. Applied
// alone it produces a project missing everything the client template provides.
func ResolveScaffoldChain(root, setName string) ([]string, error) {
	manifest, _ := LoadManifest(root)
	if manifest == nil {
		dir, err := ResolveScaffoldDir(root, setName)
		if err != nil {
			return nil, err
		}
		return []string{dir}, nil
	}
	sets := manifest.sets()
	if setName == "" {
		setName = manifest.defaultSet()
	}

	var chain []string
	seen := map[string]bool{}
	for name := setName; name != ""; {
		if seen[name] {
			return nil, fmt.Errorf("template %q extends itself", name)
		}
		seen[name] = true
		dir, err := ResolveScaffoldDir(root, name)
		if err != nil {
			return nil, err
		}
		// Base first: prepend, so the extending template is applied last.
		chain = append([]string{dir}, chain...)
		name = sets[name].Extends
	}
	return chain, nil
}
