package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestErrorsAreGroupedByTheFileTheyBlame(t *testing.T) {
	out := `# weblisk-server
./identity.go:22:2: no required module provides package github.com/cloudflare/circl
./events.go:1:1: expected 'package', found '--'
./identity.go:465:6: writeCanonical redeclared in this block
some general linker complaint`
	byFile := ErrorsByFile(out)
	if len(byFile["identity.go"]) != 2 {
		t.Errorf("identity.go got %d errors, want 2: %v", len(byFile["identity.go"]), byFile["identity.go"])
	}
	if len(byFile["events.go"]) != 1 {
		t.Errorf("events.go got %d errors, want 1", len(byFile["events.go"]))
	}
	// Errors naming no file must still reach somebody.
	if len(byFile[""]) == 0 {
		t.Error("an error naming no file was dropped")
	}
}

func TestMostBlamedFileIsRepairedFirst(t *testing.T) {
	// A file with many errors is usually the cause and the others its symptoms.
	plan := &Plan{Files: []PlannedFile{{Path: "a.go"}, {Path: "b.go"}}}
	byFile := map[string][]string{
		"a.go": {"one"},
		"b.go": {"one", "two", "three"},
	}
	order := filesToRepair(byFile, plan)
	if len(order) != 2 || order[0] != "b.go" {
		t.Errorf("repair order = %v, want b.go first", order)
	}
}

func TestFilesOutsideThePlanAreNotRepaired(t *testing.T) {
	// A build may blame a vendored or toolchain file. Regenerating it is not
	// this pipeline's business.
	plan := &Plan{Files: []PlannedFile{{Path: "a.go"}}}
	order := filesToRepair(map[string][]string{"a.go": {"x"}, "/usr/lib/go/thing.go": {"y"}}, plan)
	if len(order) != 1 || order[0] != "a.go" {
		t.Errorf("repair order = %v, want only a.go", order)
	}
}

func TestASucceedingBuildIsNotRepaired(t *testing.T) {
	dir := t.TempDir()
	p := &fakeProvider{}
	plan := &Plan{Root: ".", Build: "true", Files: []PlannedFile{{Path: "a.go", Purpose: "x"}}}
	result, _, err := BuildAndRepair(p, plan, dir, "platform", []GeneratedFile{{Path: "a.go", Content: "package main\n"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Error("a succeeding build was reported as failed")
	}
	if p.calls != 0 {
		t.Errorf("made %d model calls for a build that succeeded", p.calls)
	}
}

// TestABuildErrorIsFedBackAndFixed is the loop this file exists for: the model
// never saw a compiler error before, and wrote eleven files blind.
func TestABuildErrorIsFedBackAndFixed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := "package main\n\nfunc main() { undefinedCall() }\n"
	fixed := "package main\n\nfunc main() {}\n"
	p := &fakeProvider{responses: []string{fixed}}
	plan := &Plan{Root: ".", Build: "go build ./...", Files: []PlannedFile{{Path: "main.go", Purpose: "entry"}}}

	var repaired, errSeen string
	result, files, err := BuildAndRepair(p, plan, dir,
		"platform", []GeneratedFile{{Path: "main.go", Content: broken}},
		func(pr Progress) {
			if pr.Status == "repairing" {
				repaired, errSeen = pr.Path, pr.Detail
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if repaired != "main.go" {
		t.Errorf("repaired %q, want main.go", repaired)
	}
	if !strings.Contains(errSeen, "undefined") {
		t.Errorf("the repair was not told what the compiler said: %q", errSeen)
	}
	if !result.OK {
		t.Errorf("the build did not recover: %s", result.Output)
	}
	if len(files) != 1 || strings.Contains(files[0].Content, "undefinedCall") {
		t.Error("the repaired content was not returned")
	}
	// The model must have been given the actual error text.
	if len(p.prompts) == 0 || !strings.Contains(p.prompts[0], "undefined") {
		t.Error("the compiler error was not included in the repair prompt")
	}
}

func TestRepairIsBoundedAndReportsHonestly(t *testing.T) {
	// A target the model cannot fix must fail visibly rather than loop.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := "package main\n\nfunc main() { stillUndefined() }\n"
	p := &fakeProvider{responses: []string{broken, broken, broken, broken}}
	plan := &Plan{Root: ".", Build: "go build ./...", Files: []PlannedFile{{Path: "main.go", Purpose: "entry"}}}
	result, _, err := BuildAndRepair(p, plan, dir, "platform",
		[]GeneratedFile{{Path: "main.go", Content: broken}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Error("an unfixable build was reported as succeeding")
	}
	if p.calls > maxRepairRounds {
		t.Errorf("made %d calls, exceeding %d rounds", p.calls, maxRepairRounds)
	}
}

func TestAnEmptyBuildCommandIsNotRun(t *testing.T) {
	if r := RunBuild(t.TempDir(), "   "); !r.OK {
		t.Error("an absent build command was treated as a failure")
	}
}
