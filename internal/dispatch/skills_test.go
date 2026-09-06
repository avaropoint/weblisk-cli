package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An empty embed.FS compiles perfectly and installs nothing. This is the check
// that the files are actually in the binary.
func TestTheSkillsAreActuallyEmbedded(t *testing.T) {
	if n := embeddedSkillCount(); n < 3 {
		t.Fatalf("%d skill file(s) embedded; the //go:embed pattern is not matching", n)
	}
	for _, name := range skillsForPlatform("go") {
		body, err := skillFiles.ReadFile("skills/" + name + "/SKILL.md")
		if err != nil {
			t.Errorf("skill %s is named but not embedded: %v", name, err)
			continue
		}
		// Claude Code reads the frontmatter to decide whether a skill is
		// relevant. A skill with no description is one that never loads.
		s := string(body)
		if !strings.HasPrefix(s, "---\n") {
			t.Errorf("%s has no frontmatter", name)
		}
		if !strings.Contains(s, "\nname: "+name+"\n") {
			t.Errorf("%s's frontmatter name does not match its directory", name)
		}
		if !strings.Contains(s, "\ndescription: ") {
			t.Errorf("%s has no description, so nothing will ever load it", name)
		}
	}
}

// Only for the tool whose format this is. Writing Claude Code skill files into
// a tenant generated with Ollama leaves files nothing reads, in somebody else's
// repository.
func TestSkillsAreOnlyWrittenForTheToolThatReadsThem(t *testing.T) {
	for _, provider := range []string{"ollama", "anthropic", "openai", "codex", ""} {
		root := t.TempDir()
		written, err := InstallSkills(root, provider, "go")
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if len(written) != 0 {
			t.Errorf("%s: wrote %v", provider, written)
		}
		if _, statErr := os.Stat(filepath.Join(root, ".claude")); statErr == nil {
			t.Errorf("%s: a .claude directory was created", provider)
		}
	}
	// And the aliases resolve, so `claude` is not a different answer from
	// `claude-code`.
	for _, provider := range []string{"claude-code", "claude", "claude-local"} {
		root := t.TempDir()
		written, err := InstallSkills(root, provider, "go")
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if len(written) != 3 {
			t.Errorf("%s: wrote %d skills, want 3", provider, len(written))
		}
	}
}

// The platform skill is only installed when it matches. A Rust tenant carrying
// Go's routing rules is worse than carrying none.
func TestThePlatformSkillFollowsThePlatform(t *testing.T) {
	root := t.TempDir()
	written, err := InstallSkills(root, "claude-code", "rust")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range written {
		if strings.Contains(f, "go-hub") {
			t.Errorf("a rust tenant was given the Go skill: %v", written)
		}
	}
	if len(written) != 2 {
		t.Errorf("wrote %v; want the two universal skills", written)
	}
}

// Given freely, and theirs once written.
func TestATenantsOwnEditIsNeverOverwritten(t *testing.T) {
	root := t.TempDir()
	if _, err := InstallSkills(root, "claude-code", "go"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, ".claude", "skills", "reading-blueprints", "SKILL.md")
	const mine = "---\nname: reading-blueprints\ndescription: mine now\n---\n\nOur own rules.\n"
	if err := os.WriteFile(dest, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}

	// A second run — which is what --resume does.
	written, err := InstallSkills(root, "claude-code", "go")
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Errorf("re-installed %v over an existing tenant", written)
	}
	after, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != mine {
		t.Error("the tenant's own edit was overwritten")
	}
}
