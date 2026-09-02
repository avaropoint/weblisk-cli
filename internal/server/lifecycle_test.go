package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotStartedAndDiedAreDifferentFacts(t *testing.T) {
	// An operator needs the difference. "Never started" is a thing to do;
	// "recorded as running and the process is gone" is a crash with a log that
	// explains it, and reporting both as "not running" loses the second.
	root := t.TempDir()

	st := StatusOf(root, "orchestrator")
	if st.Recorded || st.Running || st.Stale {
		t.Errorf("a tenant that never started: %+v", st)
	}
	if !strings.Contains(st.Note, "not started") {
		t.Errorf("note does not say what to do: %q", st.Note)
	}

	// Record a run whose process cannot exist.
	if err := writeRunState(root, RunState{
		Component: "orchestrator", PID: 0x7FFFFFF0,
		Address: "http://localhost:9800", LogPath: "/tmp/x.log",
		Started: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	st = StatusOf(root, "orchestrator")
	if !st.Recorded {
		t.Error("a recorded run was not reported as recorded")
	}
	if st.Running {
		t.Error("a dead process was reported as running")
	}
	if !st.Stale {
		t.Error("a recorded run whose process is gone must be reported stale, not merely stopped")
	}
	if !strings.Contains(st.Note, "exited without being stopped") || !strings.Contains(st.Note, "x.log") {
		t.Errorf("the note does not point at the diagnosis: %q", st.Note)
	}
}

func TestRunStateIsNotWorldReadable(t *testing.T) {
	// It names a process an operator may signal, in a directory that also holds
	// keys/orchestrator.key, grants/ and bootstrap.
	root := t.TempDir()
	if err := writeRunState(root, RunState{Component: "orchestrator", PID: 1234}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(runStatePath(root, "orchestrator"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("run state is %o, want 0600", perm)
	}
	di, err := os.Stat(runDir(root))
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("run directory is %o, want 0700", perm)
	}
}

func TestStatusJSONIsTheContract(t *testing.T) {
	// Studio programs against this and never opens .weblisk/. The field names
	// are therefore an interface, and renaming one is a breaking change.
	root := t.TempDir()
	started := time.Now().Add(-90 * time.Second)
	if err := writeRunState(root, RunState{
		Component: "orchestrator", PID: os.Getpid(),
		Address: "http://localhost:9800", LogPath: "/tmp/o.log", Started: started,
	}); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(StatusOf(root, "orchestrator"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"component", "recorded", "running", "pid", "address", "log_path", "uptime_seconds"} {
		if _, ok := m[field]; !ok {
			t.Errorf("status JSON is missing %q — Studio reads this", field)
		}
	}
	if m["running"] != true {
		t.Error("a live pid was not reported running")
	}
	if u, _ := m["uptime_seconds"].(float64); u < 89 {
		t.Errorf("uptime_seconds = %v, want ~90", u)
	}
}

func TestTailReadsTheEndWithoutReadingTheWhole(t *testing.T) {
	// A hub logging every request with spans produces a large file. A viewer
	// that reads it all to show the last screen is a memory fault waiting for a
	// long-running tenant.
	dir := t.TempDir()
	path := filepath.Join(dir, "big.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50000; i++ {
		if _, err := f.WriteString(strings.Repeat("x", 120) + " line\n"); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	r, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	lines, err := lastN(r, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 10 {
		t.Fatalf("got %d lines, want 10", len(lines))
	}
	for _, l := range lines {
		if !strings.HasSuffix(l, " line") {
			t.Errorf("a line was truncated mid-content: %q", l[max(0, len(l)-20):])
		}
	}
	// Asking for more than exists returns what exists, not an error.
	short, err := lastN(r, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(short) != 50000 {
		t.Errorf("got %d lines asking for more than the file holds", len(short))
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
