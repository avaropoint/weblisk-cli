// Package platform is the small set of operations that have no common spelling
// across operating systems.
//
// The CLI ships binaries for darwin, linux and windows. Everything else in this
// program is portable Go; these four things are not, and each was written for
// POSIX and either failed to compile on Windows or compiled and then behaved
// wrongly:
//
//	ProcessAlive   POSIX asks with signal 0. Windows has no such probe, and
//	               os.FindProcess succeeding is not the same question.
//	RequestStop    SIGTERM, or a console control event to the process group.
//	ForceStop      SIGKILL, or TerminateProcess.
//	DetachAttrs    setsid, or CREATE_NEW_PROCESS_GROUP.
//	ShellCommand   `sh -c`, or `cmd /C`.
//
// Split into a package rather than left as build-tagged helpers beside their
// callers, because there were three separate copies of the liveness probe —
// dispatch/lock.go, server/runstate.go and server/lifecycle.go — and a rule
// with three implementations has three behaviours.
package platform

import "os/exec"

// ShellCommand builds a command that runs a shell line.
//
// Build and Run lines come from a platform blueprint as single strings —
// "cd server && go build -o orchestrator ." — so they need a shell to
// interpret the operators, not an argv split.
func ShellCommand(line string) (name string, args []string) {
	return shellCommand(line)
}

// ProcessAlive reports whether a pid names a running process.
func ProcessAlive(pid int) bool { return processAlive(pid) }

// LookPathCandidates returns the filenames a bare tool name may have.
//
// On Windows a program named `claude` is claude.exe, claude.cmd or claude.bat,
// and a probe that stats the bare name finds nothing even where the tool is
// installed and working. exec.LookPath already applies PATHEXT; explicit
// directory probes do not, which is what this is for.
func LookPathCandidates(name string) []string { return lookPathCandidates(name) }

// ExtraInstallDirs are the places a coding-agent CLI installs itself on THIS
// platform, beyond the ones common to all of them.
func ExtraInstallDirs() []string { return extraInstallDirs() }

// ConfigureDetached sets whatever a command needs in order to outlive the
// process that started it.
func ConfigureDetached(cmd *exec.Cmd) { configureDetached(cmd) }

// StartInOwnGroup makes cmd the leader of a new process group, so that
// KillGroup can take its children with it.
//
// Needed because killing only the process leaves its children holding the
// stdout pipe open. A stalled provider abandoned for silence would keep the
// reader blocked on a pipe nothing would ever write to again — the abort was
// detected and did not take effect.
func StartInOwnGroup(cmd *exec.Cmd) { startInOwnGroup(cmd) }

// KillGroup kills cmd's process group, or cmd alone where the platform has no
// group to kill.
func KillGroup(cmd *exec.Cmd) error { return killGroup(cmd) }
