//go:build !windows

package platform

import (
	"os"
	"os/exec"
	"syscall"
)

func shellCommand(line string) (string, []string) {
	return "sh", []string{"-c", line}
}

// processAlive asks with signal 0, which performs the existence and permission
// checks without delivering anything.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// FindProcess always succeeds on POSIX — the signal is what asks.
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// lookPathCandidates is the name itself: POSIX has no executable extensions.
func lookPathCandidates(name string) []string { return []string{name} }

func extraInstallDirs() []string { return nil }

// configureDetached puts the child in its own session, so it survives the CLI
// exiting and a Ctrl-C in the launching terminal does not take it down.
func configureDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// RequestStop asks for an orderly shutdown. architecture/agent requires one,
// and that path only exists if the component is ASKED rather than killed.
func RequestStop(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

// ForceStop ends a process that would not go.
func ForceStop(p *os.Process) error { return p.Signal(syscall.SIGKILL) }
