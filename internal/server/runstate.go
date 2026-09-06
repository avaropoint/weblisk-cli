// runstate.go — what the CLI knows about a running hub, and who may know it.
//
// # Why this is the CLI's and not Studio's
//
// A running hub's state lives in the tenant's `.weblisk/` directory, alongside
// `keys/orchestrator.key`, `grants/` and `bootstrap`. Anything that opens that
// directory to read a port has read access to the tenant's private key material
// and its one-time admission secret.
//
// Studio is the editor: it reads and writes blueprints and documents all day.
// It does not open `.weblisk/`. standards/project-structure already draws the
// line — "`.weblisk/config.yaml` is not a blueprint, this is operational
// configuration" — and the CLI owns the operational side.
//
// So Studio asks, and `status --json` answers. The payoff is that this file
// format stays private: the CLI can change how it records a running hub without
// breaking anything, because the JSON contract is the interface and the layout
// is not.
//
// # Intent and fact are different
//
// config.yaml says which port the orchestrator SHOULD bind. This says what it
// DID bind. A hub started with `--port 8801` makes config.yaml a lie, and a
// dashboard wants the fact.
package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RunState is one running component, as the CLI recorded it at start.
type RunState struct {
	Component string    `json:"component"`
	PID       int       `json:"pid"`
	Address   string    `json:"address"`
	Port      int       `json:"port"`
	Binary    string    `json:"binary"`
	LogPath   string    `json:"log_path"`
	Started   time.Time `json:"started"`
}

// Status is what `server status --json` answers.
//
// Recorded and Running are separate. A run state whose process is gone is a
// crash, and reporting it as "not running" loses that — an operator wants to
// know the difference between never started and died.
type Status struct {
	Component string     `json:"component"`
	Recorded  bool       `json:"recorded"`
	Running   bool       `json:"running"`
	PID       int        `json:"pid,omitempty"`
	Address   string     `json:"address,omitempty"`
	LogPath   string     `json:"log_path,omitempty"`
	Started   *time.Time `json:"started,omitempty"`
	UptimeSec int64      `json:"uptime_seconds,omitempty"`
	// Stale is a recorded run whose process no longer exists: it died without
	// being stopped.
	Stale bool   `json:"stale,omitempty"`
	Note  string `json:"note,omitempty"`
}

func runDir(root string) string { return filepath.Join(root, ".weblisk", "run") }
func logDir(root string) string { return filepath.Join(root, ".weblisk", "logs") }
func runStatePath(root, component string) string {
	return filepath.Join(runDir(root), component+".json")
}

func writeRunState(root string, st RunState) error {
	if err := os.MkdirAll(runDir(root), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	// 0600: this names a process an operator may signal, in a directory holding
	// key material. It is nobody else's business.
	return os.WriteFile(runStatePath(root, st.Component), b, 0o600)
}

func readRunState(root, component string) (RunState, bool) {
	b, err := os.ReadFile(runStatePath(root, component))
	if err != nil {
		return RunState{}, false
	}
	var st RunState
	if json.Unmarshal(b, &st) != nil || st.PID <= 0 {
		return RunState{}, false
	}
	return st, true
}

func clearRunState(root, component string) {
	_ = os.Remove(runStatePath(root, component))
}

// alive reports whether a pid names a running process.
//
// How that is asked differs by platform and has no common spelling — see
// process_unix.go and process_windows.go.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return processAlive(p)
}

// StatusOf reports what is known about a component.
func StatusOf(root, component string) Status {
	s := Status{Component: component}
	st, ok := readRunState(root, component)
	if !ok {
		s.Note = "not started — run `weblisk server start --detach`"
		return s
	}
	s.Recorded = true
	s.PID, s.Address, s.LogPath = st.PID, st.Address, st.LogPath
	started := st.Started
	s.Started = &started

	if !alive(st.PID) {
		// Recorded but gone. Not the same as never started, and an operator
		// needs the difference: this one died, and the log says why.
		s.Stale = true
		s.Note = fmt.Sprintf("recorded as running (pid %d) but the process is gone — it exited without being stopped; see %s",
			st.PID, st.LogPath)
		return s
	}
	s.Running = true
	s.UptimeSec = int64(time.Since(st.Started).Seconds())
	return s
}
