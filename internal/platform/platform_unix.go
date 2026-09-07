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

func startInOwnGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killGroup signals the whole group. A negative pid is the group, per kill(2).
func killGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// The group id equals the leader's pid, because Setpgid was set with no
	// Pgid — the child became its own leader.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	// No group (Setpgid was not set, or the leader is gone): the process alone.
	return cmd.Process.Kill()
}
