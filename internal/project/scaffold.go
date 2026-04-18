package project

// Project Scaffold
//
// Creates new Weblisk projects from templates resolved via
// multi-source template resolution. Templates are fetched from
// weblisk-templates or overridden via local/custom sources.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

const clientRepo = "https://github.com/avaropoint/weblisk.git"

// TplData holds the template rendering context.
type TplData struct {
	Name       string
	Title      string
	TitleLower string
	Year       string
	CDNBase    string
	Port       string
}

// RenderTpl renders a template from the resolved sources with the given data.
// The category/name is resolved via multi-source template resolution.
func RenderTpl(root, category, name string, data TplData) (string, error) {
	content, err := ResolveTemplate(root, category, name)
	if err != nil {
		return "", err
	}

	tmpl, err := template.New(name).Parse(content)
	if err != nil {
		return "", fmt.Errorf("parsing template %q: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing template %q: %w", name, err)
	}

	return buf.String(), nil
}

// ReadTpl reads a raw template without rendering.
func ReadTpl(root, category, name string) (string, error) {
	return ResolveTemplate(root, category, name)
}

// Scaffold creates a new Weblisk project directory.
func Scaffold(name, cwd, tmpl string, local bool) error {
	projectDir := filepath.Join(cwd, name)

	if _, err := os.Stat(projectDir); err == nil {
		return fmt.Errorf("directory %q already exists", name)
	}

	if err := os.MkdirAll(projectDir, 0755); err != nil {
		return fmt.Errorf("creating project directory: %w", err)
	}

	title := toScaffoldTitle(name)
	cdnBase := "https://cdn.weblisk.dev/v1/"
	if local {
		cdnBase = "/lib/weblisk/"
	}

	data := TplData{
		Name:       name,
		Title:      title,
		TitleLower: strings.ToLower(title),
		Year:       fmt.Sprintf("%d", time.Now().Year()),
		CDNBase:    cdnBase,
		Port:       "3000",
	}

	fmt.Printf("\n  Creating %s\n\n", name)

	// Resolve scaffold templates from multi-source resolution
	pages, err := ResolveTemplateSet(cwd, tmpl)
	if err != nil {
		return err
	}
	for _, page := range pages {
		outPath := templateOutputPath(page, projectDir)
		if err := writeRenderedTpl(cwd, "scaffold", page, outPath, data); err != nil {
			return err
		}
		rel, _ := filepath.Rel(projectDir, outPath)
		fmt.Printf("    %s\n", rel)
	}

	// Render core files from resolved sources
	coreTemplates := ResolveCoreTemplates(cwd)
	for _, ct := range coreTemplates {
		dest := coreOutputPath(ct, projectDir)
		if err := writeRenderedTpl(cwd, "core", ct, dest, data); err != nil {
			return err
		}
		rel, _ := filepath.Rel(projectDir, dest)
		fmt.Printf("    %s\n", rel)
	}

	// Write weblisk.json
	configContent := fmt.Sprintf(`{
  "name": "%s",
  "version": "1.0.0"
}
`, name)
	configPath := filepath.Join(projectDir, "weblisk.json")
	if err := writeScaffoldFile(configPath, configContent); err != nil {
		return err
	}
	fmt.Printf("    weblisk.json\n")

	// Extract local framework if requested (git clone + cache)
	if local {
		count, err := extractFramework(projectDir)
		if err != nil {
			return err
		}
		fmt.Printf("    lib/weblisk/ (%d files)\n", count)
	}

	fmt.Println()
	fmt.Printf("  Done! Next steps:\n")
	fmt.Printf("    cd %s\n", name)
	fmt.Printf("    weblisk dev\n\n")

	return nil
}

func templateOutputPath(tpl, projectDir string) string {
	name := strings.TrimSuffix(tpl, ".tpl")
	if name == "home.html" {
		name = "index.html"
	}
	return filepath.Join(projectDir, "app", name)
}

// coreOutputPath maps a core template name to its output destination.
func coreOutputPath(tpl, projectDir string) string {
	switch tpl {
	case "styles.css.tpl":
		return filepath.Join(projectDir, "app", "css", "styles.css")
	case "sw.js.tpl":
		return filepath.Join(projectDir, "app", "sw.js")
	case "shell.js.tpl":
		return filepath.Join(projectDir, "app", "js", "islands", "shell.js")
	case "env.tpl":
		return filepath.Join(projectDir, ".env")
	case "gitignore.tpl":
		return filepath.Join(projectDir, ".gitignore")
	default:
		name := strings.TrimSuffix(tpl, ".tpl")
		return filepath.Join(projectDir, name)
	}
}

// File Writers

func writeRenderedTpl(root, category, tpl, dest string, data TplData) error {
	content, err := RenderTpl(root, category, tpl, data)
	if err != nil {
		return err
	}
	return writeScaffoldFile(dest, content)
}

func writeScaffoldFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}
	return os.WriteFile(path, []byte(content), 0644)
}

// extractFramework clones the weblisk repo and copies lib/weblisk/ into the project.
// The clone is cached at .weblisk/client/ so subsequent scaffolds don't re-download.
func extractFramework(projectDir string) (int, error) {
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
	destDir := filepath.Join(projectDir, "lib", "weblisk")
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
