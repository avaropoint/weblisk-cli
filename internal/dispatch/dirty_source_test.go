package dispatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo makes a throwaway repo with one commit.
func dirtyTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "T"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("add", "-A")
	run("commit", "-q", "-m", "one")
	return dir
}

// capture runs f with stderr redirected and returns what was written.
func capture(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	f()
	w.Close()
	os.Stderr = old
	return <-done
}

// A clone takes HEAD. Uncommitted work in the source is simply not in the
// build, and the announcement reads as "your local blueprints, current" because
// the CACHE is clean — it is clean, it was made from the last commit.
//
// This cost a 30-minute generation run: WL_BLUEPRINT_SOURCES pointed at a
// working tree holding an uncommitted specification change, the build cloned
// it, and the change reached nothing. Zero occurrences in the cache, no warning
// anywhere, and a revision string that was accurate and misleading at once.
func TestADirtySourceIsAnnounced(t *testing.T) {
	dir := dirtyTestRepo(t)
	if got := capture(t, func() { warnIfSourceIsDirty(dir) }); got != "" {
		t.Fatalf("a clean source warned: %s", got)
	}

	// An edit that is not committed.
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# a\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := capture(t, func() { warnIfSourceIsDirty(dir) })
	if got == "" {
		t.Fatal("a source with uncommitted changes did not warn — this is the whole fault")
	}
	if !strings.Contains(got, "NOT in this build") {
		t.Errorf("the warning does not say the changes are absent: %s", got)
	}
	if !strings.Contains(got, dir) {
		t.Errorf("the warning does not name which source: %s", got)
	}
}

// An untracked file is a blueprint too. It has not been added, so a clone
// certainly will not carry it.
func TestAnUntrackedBlueprintCounts(t *testing.T) {
	dir := dirtyTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "new.md"), []byte("# new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := capture(t, func() { warnIfSourceIsDirty(dir) }); got == "" {
		t.Fatal("an untracked blueprint did not warn")
	}
}

// A remote URL has no working tree, and probing the filesystem for one would
// warn about whatever happens to sit at that path.
func TestARemoteSourceIsNotProbed(t *testing.T) {
	for _, url := range []string{
		"https://github.com/avaropoint/weblisk-blueprints",
		"git@github.com:avaropoint/weblisk-blueprints.git",
	} {
		if got := capture(t, func() { warnIfSourceIsDirty(url) }); got != "" {
			t.Errorf("%s warned: %s", url, got)
		}
	}
}

// The fetch marker every cached source carries must not count. A warning that
// is always showing is not a warning — this is the same rule revisionOf holds.
func TestTheFetchMarkerDoesNotCount(t *testing.T) {
	dir := dirtyTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".weblisk-fetched"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := capture(t, func() { warnIfSourceIsDirty(dir) }); got != "" {
		t.Errorf("our own fetch marker triggered the warning: %s", got)
	}
}
