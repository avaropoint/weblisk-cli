package dispatch

// buildstate.go — a build's state, on disk, so it can be ASKED rather than
// only watched.
//
// Streaming progress answers "what is happening" to whoever is holding the
// stream. It answers nothing to anybody else, and nothing at all once the
// stream is gone: a build started by Studio and then reloaded, a build in
// another terminal, a build whose console was closed. All of those are ordinary,
// and for all of them the only available question was "is a process running",
// whose answer is yes right up until it is no.
//
// # The verdict is derived, never timed
//
// Status is computed from two observed facts and no clock of its own:
//
//	is the writing process alive        platform.ProcessAlive
//	when did the provider last speak    the stream's own last event
//
// From those: running (alive, recently spoke), stalled (alive, silent past the
// allowance the abort itself uses), died (gone, with no terminal state
// recorded), or the terminal state it recorded. Nothing here decides how long
// is too long — it reuses the one allowance defined for that in
// localcli_stream.go, so a build's status and a build's abort cannot disagree
// about what "stalled" means.
//
// # Why it is written by the observer
//
// The observer already receives every position change and every liveness
// sample. Persisting from there means the file cannot drift from what the
// console was shown, because it is the same value.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// buildStateFile is where the state lives, beside the lock that names its owner.
const buildStateFile = "build-state.json"

// buildStateWriteInterval throttles the write.
//
// Activity samples arrive every couple of seconds and a build runs for forty
// minutes; writing each one is a thousand writes for a file only read when
// somebody asks. A position change is always written, because "file 12 of 29"
// is the durable fact and there are only tens of them.
const buildStateWriteInterval = 2 * time.Second

// PersistedBuild is a build's state as recorded on disk.
type PersistedBuild struct {
	PID     int       `json:"pid"`
	Target  string    `json:"target"`
	Started time.Time `json:"started"`
	// Updated is when this file was last written.
	Updated time.Time `json:"updated"`
	// LastEventAt is when the PROVIDER last spoke. Distinct from Updated: a
	// file written two seconds ago recording silence of four minutes is the
	// case this whole file exists to describe, and one timestamp cannot say it.
	LastEventAt time.Time `json:"last_event_at"`
	// State is the last state the console was shown.
	State BuildState `json:"state"`
	// Terminal is "completed", "failed" or "" while still running.
	Terminal string `json:"terminal,omitempty"`
	// Detail explains a failure.
	Detail string `json:"detail,omitempty"`
}

// BuildVerdict is what a caller wants to know.
type BuildVerdict struct {
	// Status is one of: none, running, stalled, died, completed, failed.
	Status string `json:"status"`
	// Why says how the status was reached, in terms of the facts behind it.
	Why string `json:"why"`
	// Build is the recorded state, when there is any.
	Build *PersistedBuild `json:"build,omitempty"`
	// IdleSeconds is how long the provider has been silent, when running.
	IdleSeconds float64 `json:"idle_seconds,omitempty"`
	// ElapsedSeconds is how long the build has been going.
	ElapsedSeconds float64 `json:"elapsed_seconds,omitempty"`
}

func buildStatePath(root string) string {
	return filepath.Join(root, cacheDirName, buildStateFile)
}

var (
	buildStateMu        sync.Mutex
	buildStateLastWrite time.Time
)

// recordBuildState persists a state snapshot for `weblisk build status`.
//
// force is set for position changes and terminal states, which are durable
// facts; liveness samples are throttled.
func recordBuildState(root string, st BuildState, terminal, detail string, force bool) {
	buildStateMu.Lock()
	if !force && time.Since(buildStateLastWrite) < buildStateWriteInterval {
		buildStateMu.Unlock()
		return
	}
	buildStateLastWrite = time.Now()
	buildStateMu.Unlock()

	path := buildStatePath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}

	// Started is preserved across writes: it belongs to the build, not to the
	// sample. Read back rather than held in a variable so a resumed build keeps
	// the original start time.
	rec := PersistedBuild{PID: os.Getpid(), Target: root, Started: time.Now()}
	if b, err := os.ReadFile(path); err == nil {
		var prev PersistedBuild
		if json.Unmarshal(b, &prev) == nil && prev.PID == os.Getpid() && !prev.Started.IsZero() {
			rec.Started = prev.Started
		}
	}
	rec.Updated = time.Now()
	rec.State = st
	rec.Terminal = terminal
	rec.Detail = detail
	// A liveness sample carries how long the provider has been quiet; that is
	// converted back to an absolute time, because a reader compares it to now
	// and a duration measured at write time would age.
	if st.IdleSeconds != nil {
		rec.LastEventAt = rec.Updated.Add(-time.Duration(*st.IdleSeconds * float64(time.Second)))
	} else {
		rec.LastEventAt = rec.Updated
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}
	// Written to a temporary file and renamed, because `weblisk build status`
	// may read while this writes and a torn read reports a build as having no
	// state at all — which is the one answer this file exists to prevent.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".build-state-*")
	if err != nil {
		return
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
	}
}

// ReadBuildVerdict answers "what is this build doing" from disk.
func ReadBuildVerdict(root string) BuildVerdict {
	path := buildStatePath(root)
	b, err := os.ReadFile(path)
	if err != nil {
		return BuildVerdict{Status: "none", Why: "no build state recorded in " + filepath.Dir(path)}
	}
	var rec PersistedBuild
	if json.Unmarshal(b, &rec) != nil {
		return BuildVerdict{Status: "none", Why: "the recorded build state could not be read"}
	}

	v := BuildVerdict{Build: &rec}
	if !rec.Started.IsZero() {
		v.ElapsedSeconds = time.Since(rec.Started).Seconds()
	}
	if !rec.LastEventAt.IsZero() {
		v.IdleSeconds = time.Since(rec.LastEventAt).Seconds()
	}

	// A recorded terminal state is the answer, whatever the process is doing.
	if rec.Terminal != "" {
		v.Status = rec.Terminal
		v.Why = fmt.Sprintf("the build recorded %s at %s", rec.Terminal, rec.Updated.Format(time.RFC3339))
		if rec.Detail != "" {
			v.Why += ": " + rec.Detail
		}
		return v
	}

	// No terminal state. Then the process is the evidence.
	if rec.PID <= 0 || !processAlive(rec.PID) {
		v.Status = "died"
		v.Why = fmt.Sprintf("pid %d is gone and recorded no outcome — it was killed or crashed. "+
			"Its last state was %q; `weblisk tenant create --resume` continues from the files it banked",
			rec.PID, rec.State.Describe())
		return v
	}

	// Alive. Silence past the allowance the abort itself uses is stalled — the
	// same number, so a status and an abort cannot disagree.
	if v.IdleSeconds > defaultIdleTimeout.Seconds() {
		v.Status = "stalled"
		v.Why = fmt.Sprintf("pid %d is alive but the provider has said nothing for %s, past the %s allowance",
			rec.PID, roundDuration(time.Duration(v.IdleSeconds)*time.Second), defaultIdleTimeout)
		return v
	}
	v.Status = "running"
	v.Why = fmt.Sprintf("pid %d is alive and the provider spoke %s ago",
		rec.PID, roundDuration(time.Duration(v.IdleSeconds)*time.Second))
	return v
}

// MarkBuildTerminal records how a build ended, so a later reader can tell a
// finished build from a crashed one.
//
// It PRESERVES the last known state rather than replacing it. The first version
// wrote `BuildState{Status: terminal}`, which discarded the position and the
// provider's quota — so after a build ended, `weblisk build status` could say
// "failed" and not how far it got or what the quota was, which is exactly what
// somebody asks next. Observed on a completed build: `quota: (none recorded)`.
//
// A terminal record is the one a person reads LONG after the fact, when the
// stream is gone and the process with it. It is the worst place to throw
// context away.
func MarkBuildTerminal(root, terminal, detail string) {
	last := BuildState{Status: terminal}
	if prev := ReadBuildVerdict(root); prev.Build != nil {
		last = prev.Build.State
		last.Status = terminal
	}
	recordBuildState(root, last, terminal, detail, true)
}
