package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGeneratedPathsCannotEscapeTheTarget is the reproduction of the fault this
// containment exists for: model output overwrote the instance's secrets store
// and wrote outside the project root entirely.
func TestGeneratedPathsCannotEscapeTheTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "server")
	secrets := filepath.Join(root, ".weblisk", "secrets")
	if err := os.MkdirAll(secrets, 0o755); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(secrets, "master.key")
	if err := os.WriteFile(keyPath, []byte("REAL KEY"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := writeGeneratedFiles(target, []GeneratedFile{
		{Path: "main.go", Content: "package main"},
		{Path: "../.weblisk/secrets/master.key", Content: "OVERWRITTEN"},
	})
	if err == nil {
		t.Fatal("a traversing path was accepted")
	}

	got, _ := os.ReadFile(keyPath)
	if string(got) != "REAL KEY" {
		t.Errorf("the secrets store was modified: %q", string(got))
	}
	// Nothing partially applied: the legitimate file in the same batch must not
	// have been written either.
	if _, serr := os.Stat(filepath.Join(target, "main.go")); serr == nil {
		t.Error("a refused batch still wrote some of its files")
	}
}

func TestAbsolutePathsAreRefused(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside.txt")
	if _, err := writeGeneratedFiles(filepath.Join(root, "server"),
		[]GeneratedFile{{Path: outside, Content: "x"}}); err == nil {
		t.Fatal("an absolute path was accepted")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("an absolute path was written")
	}
}

func TestProtectedLocationsAreRefusedEvenInsideTheTarget(t *testing.T) {
	// Containment alone would allow these, because they are nominally within the
	// target. A write to .git/hooks is code execution on the next commit.
	root := t.TempDir()
	target := filepath.Join(root, "server")
	for _, p := range []string{
		".git/hooks/pre-commit",
		"secrets/master.key",
		"keys/operator.key",
		".env",
		".weblisk/token",
	} {
		if _, err := writeGeneratedFiles(target, []GeneratedFile{{Path: p, Content: "x"}}); err == nil {
			t.Errorf("protected path %q was accepted", p)
		}
	}
}

func TestOrdinaryGenerationStillWrites(t *testing.T) {
	// A containment check that refuses everything is indistinguishable from a
	// broken generator.
	root := t.TempDir()
	target := filepath.Join(root, "server")
	n, err := writeGeneratedFiles(target, []GeneratedFile{
		{Path: "main.go", Content: "package main"},
		{Path: "internal/orchestrator/register.go", Content: "package orchestrator"},
		{Path: "go.mod", Content: "module hub"},
	})
	if err != nil {
		t.Fatalf("ordinary generation was refused: %v", err)
	}
	if n != 3 {
		t.Errorf("wrote %d files, want 3", n)
	}
	if b, rerr := os.ReadFile(filepath.Join(target, "internal", "orchestrator", "register.go")); rerr != nil || len(b) == 0 {
		t.Errorf("nested file not written: %v", rerr)
	}
}

func TestASiblingSharingAPrefixIsNotInside(t *testing.T) {
	// The classic way a containment check passes and does nothing: a raw string
	// prefix test, under which "server-old" is inside "server".
	root := t.TempDir()
	target := filepath.Join(root, "server")
	if _, err := safeGeneratedPath(target, "../server-old/x.go"); err == nil {
		t.Error("a sibling sharing a name prefix was treated as inside the target")
	}
}

func TestSymlinkedDirectoryCannotLeadOut(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "server")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(target, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := writeGeneratedFiles(target, []GeneratedFile{
		{Path: "escape/loot.txt", Content: "x"},
	}); err == nil {
		t.Error("a symlinked directory led outside the target and was accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "loot.txt")); err == nil {
		t.Error("wrote through a symlink to outside the target")
	}
}

// Each containment layer is pinned on its own. The traversal rejection and the
// resolved-path check are deliberately redundant — which means neither is
// exercised by a test that only goes through writeGeneratedFiles, because the
// other one catches it. Removing either alone would then look safe.

func TestTraversalIsRejectedBeforeCleaning(t *testing.T) {
	target := t.TempDir()
	for _, p := range []string{
		"../x.go",
		"a/../../x.go",
		"./../../x.go",
		"a/b/../../../x.go",
	} {
		if _, err := safeGeneratedPath(target, p); err == nil {
			t.Errorf("traversing path %q was accepted", p)
		}
	}
}

func TestResolvedPathMustLandInsideTheTarget(t *testing.T) {
	// Independent of the ".." scan: a path with no ".." segment that still fails
	// containment must be caught by the resolved-path check.
	target := t.TempDir()
	if _, err := safeGeneratedPath(target, "ok/file.go"); err != nil {
		t.Fatalf("an ordinary nested path was refused: %v", err)
	}
	if _, err := safeGeneratedPath(target, "/etc/passwd"); err == nil {
		t.Error("an absolute path passed containment")
	}
}

func TestEmptyPathIsRefused(t *testing.T) {
	target := t.TempDir()
	for _, p := range []string{"", "   ", "\t"} {
		if _, err := safeGeneratedPath(target, p); err == nil {
			t.Errorf("empty path %q was accepted", p)
		}
	}
}
