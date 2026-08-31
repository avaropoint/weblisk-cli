package dispatch

// A plan is a complete statement of the target, not an addition to it.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResumeRemovesTheLastPlansLeftovers(t *testing.T) {
	// The fault, exactly: run one planned routing.go, run three planned
	// registry.go, and resume wrote the second beside the first. Every symbol was
	// declared twice, and no repair could fix it because no file was wrong.
	root := t.TempDir()
	dir := filepath.Join(root, "server")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main\n")
	write("routing.go", "package main\n\ntype Registry struct{}\n")
	write("notes.md", "somebody's own file\n")

	firstPlan := &Plan{Root: "server", Files: []PlannedFile{{Path: "main.go"}, {Path: "routing.go"}}}
	RecordWritten(root, firstPlan, []GeneratedFile{{Path: "main.go"}, {Path: "routing.go"}})

	// The new plan splits the registry differently.
	secondPlan := &Plan{Root: "server", Files: []PlannedFile{{Path: "main.go"}, {Path: "registry.go"}}}
	rec, err := ReconcileTarget(root, secondPlan)
	if err != nil {
		t.Fatal(err)
	}

	if len(rec.Stale) != 1 || rec.Stale[0] != "routing.go" {
		t.Errorf("stale = %v, want [routing.go]", rec.Stale)
	}
	if _, err := os.Stat(filepath.Join(dir, "routing.go")); !os.IsNotExist(err) {
		t.Error("routing.go survived — the duplicate declarations are back")
	}
	// A file still in the plan is untouched.
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		t.Error("main.go was removed, and it is in the plan")
	}
	// A file generation never wrote is reported and LEFT. Deleting somebody's own
	// work is a far worse mistake than the one being fixed.
	if len(rec.Foreign) != 1 || rec.Foreign[0] != "notes.md" {
		t.Errorf("foreign = %v, want [notes.md]", rec.Foreign)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.md")); err != nil {
		t.Error("a hand-written file was deleted")
	}
}

func TestWithoutARecordNothingIsDeleted(t *testing.T) {
	// A first run, or a lost manifest. Everything present is foreign, and
	// reconciliation removes none of it — a cache that cannot remember what it
	// wrote does not get to guess.
	root := t.TempDir()
	dir := filepath.Join(root, "server")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, err := ReconcileTarget(root, &Plan{Root: "server", Files: []PlannedFile{{Path: "main.go"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Stale) != 0 {
		t.Errorf("removed %v with no record of writing it", rec.Stale)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.go")); err != nil {
		t.Error("a file was deleted without a record that generation wrote it")
	}
}

func TestAMissingTargetIsNotAnError(t *testing.T) {
	// The ordinary first run: nothing to reconcile.
	root := t.TempDir()
	rec, err := ReconcileTarget(root, &Plan{Root: "server", Files: []PlannedFile{{Path: "main.go"}}})
	if err != nil {
		t.Fatalf("a first run must not fail reconciliation: %v", err)
	}
	if len(rec.Stale) != 0 || len(rec.Foreign) != 0 {
		t.Errorf("unexpected reconciliation on a fresh target: %+v", rec)
	}
}
