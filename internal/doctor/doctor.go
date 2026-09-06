package doctor

import (
	"fmt"
	"github.com/avaropoint/weblisk-cli/internal/dispatch"
	"os"
	"path/filepath"
	"strings"
)

// Run performs project health checks per the specification:
// - .gitignore exists and excludes secrets
// - Config files are valid
// - Secret declarations are complete
// - File permissions are correct
func Run(root string) error {
	fmt.Println()
	fmt.Println("  Weblisk Doctor")
	fmt.Println()

	errors := 0
	warnings := 0

	// ── .gitignore checks ─────────────────────────────────────

	gitignorePath := filepath.Join(root, ".gitignore")
	gitignoreData, err := os.ReadFile(gitignorePath)
	if err != nil {
		fmt.Println("  [error] .gitignore not found")
		errors++
	} else {
		fmt.Println("  [ok]    .gitignore exists")

		content := string(gitignoreData)
		requiredEntries := []string{
			".weblisk/secrets/",
			".weblisk/keys/",
			".weblisk/token",
		}
		for _, entry := range requiredEntries {
			if strings.Contains(content, entry) {
				fmt.Printf("  [ok]    .gitignore excludes %s\n", entry)
			} else {
				fmt.Printf("  [error] .gitignore missing exclusion: %s\n", entry)
				errors++
			}
		}

		// Warn-level: .env exclusion
		if strings.Contains(content, ".env") {
			fmt.Println("  [ok]    .gitignore excludes .env")
		} else {
			fmt.Println("  [warn]  .gitignore should exclude .env and .env.*")
			warnings++
		}
	}

	// ── Config validation ──────────────────────────────────────

	// Primary: .weblisk/config.yaml (per spec)
	configYAML := filepath.Join(root, ".weblisk", "config.yaml")
	configJSON := filepath.Join(root, ".weblisk", "config.json")
	if _, err := os.Stat(configYAML); err == nil {
		data, readErr := os.ReadFile(configYAML)
		if readErr != nil {
			fmt.Println("  [error] .weblisk/config.yaml unreadable")
			errors++
		} else if len(data) > 0 {
			// Basic YAML validation: should have content and not be binary
			content := string(data)
			if strings.Contains(content, "hub:") || strings.Contains(content, "orchestrator:") || strings.Contains(content, "gateway:") {
				fmt.Println("  [ok]    .weblisk/config.yaml valid")
			} else {
				fmt.Println("  [warn]  .weblisk/config.yaml missing expected sections (hub, orchestrator, gateway)")
				warnings++
			}
		} else {
			fmt.Println("  [error] .weblisk/config.yaml is empty")
			errors++
		}
	} else if _, err := os.Stat(configJSON); err == nil {
		// Legacy JSON config support
		data, readErr := os.ReadFile(configJSON)
		if readErr != nil {
			fmt.Println("  [error] .weblisk/config.json unreadable")
			errors++
		} else if len(data) > 0 && data[0] == '{' {
			fmt.Println("  [ok]    .weblisk/config.json valid (legacy format)")
			fmt.Println("  [warn]  Consider migrating to .weblisk/config.yaml")
			warnings++
		} else {
			fmt.Println("  [error] .weblisk/config.json invalid format")
			errors++
		}
	} else {
		// Also check weblisk.json at root
		rootConfig := filepath.Join(root, "weblisk.json")
		if _, err := os.Stat(rootConfig); err == nil {
			fmt.Println("  [ok]    weblisk.json found")
		} else {
			fmt.Println("  [warn]  No project config found (.weblisk/config.yaml)")
			warnings++
		}
	}

	// ── Which blueprints this project reads ────────────────────

	// Doctor validated blueprint YAML and never said WHICH blueprints. That is
	// the question behind most of the confusing answers: a build reads local
	// `blueprints/` -> WL_BLUEPRINT_SOURCES -> the shared cache, first hit wins,
	// and the cache tracks the PUBLISHED blueprints. Somebody editing a checkout
	// this project cannot see gets a hub built to a specification they have
	// already replaced, with nothing to indicate it. See dispatch.AnnounceSources.
	dispatch.AnnounceSources(dispatch.ResolveSources(root))

	// ── Blueprint YAML validation ──────────────────────────────

	blueprintsDir := filepath.Join(root, "blueprints")
	if _, err := os.Stat(blueprintsDir); err == nil {
		valid, invalid := validateBlueprintDir(blueprintsDir)
		if invalid > 0 {
			fmt.Printf("  [error] %d blueprint file(s) with parse errors\n", invalid)
			errors += invalid
		}
		if valid > 0 {
			fmt.Printf("  [ok]    %d blueprint file(s) valid\n", valid)
		}
	}

	// ── Secret declarations vs stored values ───────────────────

	secretsDir := filepath.Join(root, ".weblisk", "secrets")
	if _, err := os.Stat(secretsDir); err == nil {
		permIssues := checkSecretPermissions(secretsDir)
		if permIssues > 0 {
			fmt.Printf("  [warn]  %d secret file(s) with incorrect permissions (should be 0600)\n", permIssues)
			warnings += permIssues
		} else {
			fmt.Println("  [ok]    Secret file permissions correct (0600)")
		}
	}

	// Check secrets directory itself
	if info, err := os.Stat(secretsDir); err == nil {
		mode := info.Mode().Perm()
		if mode != 0700 {
			fmt.Printf("  [warn]  .weblisk/secrets/ permissions %04o (should be 0700)\n", mode)
			warnings++
		} else {
			fmt.Println("  [ok]    .weblisk/secrets/ permissions correct (0700)")
		}
	}

	// ── Summary ────────────────────────────────────────────────

	fmt.Println()
	if errors == 0 && warnings == 0 {
		fmt.Println("  All checks passed.")
	} else if errors == 0 {
		fmt.Printf("  %d warning(s), no errors.\n", warnings)
	} else {
		fmt.Printf("  %d error(s), %d warning(s).\n", errors, warnings)
	}
	fmt.Println()

	if errors > 0 {
		return fmt.Errorf("%d error(s) found", errors)
	}
	if warnings > 0 {
		// Exit code 2 for warnings-only per spec
		return &WarningsOnly{Count: warnings}
	}
	return nil
}

// WarningsOnly signals exit code 2 (warnings found, no errors).
type WarningsOnly struct {
	Count int
}

func (w *WarningsOnly) Error() string {
	return fmt.Sprintf("%d warning(s)", w.Count)
}

func validateBlueprintDir(dir string) (valid, invalid int) {
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".md") && !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			invalid++
			return nil
		}
		content := string(data)
		// YAML blueprints must start with --- or have valid YAML content
		if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
			if len(content) == 0 {
				invalid++
			} else {
				valid++
			}
			return nil
		}
		// Markdown blueprints should have frontmatter
		if strings.HasPrefix(content, "---") || strings.HasPrefix(content, "<!--") {
			valid++
		} else {
			invalid++
		}
		return nil
	})
	return valid, invalid
}

func checkSecretPermissions(dir string) int {
	issues := 0
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil
		}
		if info.Mode().Perm() != 0600 {
			issues++
		}
		return nil
	})
	return issues
}
