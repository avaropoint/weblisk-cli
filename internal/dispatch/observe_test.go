package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Zero and unmeasured must not render the same.
//
// "quiet for 0 seconds" means the model just spoke; absent means nobody looked.
// A plain float64 with omitempty collapses them, which is the same trap as
// omitempty on a time.Time — it never omits and serialises as year one.
func TestZeroIdleIsNotTheSameAsUnmeasured(t *testing.T) {
	measured := stateFromActivity(ProviderActivity{IdleFor: 0, Elapsed: 3 * time.Second, Events: 2})
	if measured.IdleSeconds == nil {
		t.Fatal("a measured zero was dropped")
	}
	b, err := json.Marshal(measured)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "idle_seconds") {
		t.Errorf("a measured zero does not survive JSON: %s", b)
	}

	// File progress has no liveness reading, and must not claim one.
	fromFile := stateFromProgress(Progress{Step: 3, Total: 29, Path: "a.go", Status: "generating"})
	if fromFile.IdleSeconds != nil {
		t.Error("file progress invented an idle reading")
	}
	b, _ = json.Marshal(fromFile)
	if strings.Contains(string(b), "idle_seconds") {
		t.Errorf("file progress serialised an idle field: %s", b)
	}
}

// The observer must actually be called by the code that reports progress. A
// hook nothing invokes is the fault this repository has shipped before.
func TestPrintProgressReachesTheObserver(t *testing.T) {
	var got []BuildState
	restore := SetBuildObserver(&BuildObserver{File: func(s BuildState) { got = append(got, s) }})
	defer restore()

	printProgress(Progress{Step: 7, Total: 29, Path: "internal/x.go", Status: "generating"})
	if len(got) != 1 {
		t.Fatalf("observer saw %d state(s), want 1 — printProgress does not report structure", len(got))
	}
	if got[0].Index != 7 || got[0].Total != 29 || got[0].File != "internal/x.go" {
		t.Errorf("state lost the position: %+v", got[0])
	}
}

// And the provider's activity callback must reach it too, or a console still
// cannot tell alive from stalled.
func TestActivityReachesTheObserver(t *testing.T) {
	var got []BuildState
	restore := SetBuildObserver(&BuildObserver{Activity: func(s BuildState) { got = append(got, s) }})
	defer restore()

	observeActivity(ProviderActivity{IdleFor: 42 * time.Second, Elapsed: 5 * time.Minute, Events: 11, Phase: "assistant"})
	if len(got) != 1 {
		t.Fatalf("observer saw %d state(s), want 1", len(got))
	}
	if got[0].IdleSeconds == nil || *got[0].IdleSeconds != 42 {
		t.Errorf("idle not carried: %+v", got[0])
	}
	if got[0].Phase != "assistant" {
		t.Errorf("phase not carried: %q", got[0].Phase)
	}
}

// Removing the observer must actually remove it. A build that finishes and
// leaves a callback installed sends the next build's state to the last build's
// console.
func TestTheObserverIsRemovable(t *testing.T) {
	var n int
	restore := SetBuildObserver(&BuildObserver{File: func(BuildState) { n++ }})
	printProgress(Progress{Status: "generating", Path: "a", Step: 1, Total: 1})
	restore()
	printProgress(Progress{Status: "generating", Path: "b", Step: 1, Total: 1})
	if n != 1 {
		t.Errorf("observer fired %d time(s); it must stop after restore", n)
	}
}

// Reporting must not panic when nobody is observing, which is every terminal run.
func TestNoObserverIsSafe(t *testing.T) {
	activeObserver = nil
	printProgress(Progress{Status: "written", Path: "a.go", Step: 1, Total: 1})
	observeActivity(ProviderActivity{})
	// An observer with only one callback set must not be assumed to have both.
	restore := SetBuildObserver(&BuildObserver{})
	defer restore()
	printProgress(Progress{Status: "written", Path: "a.go", Step: 1, Total: 1})
	observeActivity(ProviderActivity{})
}

// Describe must say the thing a person needs, in both shapes.
func TestDescribeSaysWhatIsHappening(t *testing.T) {
	idle := 95.0
	elapsed := 400.0
	live := BuildState{IdleSeconds: &idle, ElapsedSeconds: &elapsed, Events: 30}
	if d := live.Describe(); !strings.Contains(d, "quiet for") || !strings.Contains(d, "30 event") {
		t.Errorf("liveness not described: %q", d)
	}
	file := BuildState{Index: 7, Total: 29, File: "a.go", Status: "generating"}
	if d := file.Describe(); !strings.Contains(d, "7/29") || !strings.Contains(d, "a.go") {
		t.Errorf("position not described: %q", d)
	}
}

// The provider a build actually uses must be wired to the observer.
//
// The tests above prove the observer works when something calls it. They do not
// prove that the claude-code provider — the one every build uses — is
// constructed with the callback attached. Removing that single line leaves
// every test above passing and Studio with no liveness at all, which is the
// shape of a guard on a helper nothing calls.
func TestTheClaudeCodeProviderIsWiredToTheObserver(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WL_AI_COMMAND", fake)

	p, err := newLocalCLIProvider("claude-code", "")
	if err != nil {
		t.Fatalf("could not construct the provider: %v", err)
	}
	lp, ok := p.(*LocalCLIProvider)
	if !ok {
		t.Fatalf("provider is %T", p)
	}
	if !lp.Stream {
		t.Error("claude-code is not streamed, so liveness cannot be observed at all")
	}
	if lp.OnActivity == nil {
		t.Fatal("the provider has no activity callback — a build reports position but never liveness, " +
			"and a console is back to showing a spinner")
	}

	// And the callback must be the one that reaches the observer, not some
	// other function that merely satisfies the field.
	var seen int
	restore := SetBuildObserver(&BuildObserver{Activity: func(BuildState) { seen++ }})
	defer restore()
	lp.OnActivity(ProviderActivity{Events: 1})
	if seen != 1 {
		t.Error("the provider's callback does not reach the build observer")
	}
}
