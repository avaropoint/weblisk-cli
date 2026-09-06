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
	// A provider that cannot resolve, so this runs identically everywhere. The
	// failure path is the one most likely to interpolate the spec into a
	// message, which is what this test is about.
	ch, err := Create(context.Background(), Spec{
		Root: dir, Operator: "lloyd", Passphrase: secret, Provider: "no-such-provider",
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
//
// Tested against checkDirectoryFree rather than by driving Create, because
// Create settles the PROVIDER first and this rule is not about providers. The
// version that drove the whole flow with Provider "claude-code" passed here and
// failed the first time CI ran the tests, on a runner with no claude binary:
// it stopped at the provider step and never reached the rule under test.
func TestAnOccupiedDirectoryIsRefusedUnlessResuming(t *testing.T) {
	dir := t.TempDir()
	if err := checkDirectoryFree(dir, false); err != nil {
		t.Fatalf("an empty directory was refused: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := checkDirectoryFree(dir, false)
	if err == nil {
		t.Fatal("a directory holding a tenant was accepted")
	}
	msg := err.Error()
	if !strings.Contains(msg, "already contains generated code") || !strings.Contains(msg, "go.mod") {
		t.Errorf("the refusal does not say what is in the way: %s", msg)
	}
	if !strings.Contains(msg, "Resume") {
		t.Errorf("the refusal does not say how to continue: %s", msg)
	}
	// Resuming is the documented way through, so it must actually pass.
	if err := checkDirectoryFree(dir, true); err != nil {
		t.Errorf("Resume was refused: %v", err)
	}
	// And "is there a tenant here" is answered by what exists, not by one name.
	for _, marker := range []string{"cmd", "internal", "wrangler.toml", "package.json", "Cargo.toml"} {
		fresh := t.TempDir()
		if err := os.MkdirAll(filepath.Join(fresh, marker), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := checkDirectoryFree(fresh, false); err == nil {
			t.Errorf("%s did not count as generated code", marker)
		}
	}
}

// The provider is settled BEFORE any work, and a provider that cannot run stops
// the operation there.
//
// Deterministic in every environment: the name is one this pipeline drives and
// that cannot be available without a key, so the outcome does not depend on
// what happens to be installed on the machine running the test.
func TestTheProviderIsSettledBeforeAnythingElse(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("WL_AI_KEY", "")

	dir := t.TempDir()
	ch, err := Create(t.Context(), Spec{
		Root: dir, Operator: "lloyd", Passphrase: "long-enough-passphrase", Provider: "anthropic",
	})
	if err != nil {
		t.Fatal(err)
	}
	var steps []Step
	var failedAt Step
	for p := range ch {
		steps = append(steps, p.Step)
		if p.Err != "" {
			failedAt = p.Step
		}
	}
	if failedAt != StepProvider {
		t.Fatalf("failed at %q; the provider must be settled before any work (steps: %v)", failedAt, steps)
	}
	// Nothing later ran, and nothing was created.
	for _, s := range steps {
		if s == StepGenerate || s == StepProvision {
			t.Errorf("%s ran after the provider could not be resolved", s)
		}
	}
	if entries, rerr := os.ReadDir(dir); rerr == nil && len(entries) != 0 {
		t.Errorf("the directory was written to before a provider was settled: %v", entries)
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
		Root: dir, Operator: "lloyd", Passphrase: "long-enough-passphrase", Provider: "no-such-provider",
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
