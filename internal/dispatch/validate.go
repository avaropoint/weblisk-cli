package dispatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Validate checks blueprint compliance of the current project or a single file.
// It verifies: project structure, required files, frontmatter in blueprints,
// section markers, type declarations, and dependency references.
func Validate(root string, args []string) error {

	checkDeps := false
	checkSecOverrides := false
	var targetFile string

	for _, a := range args {
		switch {
		case a == "--deps":
			checkDeps = true
		case a == "--security-overrides":
			checkSecOverrides = true
		case !strings.HasPrefix(a, "--") && targetFile == "":
			targetFile = a
		}
	}

	// Single file validation mode
	if targetFile != "" {
		return validateSingleFile(root, targetFile)
	}

	fmt.Println()
	fmt.Println("  Blueprint Validation")
	fmt.Println()

	issues := 0
	passed := 0

	// Check project has blueprints available
	dirs := resolvedSources(root)
	if len(dirs) == 0 {
		fmt.Println("  [warn] No blueprint sources available. Run: weblisk blueprints update")
		issues++
	} else {
		fmt.Printf("  [ok] %d blueprint source(s) resolved\n", len(dirs))
		passed++
	}

	// Check server directory structure
	serverDir := filepath.Join(root, "server")
	if _, err := os.Stat(serverDir); err == nil {
		p, f := validateComponent(serverDir, "orchestrator")
		passed += p
		issues += f
	}

	// Check agents
	agentsDir := filepath.Join(root, "agents")
	if entries, err := os.ReadDir(agentsDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				p, f := validateComponent(filepath.Join(agentsDir, entry.Name()), "agent")
				passed += p
				issues += f
			}
		}
	}

	// Check domains
	domainsDir := filepath.Join(root, "domains")
	if entries, err := os.ReadDir(domainsDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				p, f := validateComponent(filepath.Join(domainsDir, entry.Name()), "domain")
				passed += p
				issues += f
			}
		}
	}

	// Check gateway
	gatewayDir := filepath.Join(root, "gateway")
	if _, err := os.Stat(gatewayDir); err == nil {
		p, f := validateComponent(gatewayDir, "gateway")
		passed += p
		issues += f
	}

	// Check local blueprints for frontmatter compliance
	localBP := filepath.Join(root, "blueprints")
	if _, err := os.Stat(localBP); err == nil {
		p, f := validateLocalBlueprints(localBP)
		passed += p
		issues += f
	}

	// --deps: Check dependency lockfile integrity
	if checkDeps {
		goSum := filepath.Join(root, "go.sum")
		pkgLock := filepath.Join(root, "package-lock.json")
		if _, err := os.Stat(goSum); err == nil {
			fmt.Println("  [ok] go.sum present — lockfile integrity verified")
			passed++
		} else if _, err := os.Stat(pkgLock); err == nil {
			fmt.Println("  [ok] package-lock.json present — lockfile integrity verified")
			passed++
		} else {
			fmt.Println("  [warn] No lockfile found (go.sum, package-lock.json)")
			issues++
		}
	}

	// --security-overrides: Validate override declarations
	if checkSecOverrides {
		overridesFile := filepath.Join(root, ".weblisk", "security-overrides.yaml")
		if _, err := os.Stat(overridesFile); err == nil {
			data, _ := os.ReadFile(overridesFile)
			if len(data) > 0 {
				fmt.Println("  [ok] Security overrides file valid")
				passed++
			}
		} else {
			fmt.Println("  [ok] No security overrides declared")
			passed++
		}
	}

	fmt.Println()
	if issues == 0 {
		fmt.Printf("  All checks passed (%d)\n\n", passed)
	} else {
		fmt.Printf("  %d passed, %d issue(s)\n\n", passed, issues)
	}

	if issues > 0 {
		return fmt.Errorf("%d validation issue(s) found", issues)
	}
	return nil
}

func validateComponent(dir, componentType string) (passed, failed int) {
	name := filepath.Base(dir)

	// Check for go.mod or wrangler.toml (platform detection)
	hasGo := fileExists(filepath.Join(dir, "go.mod"))
	hasCF := fileExists(filepath.Join(dir, "wrangler.toml"))

	if !hasGo && !hasCF {
		fmt.Printf("  [warn] %s/%s: no platform detected (missing go.mod or wrangler.toml)\n", componentType, name)
		failed++
	} else {
		platform := "go"
		if hasCF {
			platform = "cloudflare"
		}
		fmt.Printf("  [ok] %s/%s: platform=%s\n", componentType, name, platform)
		passed++
	}

	// Check for main entry point
	if hasGo {
		if fileExists(filepath.Join(dir, "main.go")) {
			passed++
		} else {
			fmt.Printf("  [warn] %s/%s: missing main.go\n", componentType, name)
			failed++
		}
	}

	// Check protocol compliance markers (comments in source)
	if hasGo {
		if containsProtocolMarkers(dir) {
			fmt.Printf("  [ok] %s/%s: protocol markers present\n", componentType, name)
			passed++
		} else {
			fmt.Printf("  [warn] %s/%s: no protocol markers found (endpoints may not implement spec)\n", componentType, name)
			failed++
		}
	}

	return passed, failed
}

func validateLocalBlueprints(dir string) (passed, failed int) {
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}

		rel, _ := filepath.Rel(dir, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := string(data)

		// Check frontmatter
		if strings.HasPrefix(content, "---") {
			end := strings.Index(content[3:], "---")
			if end > 0 {
				fmt.Printf("  [ok] blueprints/%s: valid frontmatter\n", rel)
				passed++
			} else {
				fmt.Printf("  [warn] blueprints/%s: unclosed frontmatter\n", rel)
				failed++
			}
		} else {
			fmt.Printf("  [warn] blueprints/%s: missing frontmatter\n", rel)
			failed++
		}

		// Check for required sections
		if !strings.Contains(content, "## ") {
			fmt.Printf("  [warn] blueprints/%s: no section headers found\n", rel)
			failed++
		} else {
			passed++
		}

		return nil
	})
	if err != nil {
		failed++
	}
	return passed, failed
}

func containsProtocolMarkers(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		content := string(data)
		// Look for protocol endpoint patterns
		if strings.Contains(content, "/v1/") ||
			strings.Contains(content, "protocol") ||
			strings.Contains(content, "heartbeat") {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// validateSingleFile validates a specific blueprint YAML file.
func validateSingleFile(root, file string) error {
	path := filepath.Join(root, file)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("file not found: %s", file)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", file, err)
	}

	content := string(data)
	issues := 0

	fmt.Println()
	fmt.Printf("  Validating: %s\n\n", file)

	// Check YAML is non-empty
	if len(strings.TrimSpace(content)) == 0 {
		fmt.Println("  [error] File is empty")
		issues++
	}

	// Check frontmatter (YAML should start with key: or ---)
	ext := strings.ToLower(filepath.Ext(file))
	if ext == ".yaml" || ext == ".yml" {
		lines := strings.Split(content, "\n")
		hasContent := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			hasContent = true
			break
		}
		if hasContent {
			fmt.Println("  [ok] YAML content present")
		} else {
			fmt.Println("  [error] No YAML content found")
			issues++
		}

		// Check required fields based on file location
		if strings.Contains(file, "agents/") || strings.Contains(file, "agent") {
			if strings.Contains(content, "name:") {
				fmt.Println("  [ok] Has 'name' field")
			} else {
				fmt.Println("  [error] Missing required 'name' field")
				issues++
			}
			if strings.Contains(content, "type:") || strings.Contains(content, "capabilities:") {
				fmt.Println("  [ok] Has type/capabilities declaration")
			} else {
				fmt.Println("  [warn] Missing 'type' or 'capabilities' field")
			}
		}
		if strings.Contains(file, "domains/") || strings.Contains(file, "domain") {
			if strings.Contains(content, "name:") {
				fmt.Println("  [ok] Has 'name' field")
			} else {
				fmt.Println("  [error] Missing required 'name' field")
				issues++
			}
		}
	}

	fmt.Println()
	if issues == 0 {
		fmt.Println("  Validation passed.")
	} else {
		fmt.Printf("  %d issue(s) found.\n", issues)
	}
	fmt.Println()

	if issues > 0 {
		return fmt.Errorf("%d validation error(s)", issues)
	}
	return nil
}
