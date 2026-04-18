package project

// Project Scaffold
//
// Creates new Weblisk projects from embedded templates.
// Renders .tpl files with project-specific data. Framework
// files are fetched from the weblisk repo when --local is used.

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

//go:embed all:templates
var templateFS embed.FS

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

// RenderTpl renders a named template with the given data.
func RenderTpl(name string, data TplData) (string, error) {
	content, err := templateFS.ReadFile("templates/" + name)
	if err != nil {
		return "", fmt.Errorf("template %q not found: %w", name, err)
	}

	tmpl, err := template.New(name).Parse(string(content))
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
func ReadTpl(name string) (string, error) {
	content, err := templateFS.ReadFile("templates/" + name)
	if err != nil {
		return "", fmt.Errorf("template %q not found: %w", name, err)
	}
	return string(content), nil
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

	// Render templates based on chosen template set
	pages := templateSet(tmpl)
	for _, page := range pages {
		outPath := templateOutputPath(page, projectDir)
		if err := writeRenderedTpl(page, outPath, data); err != nil {
			return err
		}
		rel, _ := filepath.Rel(projectDir, outPath)
		fmt.Printf("    %s\n", rel)
	}

	// Always render core files
	coreFiles := []struct {
		tpl  string
		dest string
	}{
		{"styles.css.tpl", filepath.Join(projectDir, "app", "css", "styles.css")},
		{"sw.js.tpl", filepath.Join(projectDir, "app", "sw.js")},
		{"shell.js.tpl", filepath.Join(projectDir, "app", "js", "islands", "shell.js")},
		{"env.tpl", filepath.Join(projectDir, ".env")},
		{"gitignore.tpl", filepath.Join(projectDir, ".gitignore")},
	}

	for _, cf := range coreFiles {
		if err := writeRenderedTpl(cf.tpl, cf.dest, data); err != nil {
			return err
		}
		rel, _ := filepath.Rel(projectDir, cf.dest)
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

// Template Sets

func templateSet(tmpl string) []string {
	base := []string{"home.html.tpl", "404.html.tpl"}

	switch tmpl {
	case "blog":
		return append(base, "blog.html.tpl", "about.html.tpl")
	case "dashboard":
		return append(base, "dashboard.html.tpl", "settings.html.tpl")
	case "docs":
		return append(base, "docs.html.tpl")
	default:
		return base
	}
}

func templateOutputPath(tpl, projectDir string) string {
	name := strings.TrimSuffix(tpl, ".tpl")
	if name == "home.html" {
		name = "index.html"
	}
	return filepath.Join(projectDir, "app", name)
}

// File Writers

func writeRenderedTpl(tpl, dest string, data TplData) error {
	content, err := RenderTpl(tpl, data)
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
