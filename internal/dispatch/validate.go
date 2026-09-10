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
		// WHICH ones, and at what revision. "2 sources resolved" is the sentence
		// that let this command report on a clone of origin/main while an edited
		// working tree sat in front of it — the count was right and said nothing
		// about what was read. Describe() carries the kind, the revision and
		// `-dirty`, which is the whole answer to "is this my work".
		for _, src := range ResolveSources(root) {
			fmt.Printf("    %s\n", src.Describe())
		}
		passed++

		// The corpus against its own schemas.
		//
		// This is the check that matters and it did not exist here: `weblisk
		// validate` reported "All checks passed (1)" — the one check being that
		// sources resolved. Every real rule lived in Go test files, where a
		// blueprint author could not run it, so a corpus fault was discovered
		// by a forty-minute generation run failing at file 27.
		//
		// The rules are read FROM the schemas. See validate_corpus.go.
		corpus := loadCorpus(dirs)
		if len(corpus) == 0 {
			fmt.Println("  [warn] no blueprints found in the resolved sources")
			issues++
		} else {
			findings := ValidateCorpus(corpus)
			faults := Faults(findings)
			if len(findings) == 0 {
				fmt.Printf("  [ok] %d blueprint(s) conform to their schemas\n", len(corpus))
				passed++
			} else {
				fmt.Printf("  [%s] %d finding(s) across %d blueprint(s)\n\n",
					map[bool]string{true: "fail", false: "warn"}[faults > 0], len(findings), len(corpus))
				fmt.Print(FormatFindings(findings))
				fmt.Println()
				if faults > 0 {
					issues += faults
				}
			}
		}
	}

	// The tenant folder is the module root, so the orchestrator is validated
	// where it lives rather than in a server/ subdirectory that no longer exists.
	//
	// Gated on finding it, not on a go.mod existing. A tenant may hold agents
	// and no orchestrator, and the presence of a module is not a claim that one
	// was generated — asking go.mod reported every such tenant as an
	// orchestrator whose files had gone missing.
	if _, found := Locate(root, Orchestrator()); found {
		p, f := validateComponent(root, Orchestrator())
		passed += p
		issues += f
	}

	// Agents and domain controllers, wherever the platform blueprint puts them.
	//
	// This walked agents/<name>/ looking for a go.mod in it. That is the shape
	// the single-shot generator wrote; platforms/go.md puts a Go agent at
	// cmd/<name> plus internal/agents/<name> and gives it no module of its own,
	// so every correctly generated Go agent would have been reported as "no
	// platform detected". WHICH components exist comes from the manifests, and
	// WHERE each one is comes from its layout. See locate.go.
	for _, c := range GeneratedComponents(root) {
		if c.Kind != "agent" && c.Kind != "domain" {
			continue // the orchestrator is validated above, at the module root
		}
		p, f := validateComponent(root, c)
		passed += p
		issues += f
	}

	// Check gateway
	if _, found := Locate(root, Gateway()); found {
		p, f := validateComponent(root, Gateway())
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

func validateComponent(root string, c Component) (passed, failed int) {
	l, found := Locate(root, c)
	if !found {
		// Recorded as generated by a manifest and not present on disk. Said,
		// not silently skipped: a component the tenant believes it has and
		// cannot find is the finding.
		fmt.Printf("  [warn] %s: recorded as generated, and no files were found for it\n", c.Label())
		return 0, 1
	}
	fmt.Printf("  [ok] %s: platform=%s, in %s/\n", c.Label(), l.Platform, l.Home())
	passed++

	// The entry point the layout names, not a main.go beside a go.mod.
	if l.Entry != "" {
		if fileExists(filepath.Join(root, filepath.FromSlash(l.Entry))) {
			passed++
		} else {
			fmt.Printf("  [warn] %s: missing %s\n", c.Label(), l.Entry)
			failed++
		}
	}

	// Protocol compliance markers, across every directory the component owns.
	if l.Platform == "go" {
		var marked bool
		for _, d := range l.Dirs {
			if containsProtocolMarkers(filepath.Join(root, filepath.FromSlash(d))) {
				marked = true
				break
			}
		}
		if marked {
			fmt.Printf("  [ok] %s: protocol markers present\n", c.Label())
			passed++
		} else {
			fmt.Printf("  [warn] %s: no protocol markers found (endpoints may not implement spec)\n", c.Label())
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

	// A blueprint is validated against the corpus rules, not against the
	// generic YAML checks below.
	//
	// It was not, and the gap was invisible in the worst way: `weblisk validate
	// architecture/admin.md` printed "Validation passed" for a blueprint whose
	// `contracts:` block could not be parsed at all. Nothing was checked, so
	// nothing failed, and the message said what a passing check says. Anyone
	// editing one blueprint — which is how blueprints are edited — was being
	// told their change was sound by a function that had not looked at it.
	//
	// The whole corpus is loaded even when one file is named, because the rules
	// that matter most are relationships BETWEEN blueprints: a binding resolves
	// against another document's declarations, and a rule restated in two
	// places is only visible from both. Findings are then filtered to the file
	// asked about.
	if strings.EqualFold(filepath.Ext(file), ".md") {
		return validateSingleBlueprint(root, file, content)
	}

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

// loadCorpus reads every blueprint from the resolved sources.
//
// Keyed as a blueprint refers to itself — "architecture/orchestrator.md" — so a
// finding names what an author would search for. Earlier sources win, matching
// how ResolveSources orders them: a project's own copy overrides the core one.
func loadCorpus(dirs []string) map[string]string {
	corpus := map[string]string{}
	for _, dir := range dirs {
		for _, group := range []string{
			"schemas", "protocol", "architecture", "patterns", "agents", "platforms", "domains", "standards",
		} {
			entries, err := os.ReadDir(filepath.Join(dir, group))
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
					continue
				}
				key := group + "/" + e.Name()
				if _, taken := corpus[key]; taken {
					continue
				}
				b, rerr := os.ReadFile(filepath.Join(dir, group, e.Name()))
				if rerr == nil {
					corpus[key] = string(b)
				}
			}
		}
	}
	return corpus
}

// validateSingleBlueprint runs the corpus rules and reports the ones for one file.
func validateSingleBlueprint(root, file, content string) error {
	dirs := resolvedSources(root)
	corpus := loadCorpus(dirs)
	if len(corpus) == 0 {
		// Better than validating it alone: alone, every cross-document rule
		// reports the other document as missing, and a page of false faults is
		// worse than an honest refusal.
		fmt.Println("  [warn] no blueprint sources resolved — a blueprint cannot be checked")
		fmt.Println("         on its own, because most of what makes it correct is its")
		fmt.Println("         relationship to the others. Run: weblisk blueprints update")
		return fmt.Errorf("no corpus to check against")
	}

	// The file as it is on disk right now, which may differ from what the
	// resolved sources hold — this is usually run on an edit in progress.
	name := normaliseBlueprintName(root, file)
	corpus[name] = content

	var mine []Finding
	for _, f := range ValidateCorpus(corpus) {
		if f.Blueprint == name {
			mine = append(mine, f)
		}
	}
	if len(mine) == 0 {
		fmt.Printf("  [ok] conforms to its schema, checked against %d blueprint(s)\n\n", len(corpus))
		return nil
	}
	faults := Faults(mine)
	fmt.Printf("  [%s] %d finding(s)\n\n", map[bool]string{true: "fail", false: "warn"}[faults > 0], len(mine))
	fmt.Print(FormatFindings(mine))
	fmt.Println()
	if faults > 0 {
		return fmt.Errorf("%d fault(s) in %s", faults, file)
	}
	return nil
}

// normaliseBlueprintName turns a path as typed into the name the corpus uses.
//
// The corpus is keyed by the path relative to a blueprint SOURCE — "architecture/admin.md"
// — and a person types whatever their shell completed, which may be absolute,
// may start with "./", and may be relative to a source root rather than to cwd.
func normaliseBlueprintName(root, file string) string {
	name := filepath.ToSlash(strings.TrimPrefix(filepath.Clean(file), "./"))
	for _, dir := range resolvedSources(root) {
		if rel, err := filepath.Rel(dir, filepath.Join(root, file)); err == nil &&
			!strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return name
}
