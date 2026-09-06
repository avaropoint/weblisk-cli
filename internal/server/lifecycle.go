// lifecycle.go — starting, stopping and reporting a tenant's hub.
//
// `server start` ran the hub in the FOREGROUND and blocked until it exited.
// Right for a terminal, and unusable for anything else: invoked from Studio the
// hub becomes a child of an HTTP request, dies when the request ends, and the
// caller is left holding a process it should not own.
//
// So the CLI owns the lifecycle completely — start, stop, status — and every
// caller invokes the same commands. Studio never holds a PID.
package server

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// stopGrace is how long a hub is given to shut down cleanly before it is killed.
//
// architecture/agent requires an orderly shutdown — stop accepting work, drain
// what is in flight, deregister — and that takes longer than a signal. Killing
// immediately would make the graceful path unreachable and leave the
// orchestrator holding a registration for an agent that is gone.
const stopGrace = 10 * time.Second

// StartDetached builds the component and starts it in the background.
func StartDetached(root, component string, port int, extra []string) (RunState, error) {
	if st := StatusOf(root, component); st.Running {
		return RunState{}, fmt.Errorf("%s is already running (pid %d) at %s\n  Stop it first, or use `weblisk server status`",
			component, st.PID, st.Address)
	}

	bin := filepath.Join(root, "bin", component)
	build := exec.Command("go", "build", "-o", bin, "./cmd/"+component)
	build.Dir = root
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return RunState{}, fmt.Errorf("build failed: %w", err)
	}

	if err := os.MkdirAll(logDir(root), 0o700); err != nil {
		return RunState{}, err
	}
	logPath := filepath.Join(logDir(root), component+".log")
	// Appended, not truncated: a crash loop's history is the diagnosis, and
	// truncating on start discards the run that explains why this one is
	// starting.
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return RunState{}, err
	}
	defer logFile.Close()

	args := append([]string{"--port", strconv.Itoa(port)}, extra...)
	cmd := exec.Command(bin, args...)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.Stdin = nil
	// Its own process group, so it survives the CLI exiting and a Ctrl-C in the
	// terminal that launched it does not take the hub down with it. Expressed
	// per platform — see process_unix.go and process_windows.go.
	cmd.SysProcAttr = detachAttrs()

	if err := cmd.Start(); err != nil {
		return RunState{}, fmt.Errorf("starting %s: %w", component, err)
	}
	// Released rather than waited on: this process is leaving, and holding the
	// child would tie its lifetime to ours.
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()

	st := RunState{
		Component: component,
		PID:       pid,
		Address:   fmt.Sprintf("http://localhost:%d", port),
		Port:      port,
		Binary:    bin,
		LogPath:   logPath,
		Started:   time.Now().UTC(),
	}
	if err := writeRunState(root, st); err != nil {
		return st, fmt.Errorf("started (pid %d) but could not record it: %w", pid, err)
	}
	return st, nil
}

// Stop asks a component to shut down, and insists if it will not.
func Stop(root, component string) error {
	st, ok := readRunState(root, component)
	if !ok {
		return fmt.Errorf("%s is not recorded as running", component)
	}
	if !alive(st.PID) {
		clearRunState(root, component)
		return fmt.Errorf("%s was recorded as running (pid %d) but the process is gone — cleared the record; see %s",
			component, st.PID, st.LogPath)
	}

	p, err := os.FindProcess(st.PID)
	if err != nil {
		return err
	}
	// Asked first. architecture/agent requires an orderly shutdown, and that
	// path only exists if the component is asked rather than killed.
	if err := requestStop(p); err != nil {
		return fmt.Errorf("signalling %s (pid %d): %w", component, st.PID, err)
	}

	deadline := time.Now().Add(stopGrace)
	for time.Now().Before(deadline) {
		if !alive(st.PID) {
			clearRunState(root, component)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	// It did not go. Reported rather than silent: a component that ignores
	// SIGTERM has a bug worth knowing about, and killing it quietly hides that.
	_ = forceStop(p)
	clearRunState(root, component)
	return fmt.Errorf("%s did not shut down within %s and was killed — its graceful shutdown path did not complete",
		component, stopGrace)
}

// handleStop is the `weblisk server stop` command.
func handleStop(args []string, root string) error {
	component := "orchestrator"
	for i := 0; i < len(args); i++ {
		if args[i] == "--component" && i+1 < len(args) {
			i++
			component = args[i]
		}
	}
	if err := Stop(root, component); err != nil {
		return err
	}
	fmt.Printf("  [ok] %s stopped\n", component)
	return nil
}

// handleServerStatus is `weblisk server status`, which reports a component's
// run state — distinct from `weblisk status`, which asks an orchestrator over
// HTTP what the network looks like.
func handleServerStatus(args []string, root string) error {
	component := "orchestrator"
	asJSON := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--component":
			if i+1 < len(args) {
				i++
				component = args[i]
			}
		}
	}

	st := StatusOf(root, component)
	if asJSON {
		// The interface Studio programs against. The on-disk layout stays
		// private to this package precisely so it can change without breaking
		// anything that reads this.
		b, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}

	fmt.Println()
	switch {
	case st.Running:
		fmt.Printf("  %s is running\n", component)
		fmt.Printf("    address   %s\n", st.Address)
		fmt.Printf("    pid       %d\n", st.PID)
		fmt.Printf("    uptime    %s\n", time.Duration(st.UptimeSec)*time.Second)
		fmt.Printf("    log       %s\n", st.LogPath)
	case st.Stale:
		fmt.Printf("  %s is NOT running\n", component)
		fmt.Printf("    %s\n", st.Note)
	default:
		fmt.Printf("  %s is not running\n", component)
		fmt.Printf("    %s\n", st.Note)
	}
	fmt.Println()
	return nil
}

// handleLogs streams a component's log.
//
// The log lives under .weblisk/, which is the hub's operational state — so
// reading it is the CLI's job, and a caller that wants the log asks for it
// rather than opening the directory.
func handleLogs(args []string, root string) error {
	component := "orchestrator"
	tail := 200
	follow := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--follow", "-f":
			follow = true
		case "--component":
			if i+1 < len(args) {
				i++
				component = args[i]
			}
		case "--tail", "-n":
			if i+1 < len(args) {
				i++
				if n, err := strconv.Atoi(args[i]); err == nil && n >= 0 {
					tail = n
				}
			}
		}
	}

	path := filepath.Join(logDir(root), component+".log")
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("no log for %s — has it been started?", component)
	}
	defer f.Close()

	lines, err := lastN(f, tail)
	if err != nil {
		return err
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	if !follow {
		return nil
	}
	return followFrom(f)
}

// lastN reads the final n lines without loading the whole file.
//
// A hub logging every request with spans produces a large file, and a viewer
// that reads it all to show the last screen is a memory fault waiting for a
// long-running tenant.
func lastN(f *os.File, n int) ([]string, error) {
	if n == 0 {
		if _, err := f.Seek(0, 2); err != nil {
			return nil, err
		}
		return nil, nil
	}
	const chunk = 64 << 10
	size, err := f.Seek(0, 2)
	if err != nil {
		return nil, err
	}
	var (
		buf   []byte
		count int
		pos   = size
	)
	for pos > 0 && count <= n {
		read := int64(chunk)
		if pos < read {
			read = pos
		}
		pos -= read
		block := make([]byte, read)
		if _, err := f.ReadAt(block, pos); err != nil {
			return nil, err
		}
		buf = append(block, buf...)
		count = strings.Count(string(buf), "\n")
	}
	all := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(all) > n {
		all = all[len(all)-n:]
	}
	if _, err := f.Seek(0, 2); err != nil {
		return nil, err
	}
	return all, nil
}

// followFrom tails a file from its current offset.
func followFrom(f *os.File) error {
	buf := make([]byte, 32<<10)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			os.Stdout.Write(buf[:n])
		}
		if err != nil {
			time.Sleep(300 * time.Millisecond)
		}
	}
}
