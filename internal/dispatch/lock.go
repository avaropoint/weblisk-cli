package dispatch

// One writer per target directory.
//
// Two generations were once running against the same server/ directory at the
// same time. The second began with a clean sweep of the target; the first, still
// mid-repair, then wrote its own twenty files back over the top. The build came
// back with twenty-one redeclaration errors between two perfectly good plans:
//
//	./observability.go:21:2: LevelDebug redeclared in this block
//	    ./helpers.go:350:2: other declaration of LevelDebug
//
// Nothing was wrong with either plan or either set of files. The directory had
// two owners. That failure is indistinguishable from a pipeline fault when you
// read the errors, and it cost an hour of looking in the wrong place.
//
// The lock is advisory in the POSIX sense — a stale lock from a killed process
// is detected and taken over rather than blocking forever, because a crash must
// not require manual cleanup before the next run.

import (
	"encoding/json"
	"fmt"
	"github.com/avaropoint/weblisk-cli/internal/platform"
	"os"
	"path/filepath"
	"time"
)

type lockRecord struct {
	PID     int       `json:"pid"`
	Target  string    `json:"target"`
	Started time.Time `json:"started"`
}

func lockPath(root, planRoot string) string {
	return filepath.Join(root, cacheDirName, "generating.lock")
}

// AcquireTargetLock claims exclusive write access to a target directory.
//
// Returns a release function. A lock held by a process that no longer exists is
// taken over: the alternative is a crashed run leaving a directory permanently
// unbuildable, which trades a rare confusing failure for a common annoying one.
func AcquireTargetLock(root, planRoot string) (release func(), err error) {
	path := lockPath(root, planRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	if b, rerr := os.ReadFile(path); rerr == nil {
		var held lockRecord
		if json.Unmarshal(b, &held) == nil && held.PID > 0 && processAlive(held.PID) {
			return nil, fmt.Errorf(
				"another generation is writing to %s (pid %d, started %s ago)\n"+
					"  Two generations sharing a directory produce redeclaration errors between\n"+
					"  two correct plans. Wait for it, or stop it first.",
				planRoot, held.PID, roundDuration(time.Since(held.Started)))
		}
		// Stale: the holder is gone.
	}

	rec, _ := json.Marshal(lockRecord{PID: os.Getpid(), Target: planRoot, Started: time.Now()})
	if err := os.WriteFile(path, rec, 0o644); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}

// processAlive reports whether a pid names a running process.
//
// One implementation, in internal/platform. This was a second copy asking with
// signal 0, which on Windows always fails — so a lock left behind by a crashed
// run would have been treated as live forever and every later run refused.
func processAlive(pid int) bool { return platform.ProcessAlive(pid) }
