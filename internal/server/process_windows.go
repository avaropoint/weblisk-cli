//go:build windows

package server

// process_windows.go — the same three operations, in Windows' terms.
//
// None of the POSIX vocabulary carries over. Windows has no session leader, no
// SIGTERM, and no signal-0 liveness probe, so each operation is expressed as
// the nearest thing that is actually true rather than as a POSIX call that
// compiles and then fails at run time.

import (
	"os"
	"syscall"
)

// detachAttrs gives the hub its own process group.
//
// CREATE_NEW_PROCESS_GROUP is the analogue of setsid for the property that
// matters here: a Ctrl-C delivered to the launching console is not propagated
// to the new group, so the hub survives the CLI exiting.
func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// requestStop ends the process.
//
// Windows has no SIGTERM to ask with. Kill is what is available, so the orderly
// shutdown path the Unix build relies on does NOT run here — said plainly
// rather than hidden behind a call named after a signal that does not exist.
// Giving Windows a real graceful stop means the hub listening for a console
// control event or a named shutdown request, which is a hub-side change and
// belongs in architecture/lifecycle rather than in this file.
func requestStop(p *os.Process) error { return p.Kill() }

// forceStop ends a process that would not go. Already the only option above.
func forceStop(p *os.Process) error { return p.Kill() }

// processAlive reports whether a pid is a live process.
//
// os.FindProcess on Windows opens a real handle and fails when there is no such
// process, so having one IS the liveness answer — unlike POSIX, where
// FindProcess always succeeds and the signal is what asks.
func processAlive(p *os.Process) bool {
	if p == nil {
		return false
	}
	found, err := os.FindProcess(p.Pid)
	if err != nil || found == nil {
		return false
	}
	return true
}
