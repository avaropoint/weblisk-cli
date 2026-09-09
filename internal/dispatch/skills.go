package dispatch

// skills.go — teaching a coding agent how to drive this tenant.
//
// # Why this exists
//
// Blueprints say what to build. The CLI verbs generate, start and verify it.
// An agent opening the repository knows neither, and will invent a synonym
// for a declared name or write an orchestrator by hand instead of running
// `weblisk tenant create`.
//
// Skills are the thin guide: how to *read* a blueprint, and which verb to
// run. They do not restate protocol tables. The corpus lives in
// weblisk-blueprints/skills/; this package resolves that corpus the same
// way it resolves blueprints, with an embed as fallback so a machine with
// no cache still installs something.
//
// # One corpus, not one skill per model
//
// Content is the same for every backend. The only per-tool difference is
// the install path: `.agents/skills/` always (vendor-neutral; Grok scans
// it), plus `.claude/skills/` or `.grok/skills/` when that coding-agent
// CLI generated the tenant.
//
// # Complimentary, overridable
//
// Given freely. Never rewritten once a tenant has changed them.

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed skills/*/SKILL.md
var skillFiles embed.FS

// verbSkills is the catalog keyed by the CLI's main verb.
//
// `blueprints` is on every generate verb because every generate reads a
// blueprint. The platform skill is added separately — it is not a verb.
var verbSkills = map[string][]string{
	"tenant":   {"tenants", "blueprints", "hubs", "operators"},
	"server":   {"hubs", "blueprints"},
	"agent":    {"agents", "blueprints"},
	"domain":   {"domains", "blueprints"},
	"gateway":  {"gateways", "blueprints"},
	"operator": {"operators", "blueprints"},
}

// SkillsFor names the skills a verb installs, including the platform skill
// when this tenant is being generated for a language that has one.
func SkillsFor(verb, platform string) []string {
	names := append([]string(nil), verbSkills[strings.ToLower(strings.TrimSpace(verb))]...)
	if len(names) == 0 {
		names = []string{"blueprints"}
	}
	if p := platformSkill(platform); p != "" {
		names = append(names, p)
	}
	return names
}

func platformSkill(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "go", "":
		return "go"
	}
	return ""
}

func skillDests(provider string) []string {
	dests := []string{".agents"}
	switch normaliseKind(ProviderKind(provider)) {
	case ProviderClaudeCode:
		dests = append(dests, ".claude")
	case ProviderGrok:
		dests = append(dests, ".grok")
	}
	return dests
}

// loadSkill reads one skill, first-hit across blueprint sources, then the
// embed. A local `blueprints/skills/<name>/SKILL.md` therefore overrides the
// published copy, the same way a local blueprint overrides the cache.
func loadSkill(root, name string) ([]byte, error) {
	rel := filepath.Join("skills", name, "SKILL.md")
	for _, dir := range resolvedSources(root) {
		body, err := os.ReadFile(filepath.Join(dir, rel))
		if err == nil {
			return body, nil
		}
	}
	body, err := skillFiles.ReadFile("skills/" + name + "/SKILL.md")
	if err != nil {
		return nil, fmt.Errorf("skill %s is missing from this build and from the blueprint sources", name)
	}
	return body, nil
}

// InstallSkills writes the skills for a CLI verb into a tenant.
//
// `verb` is the main command (`tenant`, `server`, `agent`, …). Unknown verbs
// still get `blueprints`, so a new command cannot silently install nothing.
func InstallSkills(root, provider, platform, verb string) ([]string, error) {
	var written []string
	for _, name := range SkillsFor(verb, platform) {
		body, err := loadSkill(root, name)
		if err != nil {
			return written, err
		}
		for _, destRoot := range skillDests(provider) {
			dir := filepath.Join(root, destRoot, "skills", name)
			dest := filepath.Join(dir, "SKILL.md")
			if _, rerr := os.ReadFile(dest); rerr == nil {
				continue
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return written, fmt.Errorf("creating %s: %w", dir, err)
			}
			if err := os.WriteFile(dest, body, 0o644); err != nil {
				return written, fmt.Errorf("writing %s: %w", dest, err)
			}
			written = append(written, filepath.Join(destRoot, "skills", name, "SKILL.md"))
		}
	}
	return written, nil
}

// InstallAndNoteSkills installs and prints. Used by the generate commands
// that report to a terminal rather than a progress channel.
func InstallAndNoteSkills(root, platform, verb string) {
	provider := ""
	if k, _, ok := SelectedProvider(); ok {
		provider = string(k)
	} else {
		provider = os.Getenv("WL_AI_PROVIDER")
	}
	files, err := InstallSkills(root, provider, platform, verb)
	if err != nil {
		fmt.Printf("  [warn] agent skills were not installed: %v\n", err)
		return
	}
	if len(files) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("  Agent skills installed — this tenant now explains itself:")
	for _, f := range files {
		fmt.Printf("    %s\n", f)
	}
}

// SkillNames lists what a verb would install, without writing anything.
func SkillNames(verb, platform string) []string { return SkillsFor(verb, platform) }

// embeddedSkillCount is used by the tests to prove the embed actually carries
// files — an empty embed.FS compiles perfectly and installs nothing.
func embeddedSkillCount() int {
	n := 0
	_ = fs.WalkDir(skillFiles, ".", func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}
