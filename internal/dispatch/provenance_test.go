package dispatch

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A build must say which blueprints it is about to read.
//
// On 2026-09-06 the absence of this produced the quietest failure this project
// has had: weblisk-blueprints was 40 commits ahead of its remote with 40 files
// uncommitted, the shared cache faithfully refreshed itself FROM that remote,
// and every hub generated outside a local checkout was built and
// conformance-repaired against the superseded specification. 88 files differed.
// Nothing was broken. Nothing said anything.
//
// The provenance was already RECORDED (SetProvenance) — it was never SAID.
// Recorded answers "what was this built from" afterwards; said answers "am I
// about to build from the right thing".
func TestASourceIsDescribedWellEnoughToTellTwoCopiesApart(t *testing.T) {
	s := Source{
		Dir:  "/home/x/.weblisk/blueprints/weblisk-blueprints-c1ce483f",
		Kind: "core", Origin: "https://github.com/avaropoint/weblisk-blueprints",
		Revision: "ce33c47", Fetched: time.Now().Add(-6 * 24 * time.Hour),
	}
	d := s.Describe()
	// The revision is the whole point: two copies of the same blueprints are
	// told apart by it and by nothing else in the path.
	if !strings.Contains(d, "ce33c47") {
		t.Errorf("Describe omits the revision, so two copies are indistinguishable: %q", d)
	}
	if !strings.Contains(d, "core") {
		t.Errorf("Describe omits which kind of source this is: %q", d)
	}
	if !strings.Contains(d, s.Dir) {
		t.Errorf("Describe omits where it is: %q", d)
	}
}

// Reading only the shared cache is the case worth naming.
//
// A cache is not stale by being a cache — it is the normal source. What is worth
// saying is that a local checkout is NOT being read, because that is the state
// in which somebody's in-flight blueprint work cannot reach the build.
func TestAnnouncingNamesTheCacheOnlyCase(t *testing.T) {
	out := captureStdout(t, func() {
		AnnounceSources([]Source{{Dir: "/cache", Kind: "core", Revision: "ce33c47"}})
	})
	for _, want := range []string{"ce33c47", "blueprints/", "WL_BLUEPRINT_SOURCES"} {
		if !strings.Contains(out, want) {
			t.Errorf("the cache-only note omits %q:\n%s", want, out)
		}
	}

	// With a project checkout present there is nothing to warn about — the
	// operator's own blueprints are being read, which is what they wanted.
	out = captureStdout(t, func() {
		AnnounceSources([]Source{
			{Dir: "/work/blueprints", Kind: "project", Revision: "6a6d832"},
			{Dir: "/cache", Kind: "core", Revision: "ce33c47"},
		})
	})
	if strings.Contains(out, "WL_BLUEPRINT_SOURCES") {
		t.Errorf("warned about the cache while a project checkout was in use:\n%s", out)
	}
	if !strings.Contains(out, "6a6d832") || !strings.Contains(out, "ce33c47") {
		t.Errorf("both sources must be named, in order:\n%s", out)
	}
	// Order matters: first hit wins, so the first line is what the build reads.
	if strings.Index(out, "6a6d832") > strings.Index(out, "ce33c47") {
		t.Errorf("sources are announced out of resolution order:\n%s", out)
	}
}

// Nothing to announce announces nothing, rather than an empty heading.
func TestAnnouncingNothingIsSilent(t *testing.T) {
	if out := captureStdout(t, func() { AnnounceSources(nil) }); out != "" {
		t.Errorf("printed %q for no sources", out)
	}
}

// captureStdout runs fn and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				b.Write(buf[:n])
			}
			if rerr != nil {
				break
			}
		}
		done <- b.String()
	}()
	fn()
	w.Close()
	os.Stdout = saved
	return <-done
}
