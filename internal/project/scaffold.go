package project

// Project Scaffold
//
// Creates new Weblisk projects by copying a scaffold set from
// weblisk-templates. Templates are plain HTML/CSS/JS files — the
// CLI does simple string replacement for the project name and
// CDN base path. No template engine required.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/avaropoint/weblisk-cli/internal/config"
)

const clientRepo = "https://github.com/avaropoint/weblisk.git"

// Scaffold creates a new Weblisk project directory.
func Scaffold(name, cwd, tmpl string, local bool, lib string) error {
	if lib == "" {
		lib = config.DefaultLib
	}
	lib = strings.TrimRight(lib, "/")

	projectDir := filepath.Join(cwd, name)

	if _, err := os.Stat(projectDir); err == nil {
		return fmt.Errorf("directory %q already exists", name)
	}

	if err := os.MkdirAll(projectDir, 0755); err != nil {
		return fmt.Errorf("creating project directory: %w", err)
	}

	fmt.Printf("\n  Creating %s\n\n", name)

	// Resolve and copy the scaffold set directory.
	scaffoldDir, err := ResolveScaffoldDir(cwd, tmpl)
	if err != nil {
		return err
	}

	count, err := CopyScaffoldDir(scaffoldDir, projectDir, name, local, lib)
	if err != nil {
		return err
	}
	fmt.Printf("    %d files\n", count)

	// Copy init config files (.env, .gitignore).
	if err := CopyInitFiles(cwd, projectDir, name); err != nil {
		return err
	}
	fmt.Printf("    .env\n")
	fmt.Printf("    .gitignore\n")

	// Write weblisk.json
	configJSON := fmt.Sprintf(`{
  "name": "%s",
  "version": "1.0.0"`, name)
	if lib != config.DefaultLib {
		configJSON += fmt.Sprintf(`,
  "lib": "%s"`, lib)
	}
	configJSON += "\n}\n"
	configPath := filepath.Join(projectDir, "weblisk.json")
	if err := writeFile(configPath, configJSON); err != nil {
		return err
	}
	fmt.Printf("    weblisk.json\n")

	// Extract local framework if requested (git clone + cache)
	if local {
		count, err := extractFramework(projectDir, lib)
		if err != nil {
			return err
		}
		fmt.Printf("    %s/ (%d files)\n", lib, count)
	}

	fmt.Println()
	fmt.Printf("  Done! Next steps:\n")
	fmt.Printf("    cd %s\n", name)
	fmt.Printf("    weblisk dev\n\n")

	return nil
}

func writeFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}
	return os.WriteFile(path, []byte(content), 0644)
}

// extractFramework clones the weblisk repo and copies lib/weblisk/ into the
// user-specified lib directory. The clone is cached at .weblisk/client/.
func extractFramework(projectDir, lib string) (int, error) {
	cacheDir := filepath.Join(projectDir, ".weblisk", "client")

	if _, err := os.Stat(filepath.Join(cacheDir, "lib", "weblisk", "weblisk.js")); err != nil {
		fmt.Println("    Fetching client framework...")
		os.RemoveAll(cacheDir)
		if err := os.MkdirAll(filepath.Dir(cacheDir), 0755); err != nil {
			return 0, fmt.Errorf("creating cache dir: %w", err)
		}
		cmd := exec.Command("git", "clone", "--depth=1", clientRepo, cacheDir)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return 0, fmt.Errorf("cloning client framework: %w", err)
		}
	}

	srcDir := filepath.Join(cacheDir, "lib", "weblisk")
	destDir := filepath.Join(projectDir, lib)
	count := 0

	err := filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcDir, path)
		if rel == "." {
			return nil
		}
		dest := filepath.Join(destDir, rel)
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
		count++
		return os.WriteFile(dest, data, 0644)
	})

	return count, err
}

// Helpers

func toScaffoldTitle(name string) string {
	name = strings.ReplaceAll(name, "-", " ")
	name = strings.ReplaceAll(name, "_", " ")
	words := strings.Fields(name)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
