package tenant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Validation happens before anything is created, so a bad request leaves no
// half-made directory behind for somebody to find later and wonder about.
func TestABadRequestCreatesNothing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "should-not-exist")
	for _, tc := range []struct {
		name string
		spec Spec
		want string
	}{
		{"no directory", Spec{Operator: "lloyd", Passphrase: "long-enough-passphrase"}, "directory"},
		{"no operator", Spec{Root: root, Passphrase: "long-enough-passphrase"}, "operator name is required"},
		{"short passphrase", Spec{Root: root, Operator: "lloyd", Passphrase: "short"}, "at least 12"},
		{"no passphrase", Spec{Root: root, Operator: "lloyd"}, "at least 12"},
	} {
		ch, err := Create(context.Background(), tc.spec)
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v does not mention %q", tc.name, err, tc.want)
		}
		if ch != nil {
			t.Errorf("%s: returned a channel for a request it rejected", tc.name)
		}
		if _, statErr := os.Stat(root); statErr == nil {
			t.Errorf("%s: created %s anyway", tc.name, root)
		}
	}
}

// The passphrase must not be recoverable from anything this package emits.
//
// Progress objects are printed to a terminal, serialised into a Studio console,
// and written into build logs. A passphrase in one of those is a passphrase in
// all three.
func TestNoProgressMessageCanCarryThePassphrase(t *testing.T) {
	const secret = "correct-horse-battery-staple"
	dir := t.TempDir()
	// A directory that already holds a tenant, so the run stops at the
	// directory step without needing a model — the failure path is the one most
	// likely to interpolate the spec into a message.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := Create(context.Background(), Spec{
		Root: dir, Operator: "lloyd", Passphrase: secret, Provider: "claude-code",
	})
	if err != nil {
		t.Fatal(err)
	}
	for p := range ch {
		blob := p.Message + " " + p.Err
		if strings.Contains(blob, secret) {
			t.Fatalf("step %s carries the passphrase: %q", p.Step, blob)
		}
	}
}

// A directory that already holds a tenant is refused, not merged into.
func TestAnOccupiedDirectoryIsRefusedUnlessResuming(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := Create(context.Background(), Spec{
		Root: dir, Operator: "lloyd", Passphrase: "long-enough-passphrase", Provider: "claude-code",
	})
	if err != nil {
		t.Fatal(err)
	}
	var stoppedAt Step
	var msg string
	for p := range ch {
		if p.Err != "" {
			stoppedAt, msg = p.Step, p.Err
		}
	}
	if stoppedAt != StepDirectory {
		t.Fatalf("stopped at %q, want the directory step (message: %s)", stoppedAt, msg)
	}
	if !strings.Contains(msg, "already contains generated code") || !strings.Contains(msg, "go.mod") {
		t.Errorf("the refusal does not say what is in the way: %s", msg)
	}
	if !strings.Contains(msg, "Resume") {
		t.Errorf("the refusal does not say how to continue: %s", msg)
	}
}

// A cancelled context ends the operation rather than leaking the goroutine.
func TestCancellingEndsTheOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := Create(ctx, Spec{
		Root: dir, Operator: "lloyd", Passphrase: "long-enough-passphrase", Provider: "claude-code",
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	// Draining must terminate. Before the ctx.Done() arms on every send, a
	// caller that stopped reading would have wedged the goroutine forever.
	for range ch {
	}
}

// The steps are a contract: two front ends render them, so a rename breaks both
// at once and must be a deliberate act.
func TestTheStepNamesAreStable(t *testing.T) {
	for step, want := range map[Step]string{
		StepProvider:  "provider",
		StepDirectory: "directory",
		StepGenerate:  "generate",
		StepSkills:    "skills",
		StepProvision: "provision",
		StepDone:      "done",
	} {
		if string(step) != want {
			t.Errorf("step %q renamed to %q — both front ends render these", want, step)
		}
	}
}
