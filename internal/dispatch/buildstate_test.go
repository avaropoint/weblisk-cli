package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeState puts a state file in root, as the observer would.
func writeState(t *testing.T, root string, rec PersistedBuild) {
	t.Helper()
	dir := filepath.Join(root, cacheDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, buildStateFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The verdict comes from two observed facts and no clock of its own: whether
// the writing process is alive, and when the provider last spoke.
func TestTheVerdictIsDerivedFromLivenessAndSilence(t *testing.T) {
	now := time.Now()

	t.Run("running", func(t *testing.T) {
		root := t.TempDir()
		writeState(t, root, PersistedBuild{
			PID: os.Getpid(), Started: now.Add(-10 * time.Minute),
			LastEventAt: now.Add(-3 * time.Second), Updated: now,
			State: BuildState{Index: 7, Total: 29, File: "a.go", Status: "generating"},
		})
		v := ReadBuildVerdict(root)
		if v.Status != "running" {
			t.Fatalf("status = %q, want running: %s", v.Status, v.Why)
		}
		if !strings.Contains(v.Why, "alive") {
			t.Errorf("the why does not cite liveness: %s", v.Why)
		}
	})

	t.Run("stalled", func(t *testing.T) {
		root := t.TempDir()
		// Alive, but silent well past the allowance the abort itself uses.
		writeState(t, root, PersistedBuild{
			PID: os.Getpid(), Started: now.Add(-30 * time.Minute),
			LastEventAt: now.Add(-defaultIdleTimeout - time.Minute), Updated: now,
			State: BuildState{Index: 12, Total: 29, File: "b.go", Status: "generating"},
		})
		v := ReadBuildVerdict(root)
		if v.Status != "stalled" {
			t.Fatalf("status = %q, want stalled: %s", v.Status, v.Why)
		}
		// The SAME allowance as the abort. A status that called this stalled at
		// a different threshold than the one the build acts on would have two
		// meanings for one word.
		if !strings.Contains(v.Why, defaultIdleTimeout.String()) {
			t.Errorf("the why does not cite the allowance the build itself uses: %s", v.Why)
		}
	})

	t.Run("died", func(t *testing.T) {
		root := t.TempDir()
		// A pid nothing holds, and no recorded outcome.
		writeState(t, root, PersistedBuild{
			PID: 2147483646, Started: now.Add(-20 * time.Minute),
			LastEventAt: now.Add(-time.Minute), Updated: now.Add(-time.Minute),
			State: BuildState{Index: 18, Total: 29, File: "c.go", Status: "generating"},
		})
		v := ReadBuildVerdict(root)
		if v.Status != "died" {
			t.Fatalf("status = %q, want died: %s", v.Status, v.Why)
		}
		// It must say how far it got, because that is what resuming builds on.
		if !strings.Contains(v.Why, "18/29") {
			t.Errorf("the why does not say how far it got: %s", v.Why)
		}
		if !strings.Contains(v.Why, "--resume") {
			t.Errorf("the why does not say what to do: %s", v.Why)
		}
	})

	t.Run("none", func(t *testing.T) {
		v := ReadBuildVerdict(t.TempDir())
		if v.Status != "none" {
			t.Fatalf("status = %q for a directory with no build, want none", v.Status)
		}
	})
}

// A recorded outcome beats the process state, in both directions.
//
// This is the difference between a build that FAILED and one whose process
// merely vanished. Without a recorded terminal state both read as "died", and
// only one of them has a reason worth reading.
func TestARecordedOutcomeBeatsProcessState(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct{ terminal, detail string }{
		{"completed", ""},
		{"failed", "1 conformance test failed"},
	} {
		root := t.TempDir()
		// A dead pid: without the terminal state this would be "died".
		writeState(t, root, PersistedBuild{
			PID: 2147483646, Started: now.Add(-time.Hour), Updated: now,
			LastEventAt: now, Terminal: tc.terminal, Detail: tc.detail,
			State: BuildState{Status: tc.terminal},
		})
		v := ReadBuildVerdict(root)
		if v.Status != tc.terminal {
			t.Errorf("status = %q, want %q — a recorded outcome must win", v.Status, tc.terminal)
		}
		if tc.detail != "" && !strings.Contains(v.Why, tc.detail) {
			t.Errorf("the reason was lost: %s", v.Why)
		}
	}
}

// The start time belongs to the BUILD, not to the sample, so it must survive
// every later write. Otherwise "running for 40 minutes" resets to zero every
// couple of seconds.
func TestTheStartTimeSurvivesLaterWrites(t *testing.T) {
	root := t.TempDir()
	recordBuildState(root, BuildState{Index: 1, Total: 29, Status: "generating"}, "", "", true)

	first := ReadBuildVerdict(root)
	if first.Build == nil {
		t.Fatal("nothing was recorded")
	}
	started := first.Build.Started

	time.Sleep(20 * time.Millisecond)
	recordBuildState(root, BuildState{Index: 2, Total: 29, Status: "generating"}, "", "", true)

	second := ReadBuildVerdict(root)
	if !second.Build.Started.Equal(started) {
		t.Errorf("start time moved from %s to %s — elapsed time would reset on every sample",
			started, second.Build.Started)
	}
	if second.Build.State.Index != 2 {
		t.Errorf("position did not advance: %+v", second.Build.State)
	}
}

// Position changes are always written; liveness samples are throttled. A
// position change is a durable fact and there are tens of them, so throttling
// one away loses the answer to "how far did it get" for a build that then died.
func TestPositionIsAlwaysWrittenAndSamplesAreThrottled(t *testing.T) {
	root := t.TempDir()
	idle := 1.0

	// Two position changes in quick succession: both must land.
	recordBuildState(root, BuildState{Index: 1, Total: 29, Status: "generating"}, "", "", true)
	recordBuildState(root, BuildState{Index: 2, Total: 29, Status: "generating"}, "", "", true)
	if got := ReadBuildVerdict(root).Build.State.Index; got != 2 {
		t.Errorf("second position change was dropped: index = %d", got)
	}

	// A sample immediately after is throttled away, leaving the position.
	recordBuildState(root, BuildState{Status: "waiting", IdleSeconds: &idle}, "", "", false)
	if got := ReadBuildVerdict(root).Build.State.Index; got != 2 {
		t.Errorf("a throttled sample overwrote the position: index = %d", got)
	}
}

// Silence is stored as an absolute time, not a duration.
//
// A duration measured at write time ages: a file written four minutes ago
// saying "idle 1s" describes a build that has since been silent for four
// minutes, and reports it as healthy.
func TestSilenceIsStoredAbsolutelySoItAges(t *testing.T) {
	root := t.TempDir()
	idle := 2.0
	recordBuildState(root, BuildState{Status: "waiting", IdleSeconds: &idle}, "", "", true)

	rec := ReadBuildVerdict(root).Build
	if rec.LastEventAt.IsZero() {
		t.Fatal("no absolute last-event time was stored")
	}
	// Recorded as ~2s before the write, so a reader an hour later computes an
	// hour of silence rather than two seconds.
	gap := rec.Updated.Sub(rec.LastEventAt).Seconds()
	if gap < 1.5 || gap > 2.5 {
		t.Errorf("last event recorded %.1fs before the write, want ~2s", gap)
	}
}

// A status command reports; it must not fail because what it reports on failed.
// A caller reads the status field, and a non-zero exit would make a monitoring
// loop treat "the build failed" as "the status command is broken".
func TestStatusReportsFailureWithoutFailing(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, PersistedBuild{
		PID: 2147483646, Started: time.Now().Add(-time.Hour), Updated: time.Now(),
		Terminal: "failed", Detail: "build failed", State: BuildState{Status: "failed"},
	})
	if err := BuildStatus(root, true); err != nil {
		t.Errorf("reporting a failed build returned an error: %v", err)
	}
	if err := BuildStatus(root, false); err != nil {
		t.Errorf("reporting a failed build returned an error: %v", err)
	}
}
