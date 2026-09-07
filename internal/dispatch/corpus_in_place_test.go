package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

// corpusLike builds a directory with the shape of a blueprint corpus.
func corpusLike(t *testing.T, families ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "schemas"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "schemas", "common.md"), []byte("# common\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if err := os.MkdirAll(filepath.Join(dir, f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A blueprint command run INSIDE a blueprint corpus must read that corpus.
//
// It read the cache instead — a clone of the remote — so `weblisk validate` in
// weblisk-blueprints reported on origin/main while an edited working tree sat in
// front of it. An edit adding a contract binding showed 1 occurrence in the
// tree, 0 in what validate examined, and an identical finding count before and
// after. The command whose job is checking your work was checking a copy of
// somebody else's.
func TestACorpusInPlaceIsDetected(t *testing.T) {
	for _, family := range []string{"architecture", "protocol", "patterns", "agents"} {
		if !isBlueprintCorpus(corpusLike(t, family)) {
			t.Errorf("a corpus with schemas/common.md and %s/ was not detected", family)
		}
	}
}

// And it must be FIRST, so the files in front of you outrank any cache.
func TestTheCorpusInPlaceOutranksTheCache(t *testing.T) {
	dir := corpusLike(t, "architecture")
	srcs := ResolveSources(dir)
	if len(srcs) == 0 {
		t.Fatal("no sources resolved at all")
	}
	if srcs[0].Dir != dir {
		t.Errorf("first source is %q; the corpus you are standing in must win, not %q", srcs[0].Dir, dir)
	}
	if srcs[0].Kind != "project" {
		t.Errorf("kind is %q, want project — it is a working directory, not a copy", srcs[0].Kind)
	}
}

// The detector must be specific. A lone architecture/ directory is a shape
// plenty of unrelated repositories have, and treating one as a corpus would
// make a build read a stranger's markdown as its specification.
func TestALookalikeIsNotACorpus(t *testing.T) {
	// Families but no schemas/common.md.
	bare := t.TempDir()
	for _, f := range []string{"architecture", "protocol", "patterns"} {
		if err := os.MkdirAll(filepath.Join(bare, f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if isBlueprintCorpus(bare) {
		t.Error("a directory with family folders but no schemas/common.md was treated as a corpus")
	}

	// schemas/common.md but no families — a project that documents its schemas.
	onlySchemas := corpusLike(t)
	if isBlueprintCorpus(onlySchemas) {
		t.Error("schemas/common.md alone was treated as a corpus")
	}

	// common.md as a DIRECTORY, not a file.
	odd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(odd, "schemas", "common.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(odd, "architecture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if isBlueprintCorpus(odd) {
		t.Error("a DIRECTORY named common.md satisfied the detector")
	}

	// Empty, and an empty string.
	if isBlueprintCorpus(t.TempDir()) {
		t.Error("an empty directory was treated as a corpus")
	}
	if isBlueprintCorpus("") {
		t.Error("the empty path was treated as a corpus")
	}
}

// A corpus nested at blueprints/ must still work — that is the ordinary case
// for a tenant that carries its own copy, and it must not be shadowed.
func TestANestedCorpusStillResolves(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "blueprints")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	srcs := ResolveSources(root)
	var found bool
	for _, s := range srcs {
		if s.Dir == nested {
			found = true
		}
	}
	if !found {
		t.Errorf("the nested blueprints/ directory was not resolved; got %d source(s)", len(srcs))
	}
}
