//go:build windows

package platform

// The Windows half, expressed in Windows' own terms.
//
// None of the POSIX vocabulary carries over, and the first version of this file
// said so and stopped there — Kill for stop, no orderly shutdown. That was
// honest and not good enough: a Go binary should work on the three platforms it
// ships for, and "we kill it" means a hub loses its drain, its deregistration
// and its final audit write on one of them.
//
// Windows does have an orderly stop for a console program: a control event
// delivered to a process GROUP. That is why the child is started with
// CREATE_NEW_PROCESS_GROUP — it is not only about surviving Ctrl-C, it is what
// makes the group addressable by GenerateConsoleCtrlEvent afterwards. Go's
// runtime turns CTRL_BREAK_EVENT into os.Interrupt in the child, so a hub that
// listens for os.Interrupt alongside SIGTERM gets the same orderly path it has
// on POSIX.
//
// kernel32 is reached through LazyDLL rather than golang.org/x/sys/windows, to
// keep the dependency policy in platforms/go: two modules, each because Go does
// not ship the primitive the protocol calls for. A console control event is not
// one of those — it is three lines of syscall.

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procGenerateConsoleCtrl = kernel32.NewProc("GenerateConsoleCtrlEvent")
	procGetExitCodeProcess  = kernel32.NewProc("GetExitCodeProcess")
)

// ctrlBreakEvent is the control event a new process group can be sent.
//
// CTRL_BREAK_EVENT and not CTRL_C_EVENT: a Ctrl-C event cannot be directed at a
// single group — passing a group id with CTRL_C_EVENT is documented as
// unsupported — so break is the one that can address the hub and nothing else.
const ctrlBreakEvent = 1

// stillActive is what GetExitCodeProcess reports for a process that has not
// exited (STILL_ACTIVE).
const stillActive = 259

func shellCommand(line string) (string, []string) {
	// /C rather than /K: run the line and exit, never leave an interpreter
	// holding the pipe open.
	return "cmd", []string{"/C", line}
}

// processAlive reports whether a pid is a live process.
//
// Opening a handle is NOT the question: a handle can be opened for a process
// that has already exited while anything still holds a reference to it, so
// os.FindProcess succeeding would report a dead hub as running and `server
// stop` would wait out its whole grace period on nothing. GetExitCodeProcess
// answers the actual question.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const access = syscall.PROCESS_QUERY_INFORMATION | syscall.SYNCHRONIZE
	h, err := syscall.OpenProcess(access, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)

	var code uint32
	r, _, _ := procGetExitCodeProcess.Call(uintptr(h), uintptr(unsafePointerTo(&code)))
	if r == 0 {
		return false
	}
	return code == stillActive
}

// lookPathCandidates covers the executable extensions Windows uses.
//
// A tool named `claude` is claude.exe, or a claude.cmd shim from an npm global
// install. A probe that stats the bare name finds neither, which presents as
// "not installed" to somebody using it in another window.
func lookPathCandidates(name string) []string {
	return []string{name + ".exe", name + ".cmd", name + ".bat", name}
}

// extraInstallDirs are the Windows locations coding-agent CLIs install to.
//
// The shared list is POSIX — ~/.local/bin, /opt/homebrew/bin — and none of it
// exists here, so discovery on Windows would find nothing whatever was
// installed.
func extraInstallDirs() []string {
	var dirs []string
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		dirs = append(dirs,
			v+`\Programs`,
			v+`\Programs\claude`,
			v+`\npm`,
		)
	}
	if v := os.Getenv("APPDATA"); v != "" {
		dirs = append(dirs, v+`\npm`)
	}
	if v := os.Getenv("ProgramFiles"); v != "" {
		dirs = append(dirs, v+`\nodejs`)
	}
	return dirs
}

// configureDetached gives the child its own process group.
//
// Two reasons, and the second is the one that was missing: a Ctrl-C in the
// launching console is not propagated to a new group, AND a new group is what
// GenerateConsoleCtrlEvent can address in order to ask for an orderly stop.
func configureDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// RequestStop asks for an orderly shutdown.
//
// A console control event to the hub's own process group. Go's runtime delivers
// CTRL_BREAK_EVENT to the child as os.Interrupt, so a component listening for
// os.Interrupt drains, deregisters and writes its final audit entry exactly as
// it does for SIGTERM elsewhere.
func RequestStop(p *os.Process) error {
	if p == nil {
		return fmt.Errorf("no process to stop")
	}
	r, _, err := procGenerateConsoleCtrl.Call(uintptr(ctrlBreakEvent), uintptr(uint32(p.Pid)))
	if r == 0 {
		return fmt.Errorf("asking process %d to stop: %w", p.Pid, err)
	}
	return nil
}

// ForceStop ends a process that would not go.
func ForceStop(p *os.Process) error {
	if p == nil {
		return fmt.Errorf("no process to stop")
	}
	return p.Kill()
}
