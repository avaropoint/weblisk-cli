package dispatch

// skills.go — teaching the local agent about the tenant it is standing in.
//
// # Why this exists
//
// A generated tenant is a repository full of code whose rules live somewhere
// else: in the blueprints it was derived from, in the platform blueprint's
// routing constraints, in a verification checklist whose identifiers must be
// backticked. An agent opening that repository afterwards knows none of it, and
// will confidently make edits that are locally reasonable and globally wrong —
// inventing a synonym for a declared name, restating a rule another blueprint
// owns, registering a route in the shape that panics at startup.
//
// Claude Code reads `.claude/skills/<name>/SKILL.md` from the repository it is
// working in. So the knowledge travels with the tenant.
//
// # Why only for Claude Code
//
// Because the format is Claude Code's. Writing these files for a tenant
// generated with Ollama would leave dead files in somebody's repository that
// nothing reads — the inert-code fault, in a customer's directory. When another
// tool grows an equivalent, it gets its own writer here.
//
// # Content, not code
//
// These are Markdown files under skills/, embedded rather than built from
// string literals, so they can be read and edited as documents. They are
// COMPLIMENTARY CONTENT in the project's sense: given freely, overridable, and
// never rewritten once a tenant has changed them.

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

// skillsForPlatform names which skills a tenant gets.
//
// Two are universal — reading a blueprint and verifying a hub are the same job
// whatever it is written in. The platform one is only installed when it matches,
// because Go's routing panics are not a Rust tenant's problem and a skill full
// of irrelevant rules is worse than no skill.
func skillsForPlatform(platform string) []string {
	skills := []string{"reading-blueprints", "hub-conformance"}
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "go", "":
		skills = append(skills, "go-hub")
	}
	return skills
}

// InstallSkills writes the agent skills into a generated tenant.
//
// Reports what it wrote. Errors are returned rather than logged-and-ignored: a
// tenant that was supposed to carry its own rules and silently did not is the
// quiet half of this whole class of fault.
func InstallSkills(root, provider, platform string) ([]string, error) {
	if normaliseKind(ProviderKind(provider)) != ProviderClaudeCode {
		return nil, nil
	}
	var written []string
	for _, name := range skillsForPlatform(platform) {
		src := "skills/" + name + "/SKILL.md"
		body, err := skillFiles.ReadFile(src)
		if err != nil {
			return written, fmt.Errorf("skill %s is missing from this build: %w", name, err)
		}
		dir := filepath.Join(root, ".claude", "skills", name)
		dest := filepath.Join(dir, "SKILL.md")

		// Never overwrite a tenant's own edit. These are given freely and are
		// theirs once written; silently replacing an operator's changes on the
		// next `--resume` would make them unmaintainable.
		if existing, rerr := os.ReadFile(dest); rerr == nil {
			if string(existing) != string(body) {
				continue
			}
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return written, fmt.Errorf("creating %s: %w", dir, err)
		}
		if err := os.WriteFile(dest, body, 0o644); err != nil {
			return written, fmt.Errorf("writing %s: %w", dest, err)
		}
		written = append(written, filepath.Join(".claude", "skills", name, "SKILL.md"))
	}
	return written, nil
}

// SkillNames lists what would be installed, without writing anything.
func SkillNames(platform string) []string { return skillsForPlatform(platform) }

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
