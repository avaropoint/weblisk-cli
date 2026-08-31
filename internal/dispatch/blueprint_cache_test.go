package dispatch

// A cache that cannot go stale silently, and a resolution that cannot disagree
// with itself.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitRepo makes a real one-commit repository, so refresh() is tested against git
// rather than against a mock that agrees with me.
func gitRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "-q", "-b", "main")
	writeAll(t, dir, files)
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "one")
}

// gitRun runs git isolated from the developer's own configuration.
//
// Without the isolation these tests inherit commit.gpgsign from ~/.gitconfig and
// hang on a signing prompt — a fixture failing for a reason that has nothing to
// do with what it measures.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func writeAll(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStaleCacheIsRefreshed(t *testing.T) {
	// The fault this is here for: ensureCloned's freshness test was "is the
	// directory non-empty", so a May clone answered August's questions and the
	// pipeline generated against a specification nobody had written.
	base := t.TempDir()
	upstream := filepath.Join(base, "upstream")
	gitRepo(t, upstream, map[string]string{"protocol/types.md": "# v1\n"})

	cacheDir := filepath.Join(base, "cache")
	if err := clone(upstream, cacheDir); err != nil {
		t.Fatal(err)
	}
	first := revisionOf(cacheDir)

	// Upstream moves.
	writeAll(t, upstream, map[string]string{"protocol/types.md": "# v2\n"})
	gitRun(t, upstream, "add", "-A")
	gitRun(t, upstream, "commit", "-q", "-m", "two")

	// Inside the TTL, the cached copy is kept — a refresh per command would be a
	// network round trip on every build.
	if err := ensureFresh(upstream, cacheDir); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(cacheDir, "protocol/types.md")); string(got) != "# v1\n" {
		t.Fatalf("refreshed inside the TTL: %q", got)
	}

	// Age the stamp past the TTL.
	old := time.Now().Add(-blueprintTTL - time.Hour)
	if err := os.Chtimes(filepath.Join(cacheDir, fetchStamp), old, old); err != nil {
		t.Fatal(err)
	}
	if err := ensureFresh(upstream, cacheDir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(cacheDir, "protocol/types.md"))
	if string(got) != "# v2\n" {
		t.Fatalf("stale cache served past the TTL: %q", got)
	}
	if revisionOf(cacheDir) == first {
		t.Fatal("revision unchanged after refresh")
	}
	// And the stamp moved, so the next command does not fetch again.
	if time.Since(stampTime(cacheDir)) > time.Minute {
		t.Fatal("fetch stamp was not renewed")
	}
}

func TestOfflineKeepsWorkingFromTheCachedCopy(t *testing.T) {
	// A build on a plane must run. A stale specification is worse than a current
	// one and far better than none — provided the output says which it was.
	base := t.TempDir()
	upstream := filepath.Join(base, "upstream")
	gitRepo(t, upstream, map[string]string{"protocol/types.md": "# v1\n"})
	cacheDir := filepath.Join(base, "cache")
	if err := clone(upstream, cacheDir); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-blueprintTTL - time.Hour)
	if err := os.Chtimes(filepath.Join(cacheDir, fetchStamp), old, old); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WL_BLUEPRINT_OFFLINE", "1")
	if err := ensureFresh(upstream, cacheDir); err != nil {
		t.Fatalf("offline refresh must not fail the run: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(cacheDir, "protocol/types.md")); string(got) != "# v1\n" {
		t.Fatalf("offline resolution lost the cached copy: %q", got)
	}

	// An unreachable remote is the same situation without the switch: warn, keep
	// going, and do not retry on every command.
	os.Unsetenv("WL_BLUEPRINT_OFFLINE")
	os.RemoveAll(upstream)
	if err := ensureFresh(upstream, cacheDir); err != nil {
		t.Fatalf("unreachable remote must not fail the run: %v", err)
	}
	if time.Since(stampTime(cacheDir)) > time.Minute {
		t.Fatal("a failed fetch must still stamp, or every command retries")
	}
}

func TestProvenanceNamesTheCopyThatAnswered(t *testing.T) {
	// The measurement that disagreed with an edit I had just made came from
	// ~/.weblisk/blueprints, and nothing in the output could have told me. A
	// source must describe itself well enough to tell two copies apart.
	base := t.TempDir()
	dir := filepath.Join(base, "bp")
	gitRepo(t, dir, map[string]string{"protocol/types.md": "# v1\n"})
	touchStamp(dir)

	s := describeCache(dir, "core", "https://example.test/bp.git")
	d := s.Describe()
	if !strings.Contains(d, dir) || !strings.Contains(d, "core") {
		t.Fatalf("source does not name itself: %q", d)
	}
	if !strings.Contains(d, "@") {
		t.Fatalf("no revision in %q — two copies of a repo are indistinguishable", d)
	}
	if !strings.Contains(d, "fetched") {
		t.Fatalf("no fetch age in %q — staleness is invisible again", d)
	}
}

func TestProjectBlueprintsAreNeverRefreshedOverTheTop(t *testing.T) {
	// A local blueprints/ directory is a working tree, not a copy. Fetching over
	// it would be a cache eating the edits it exists to serve.
	root := t.TempDir()
	local := filepath.Join(root, "blueprints")
	gitRepo(t, local, map[string]string{"protocol/types.md": "# mine\n"})
	writeAll(t, root, map[string]string{"blueprints/protocol/types.md": "# uncommitted edit\n"})

	t.Setenv("WL_BLUEPRINT_OFFLINE", "1")
	srcs := ResolveSources(root)
	if len(srcs) == 0 || srcs[0].Kind != "project" {
		t.Fatalf("project blueprints are not first: %+v", srcs)
	}
	got, _ := os.ReadFile(filepath.Join(local, "protocol/types.md"))
	if string(got) != "# uncommitted edit\n" {
		t.Fatalf("resolution overwrote local work: %q", got)
	}
}
