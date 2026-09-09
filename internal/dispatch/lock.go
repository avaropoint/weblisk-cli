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

// lockPath ignores planRoot, and that is deliberate rather than a bug — but
// the name and the callers' "one writer per target" both overstate it, so it
// is said here: this is ONE lock per tenant root, not one per target.
//
// That is the stricter of the two, and the safe direction. Two targets in one
// root share a module, a go.mod and a cache; serialising them costs a wait and
// admitting them concurrently costs a corrupted tree. If it is ever narrowed
// to a real per-target lock, planRoot is already threaded here to do it with.
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

	rec, _ := json.Marshal(lockRecord{PID: os.Getpid(), Target: planRoot, Started: time.Now()})

	// O_EXCL, not read-then-write.
	//
	// This was `os.ReadFile` to check the holder, then `os.WriteFile` to claim
	// it — two syscalls with a window between them. Two runs starting together
	// both read "no lock" (or both read the same stale one) and both went on to
	// write, so each believed it held an exclusive lock and neither did. The one
	// file written specifically to stop two generations sharing a directory
	// could be held twice, and the failure it was meant to prevent —
	// redeclaration errors between two individually correct plans — is exactly
	// what came out. O_CREATE|O_EXCL makes the check and the claim one atomic
	// operation, which is the only way this is actually a lock.
	//
	// The retry exists for the stale case only: losing the create means somebody
	// else won it, so the loop goes back and reads whose it is. Bounded, because
	// a lock that cannot be settled in a few attempts is contention to report,
	// not to spin on.
	for attempt := 0; attempt < 5; attempt++ {
		claimed, cerr := claimLockFile(path, rec)
		if cerr == nil && claimed {
			return func() { releaseIfOurs(path) }, nil
		}
		if cerr != nil {
			return nil, cerr
		}

		// Somebody holds it. Whether that somebody still exists decides
		// whether this is contention or debris.
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			// Removed between the failed create and this read — go round and
			// try to claim it.
			continue
		}
		var held lockRecord
		if len(b) == 0 {
			// Cannot happen with the link-then-claim above — the file is
			// complete before it has a name. Kept as a guard because the
			// version that COULD produce it treated an empty file as debris,
			// removed a lock somebody else had just taken, and let two runs in.
			continue
		}
		if json.Unmarshal(b, &held) == nil && held.PID > 0 && processAlive(held.PID) {
			return nil, fmt.Errorf(
				"another generation is writing to %s (pid %d, started %s ago)\n"+
					"  Two generations sharing a directory produce redeclaration errors between\n"+
					"  two correct plans. Wait for it, or stop it first.",
				planRoot, held.PID, roundDuration(time.Since(held.Started)))
		}
		// Stale: the holder is gone, or the record is unreadable. Clear it and
		// contend for the create again — whoever wins O_EXCL wins the lock, so
		// two processes clearing the same debris still cannot both proceed.
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			return nil, rmErr
		}
	}
	return nil, fmt.Errorf("could not settle the generation lock at %s after 5 attempts — "+
		"something is creating and removing it concurrently", path)
}

// releaseIfOurs removes the lock only while this process still holds it.
//
// release() used to be an unconditional os.Remove, which is a second way for
// two generations to end up in one directory: run A stalls long enough to look
// dead, run B takes the stale lock over legitimately, then A finishes and
// deletes B's lock on its way out. B carries on believing it is protected, and
// a third run walks straight in. Checking the record first costs one read at
// the end of a build.
func releaseIfOurs(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return // already gone
	}
	var held lockRecord
	if json.Unmarshal(b, &held) == nil && held.PID != os.Getpid() {
		return // somebody else's now — taken over while this run was stalled
	}
	_ = os.Remove(path)
}

// claimLockFile publishes a COMPLETE lock record under path, atomically.
//
// The obvious O_CREATE|O_EXCL then Write is not enough, and the test that
// races two dozen acquisitions is what showed it: between the exclusive create
// and the write, the file exists and is EMPTY. A second process fails the
// create, reads zero bytes, cannot parse a holder out of them, concludes the
// lock is debris, deletes it and takes it — so both are inside. The exclusive
// create was atomic and the thing it protected was not.
//
// Link fixes the ordering: the record is written to a private temporary file
// FIRST and only given its public name once it is complete. os.Link fails with
// EEXIST if the name is taken, so the name is still claimed atomically, and
// any reader that finds the name always finds a whole record behind it.
//
// Returns (false, nil) when somebody else holds the name — the caller decides
// whether that is contention or debris.
func claimLockFile(path string, rec []byte) (bool, error) {
	// Same directory, so the link cannot cross a filesystem.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".generating.lock.*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(rec); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Link(tmpName, path); err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// processAlive reports whether a pid names a running process.
//
// One implementation, in internal/platform. This was a second copy asking with
// signal 0, which on Windows always fails — so a lock left behind by a crashed
// run would have been treated as live forever and every later run refused.
func processAlive(pid int) bool { return platform.ProcessAlive(pid) }
