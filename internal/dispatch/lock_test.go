package dispatch

// One writer per target directory.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestASecondGenerationCannotShareATarget(t *testing.T) {
	// The fault: a second run swept the target clean while the first, still
	// mid-repair, wrote its own twenty files back over the top. The build came
	// back with twenty-one redeclarations between two correct plans — which reads
	// exactly like a pipeline fault and is not one.
	root := t.TempDir()
	release, err := AcquireTargetLock(root, "server")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, err = AcquireTargetLock(root, "server")
	if err == nil {
		t.Fatal("a second generation acquired the same target")
	}
	if !strings.Contains(err.Error(), "another generation") {
		t.Errorf("the error does not say what is wrong: %v", err)
	}
	// It must name the running holder, or the operator cannot act on it.
	if !strings.Contains(err.Error(), "pid") {
		t.Errorf("the holder is not identified: %v", err)
	}

	// Released, it is available again.
	release()
	r2, err := AcquireTargetLock(root, "server")
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	r2()
}

func TestAStaleLockIsTakenOver(t *testing.T) {
	// A crashed run must not leave a directory permanently unbuildable. That
	// trades a rare confusing failure for a common annoying one.
	root := t.TempDir()
	dir := filepath.Join(root, cacheDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A pid that cannot be running: process IDs are allocated from a bounded
	// range and this is above it on every platform we target.
	rec, _ := json.Marshal(lockRecord{PID: 0x7FFFFFF0, Target: "server", Started: time.Now()})
	if err := os.WriteFile(filepath.Join(dir, "generating.lock"), rec, 0o644); err != nil {
		t.Fatal(err)
	}

	release, err := AcquireTargetLock(root, "server")
	if err != nil {
		t.Fatalf("a stale lock blocked a new run: %v", err)
	}
	release()
}

func TestAnUnreadableLockDoesNotBlockForever(t *testing.T) {
	// Corrupt content is the same situation as a stale lock: nobody demonstrable
	// holds it.
	root := t.TempDir()
	dir := filepath.Join(root, cacheDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generating.lock"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireTargetLock(root, "server")
	if err != nil {
		t.Fatalf("an unreadable lock blocked a new run: %v", err)
	}
	release()
}

// The property the lock exists for, asserted directly: under concurrent
// acquisition exactly ONE holder gets in.
//
// The previous implementation read the file, decided, then wrote — and would
// pass every test above while failing this one, because both goroutines read
// "no lock" before either wrote. A lock with no exclusion test is a file.
func TestOnlyOneGenerationHoldsTheLock(t *testing.T) {
	root := t.TempDir()

	const racers = 24
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(racers)

	var mu sync.Mutex
	var held int
	var releases []func()

	for i := 0; i < racers; i++ {
		go func() {
			defer done.Done()
			start.Wait()
			release, err := AcquireTargetLock(root, "orchestrator")
			if err != nil {
				return // refused, which is the correct outcome for a loser
			}
			mu.Lock()
			held++
			releases = append(releases, release)
			mu.Unlock()
		}()
	}
	start.Done()
	done.Wait()

	if held != 1 {
		t.Errorf("%d of %d concurrent acquisitions succeeded, want exactly 1.\n"+
			"Two generations writing one tree produce redeclaration errors between two "+
			"individually correct plans — which is the failure this lock exists to prevent.",
			held, racers)
	}
	for _, r := range releases {
		r()
	}

	// And after the holder releases, the lock is takeable again.
	release, err := AcquireTargetLock(root, "orchestrator")
	if err != nil {
		t.Fatalf("lock not reusable after release: %v", err)
	}
	release()
}
