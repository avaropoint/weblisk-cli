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
	rec, err := ReconcileTarget(root, secondPlan, nil)
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
	rec, err := ReconcileTarget(root, &Plan{Root: "server", Files: []PlannedFile{{Path: "main.go"}}}, nil)
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
	rec, err := ReconcileTarget(root, &Plan{Root: "server", Files: []PlannedFile{{Path: "main.go"}}}, nil)
	if err != nil {
		t.Fatalf("a first run must not fail reconciliation: %v", err)
	}
	if len(rec.Stale) != 0 || len(rec.Foreign) != 0 {
		t.Errorf("unexpected reconciliation on a fresh target: %+v", rec)
	}
}

// A file the planner was TOLD to omit is not stale.
//
// Two correct rules contradicted each other. FormatTenantState tells a planner
// "do NOT plan go.mod — it exists, and renaming the module breaks every package
// in the tenant". The planner complied. Reconcile then read the omission as a
// deliberate drop and deleted go.mod from a working tenant, because nothing
// carried the fact that the omission had been requested.
func TestAnInstructedOmissionIsNotStale(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".")
	for _, f := range []string{"go.mod", "old_registry.go", "main.go"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	first := &Plan{Target: "orchestrator", Root: ".", Files: []PlannedFile{
		{Path: "go.mod"}, {Path: "old_registry.go"}, {Path: "main.go"},
	}}
	RecordWritten(root, first, []GeneratedFile{
		{Path: "go.mod"}, {Path: "old_registry.go"}, {Path: "main.go"},
	})

	// The new plan omits go.mod because it was instructed to, and drops
	// old_registry.go because it genuinely re-decomposed.
	second := &Plan{Target: "orchestrator", Root: ".", Files: []PlannedFile{{Path: "main.go"}}}
	st := &TenantState{Module: "hubgen", Owned: map[string]string{}}

	rec, err := ReconcileTarget(root, second, st)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatalf("go.mod was deleted from a working tenant: %v", err)
	}
	if len(rec.Retained) != 1 || rec.Retained[0] != "go.mod" {
		t.Errorf("Retained = %v, want [go.mod]", rec.Retained)
	}
	if len(rec.Stale) != 1 || rec.Stale[0] != "old_registry.go" {
		t.Errorf("Stale = %v, want [old_registry.go] — a real re-decomposition must still be cleaned", rec.Stale)
	}
	if _, err := os.Stat(filepath.Join(dir, "old_registry.go")); err == nil {
		t.Error("a genuinely stale file survived; protection is now too broad")
	}
}

// Reconciliation must reach a nested file, and must still refuse to touch one
// it cannot prove it wrote.
//
// It called os.ReadDir on plan.Root and skipped every directory entry.
// plan.Root is "." — the tenant IS the module root — so it examined go.mod and
// nothing else, and no stale file has been removed since platforms/go
// specified cmd/ and internal/ packages. It surfaced as eleven redeclaration
// errors: one plan named a file auditlog.go, the next named it audit.go, both
// existed, and the package declared Auditor twice.
func TestReconcileReachesNestedFiles(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{
		"internal/orchestrator/audit.go",       // planned — kept
		"internal/orchestrator/auditlog.go",    // ours, dropped by this plan — removed
		"internal/orchestrator/handwritten.go", // never ours — left alone
		"cmd/orchestrator/main.go",             // planned — kept
		"bin/orchestrator",                     // build output — never considered
	} {
		write(f)
	}

	plan := &Plan{Target: "orchestrator", Root: ".", Files: []PlannedFile{
		{Path: "internal/orchestrator/audit.go"},
		{Path: "cmd/orchestrator/main.go"},
	}}
	// The previous run owned the file this plan drops.
	RecordWritten(root, &Plan{Target: "orchestrator", Root: "."}, []GeneratedFile{
		{Path: "internal/orchestrator/audit.go"},
		{Path: "internal/orchestrator/auditlog.go"},
		{Path: "cmd/orchestrator/main.go"},
	})

	rec, err := ReconcileTarget(root, plan, &TenantState{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsStr(rec.Stale, "internal/orchestrator/auditlog.go") {
		t.Fatalf("the nested stale file was not removed: stale=%v foreign=%v", rec.Stale, rec.Foreign)
	}
	if _, err := os.Stat(filepath.Join(root, "internal/orchestrator/auditlog.go")); err == nil {
		t.Fatal("reported stale but still on disk")
	}
	// The safety property. Widening the walk must not widen what is deleted.
	if _, err := os.Stat(filepath.Join(root, "internal/orchestrator/handwritten.go")); err != nil {
		t.Fatal("a file generation never wrote was deleted")
	}
	if !containsStr(rec.Foreign, "internal/orchestrator/handwritten.go") {
		t.Errorf("the unowned file was not reported: %v", rec.Foreign)
	}
	// Build output is not part of the source tree and is never walked.
	if _, err := os.Stat(filepath.Join(root, "bin/orchestrator")); err != nil {
		t.Error("bin/ was reconciled; it is build output")
	}
	for _, f := range []string{"internal/orchestrator/audit.go", "cmd/orchestrator/main.go"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err != nil {
			t.Errorf("%s is in the plan and was removed", f)
		}
	}
}

// A file this target wrote stays this target's until it is removed. Dropping it
// from the record while it is still on disk makes it indistinguishable from a
// hand-written file, and it can then never be cleaned up.
func TestOwnershipSurvivesAPlanThatDropsAFile(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "internal", "x", "old.go")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := &Plan{Target: "orchestrator", Root: "."}
	RecordWritten(root, plan, []GeneratedFile{{Path: "internal/x/old.go"}})

	// A later run writes a different file and does not mention old.go — which
	// is still on disk, because the run was interrupted before reconcile.
	RecordWritten(root, plan, []GeneratedFile{{Path: "internal/x/new.go"}})

	if got := PriorPaths(root, "orchestrator"); !containsStr(got, "internal/x/old.go") {
		t.Fatalf("ownership of a still-present file was forgotten: %v", got)
	}

	// And a file that is gone is NOT carried forward, so the record cannot grow
	// without bound as a tenant is restructured.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	RecordWritten(root, plan, []GeneratedFile{{Path: "internal/x/new.go"}})
	if got := PriorPaths(root, "orchestrator"); containsStr(got, "internal/x/old.go") {
		t.Fatalf("a deleted file is still claimed: %v", got)
	}
}
