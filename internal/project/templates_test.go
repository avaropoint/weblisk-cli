package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// manifestV3 is the shape the templates repository actually publishes.
const manifestV3 = `{
  "version": "3.0.0",
  "templates": {
    "client/starter": {"description": "client", "path": "client/starter/", "default": true},
    "server/starter": {"description": "hub", "path": "server/starter/", "extends": "client/starter"}
  }
}`

func TestManifestReadsBothKeyNames(t *testing.T) {
	// The CLI read "scaffold"; the repository publishes "templates". The map
	// unmarshalled empty, resolution fell through to a literal "scaffold/default/"
	// path, and `weblisk new` failed against a source full of usable templates.
	var m Manifest
	if err := json.Unmarshal([]byte(manifestV3), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.sets()) != 2 {
		t.Fatalf("v3 manifest yielded %d templates, want 2", len(m.sets()))
	}

	var old Manifest
	if err := json.Unmarshal([]byte(`{"scaffold":{"a":{"path":"a/"}}}`), &old); err != nil {
		t.Fatal(err)
	}
	if len(old.sets()) != 1 {
		t.Error("a pinned or vendored older source stopped being readable")
	}
}

func TestDefaultIsAFlagNotAName(t *testing.T) {
	// No set is called "default" — client/starter carries the flag. Looking one
	// up by that literal name is what produced the reported failure.
	var m Manifest
	if err := json.Unmarshal([]byte(manifestV3), &m); err != nil {
		t.Fatal(err)
	}
	if got := m.defaultSet(); got != "client/starter" {
		t.Errorf("defaultSet() = %q, want client/starter", got)
	}
	if _, named := m.sets()["default"]; named {
		t.Error("fixture no longer represents the real manifest")
	}
}

func TestSubstitutionReplacesEveryPlaceholder(t *testing.T) {
	// A scaffolded project shipped with literal {{name}} and {{domain}} in 37
	// places, including .weblisk/config.yaml — the hub's own configuration.
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "config.yaml"),
		[]byte("hub:\n  name: \"{{name}}\"\n  host: {{domain}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"name": "avaropoint", "domain": "avaropoint.local"}
	if _, err := CopyScaffoldDirWith(src, dst, vars); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := "hub:\n  name: \"avaropoint\"\n  host: avaropoint.local\n"
	if string(got) != want {
		t.Errorf("substitution produced %q, want %q", got, want)
	}
}

func TestUnknownPlaceholdersSurviveIntact(t *testing.T) {
	// Blanking syntax this CLI does not recognise would corrupt content in a way
	// nobody could trace back to here. Leaving it visible is recoverable.
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "x.yaml"), []byte("a: {{name}}\nb: {{unknown_thing}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyScaffoldDirWith(src, dst, map[string]string{"name": "acme"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dst, "x.yaml"))
	if string(got) != "a: acme\nb: {{unknown_thing}}\n" {
		t.Errorf("unknown placeholder was not preserved: %q", got)
	}
}

func TestBinaryFilesAreNotSubstituted(t *testing.T) {
	// Substitution over a PNG or font would corrupt bytes that happen to spell a
	// placeholder.
	src, dst := t.TempDir(), t.TempDir()
	blob := []byte{0x89, 'P', 'N', 'G', 0x00, '{', '{', 'n', 'a', 'm', 'e', '}', '}', 0x00}
	if err := os.WriteFile(filepath.Join(src, "logo.png"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyScaffoldDirWith(src, dst, map[string]string{"name": "acme"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dst, "logo.png"))
	if string(got) != string(blob) {
		t.Error("a binary file was rewritten by substitution")
	}
}

func TestCopyWithNoVarsIsVerbatim(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("{{name}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyScaffoldDir(src, dst); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dst, "a.txt"))
	if string(got) != "{{name}}" {
		t.Errorf("copy without vars altered content: %q", got)
	}
}
