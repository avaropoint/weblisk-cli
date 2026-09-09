package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheSkillsAreActuallyEmbedded(t *testing.T) {
	if n := embeddedSkillCount(); n < 8 {
		t.Fatalf("%d skill file(s) embedded; the //go:embed pattern is not matching", n)
	}
	for _, name := range SkillsFor("tenant", "go") {
		body, err := skillFiles.ReadFile("skills/" + name + "/SKILL.md")
		if err != nil {
			t.Errorf("skill %s is named but not embedded: %v", name, err)
			continue
		}
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

func TestAVerbSelectsItsSkills(t *testing.T) {
	got := SkillsFor("tenant", "go")
	for _, want := range []string{"tenants", "blueprints", "hubs", "operators", "go"} {
		if !hasName(got, want) {
			t.Errorf("tenant/go missing %s: %v", want, got)
		}
	}
	got = SkillsFor("agent", "rust")
	if hasName(got, "go") {
		t.Errorf("a rust agent was given the Go skill: %v", got)
	}
	if !hasName(got, "agents") || !hasName(got, "blueprints") {
		t.Errorf("agent/rust = %v", got)
	}
	if hasName(SkillsFor("server", "go"), "tenants") {
		t.Error("server init installed the tenant skill; that verb is tenant create")
	}
}

func TestEveryProviderGetsTheVendorNeutralPath(t *testing.T) {
	for _, provider := range []string{"ollama", "anthropic", "openai", "xai", ""} {
		root := t.TempDir()
		written, err := InstallSkills(root, provider, "go", "server")
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if len(written) == 0 {
			t.Errorf("%s: wrote nothing", provider)
		}
		if _, err := os.Stat(filepath.Join(root, ".agents", "skills", "hubs", "SKILL.md")); err != nil {
			t.Errorf("%s: .agents/skills/hubs missing: %v", provider, err)
		}
		if _, err := os.Stat(filepath.Join(root, ".claude")); err == nil {
			t.Errorf("%s: a .claude directory was created", provider)
		}
		if _, err := os.Stat(filepath.Join(root, ".grok")); err == nil {
			t.Errorf("%s: a .grok directory was created", provider)
		}
	}
}

func TestTheGeneratingToolGetsItsNativePath(t *testing.T) {
	root := t.TempDir()
	written, err := InstallSkills(root, "claude-code", "go", "server")
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(written, ".claude/skills/hubs/SKILL.md") {
		t.Errorf("claude-code did not write its native path: %v", written)
	}
	if !containsPath(written, ".agents/skills/hubs/SKILL.md") {
		t.Errorf("claude-code did not write .agents: %v", written)
	}

	root = t.TempDir()
	written, err = InstallSkills(root, "grok", "go", "tenant")
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(written, ".grok/skills/tenants/SKILL.md") {
		t.Errorf("grok did not write its native path: %v", written)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude")); err == nil {
		t.Error("grok wrote Claude Code's skill path")
	}
}

func TestThePlatformSkillFollowsThePlatform(t *testing.T) {
	root := t.TempDir()
	written, err := InstallSkills(root, "claude-code", "rust", "server")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range written {
		if strings.Contains(f, "/go/") {
			t.Errorf("a rust tenant was given the Go skill: %v", written)
		}
	}
}

func TestATenantsOwnEditIsNeverOverwritten(t *testing.T) {
	root := t.TempDir()
	if _, err := InstallSkills(root, "claude-code", "go", "server"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, ".claude", "skills", "hubs", "SKILL.md")
	const mine = "---\nname: hubs\ndescription: mine now\n---\n\nOur own rules.\n"
	if err := os.WriteFile(dest, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	written, err := InstallSkills(root, "claude-code", "go", "server")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range written {
		if strings.Contains(f, ".claude/skills/hubs") {
			t.Errorf("re-installed %v over an existing tenant edit", written)
		}
	}
	after, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != mine {
		t.Error("the tenant's own edit was overwritten")
	}
}

func TestALocalBlueprintSkillOutranksTheEmbed(t *testing.T) {
	root := t.TempDir()
	custom := filepath.Join(root, "blueprints", "skills", "hubs")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	const body = "---\nname: hubs\ndescription: local override\n---\n\nLOCAL.\n"
	if err := os.WriteFile(filepath.Join(custom, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallSkills(root, "claude-code", "go", "server"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, ".claude", "skills", "hubs", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("installed %q, want the local override", got)
	}
}

func hasName(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func containsPath(list []string, want string) bool {
	for _, s := range list {
		if filepath.ToSlash(s) == want {
			return true
		}
	}
	return false
}
