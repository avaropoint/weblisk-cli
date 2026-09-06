//go:build !windows

package server

// process_unix.go — the three things starting and stopping a hub need from the
// operating system, on everything that is not Windows.
//
// Split out because syscall.SysProcAttr has no common shape: Setsid does not
// exist on Windows, so a single file naming it cannot compile there. The CLI
// cross-compiles for windows/amd64 as a distribution target and had never been
// checked — the CI job that would have caught it was running no tests and no
// cross-compile until it was repaired.

import (
	"os"
	"syscall"
)

// detachAttrs puts a started hub in its own session.
//
// So it survives the CLI exiting, and a Ctrl-C in the terminal that launched it
// does not take the hub down with it.
func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// requestStop asks a process to shut down in an orderly way.
//
// SIGTERM, because architecture/agent requires an orderly shutdown and that
// path only exists if the component is ASKED rather than killed.
func requestStop(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

// forceStop ends a process that would not go.
func forceStop(p *os.Process) error { return p.Signal(syscall.SIGKILL) }

// processAlive reports whether a pid is a live process this user may signal.
//
// Signal 0 performs the existence and permission checks without delivering
// anything, which is the portable way to ask on a POSIX system.
func processAlive(p *os.Process) bool { return p.Signal(syscall.Signal(0)) == nil }
