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
	result, _, err := BuildAndRepair(p, plan, dir, "platform", []GeneratedFile{{Path: "a.go", Content: "package main\n"}}, nil, nil, nil)
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
		}, nil, nil)
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
		[]GeneratedFile{{Path: "main.go", Content: broken}}, nil, nil, nil)
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

// TestTheBuildRunsFromTheProjectRoot reproduces a run that generated all twelve
// files and then failed on "cd: server: No such file or directory". Build
// commands come from a platform blueprint's Build and Run section and are
// written from the project root, not from inside the target.
func TestTheBuildRunsFromTheProjectRoot(t *testing.T) {
	root := t.TempDir()
	srv := filepath.Join(root, "server")
	if err := os.MkdirAll(srv, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srv, "go.mod"), []byte("module x\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := &Plan{Root: "server", Build: "cd server && go build ./...",
		Files: []PlannedFile{{Path: "main.go", Purpose: "entry"}}}
	p := &fakeProvider{}
	result, _, err := BuildAndRepair(p, plan, root, "platform",
		[]GeneratedFile{{Path: "main.go", Content: "package main\n\nfunc main() {}\n"}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("a root-relative build command failed: %s", result.Output)
	}
}

// TestCompilerPathsAreMatchedToPlanPaths — the build reports "server/main.go"
// and the plan says "main.go". A mismatch means the repair loop finds nothing to
// repair and gives up without saying why.
func TestCompilerPathsAreMatchedToPlanPaths(t *testing.T) {
	byFile := ErrorsByFileIn("./server/main.go:3:5: undefined: x", "server")
	if len(byFile["main.go"]) != 1 {
		t.Errorf("path not normalised to the plan root: %v", byFile)
	}
	// And a target rooted at "." must be left alone.
	flat := ErrorsByFileIn("./main.go:3:5: undefined: x", ".")
	if len(flat["main.go"]) != 1 {
		t.Errorf("a flat target was mangled: %v", flat)
	}
}

// TestPrepareRunsBeforeEveryBuild reproduces a round-two failure: repairs
// changed imports, go.mod needed resolving again, and prepare had run only once.
// The resulting error names no file, so the loop found nothing to repair and
// stopped on a fault no repair could have fixed.
func TestPrepareRunsBeforeEveryBuild(t *testing.T) {
	root := t.TempDir()
	counter := filepath.Join(root, "prepare-count")
	// Prepare appends a line each time. The build always fails AND blames a file
	// the plan owns, so the loop repairs and goes round again rather than taking
	// the "no file blamed" early exit.
	plan := &Plan{
		Root:    ".",
		Prepare: "echo x >> " + counter,
		Build:   "echo './a.go:1:1: forced failure' >&2; exit 1",
		Files:   []PlannedFile{{Path: "a.go", Purpose: "x"}},
	}
	p := &fakeProvider{responses: []string{
		"package main\n", "package main\n", "package main\n", "package main\n",
	}}
	if _, _, err := BuildAndRepair(p, plan, root, "platform",
		[]GeneratedFile{{Path: "a.go", Content: "package main\n"}}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("prepare never ran: %v", err)
	}
	runs := strings.Count(string(b), "x")
	if runs < 2 {
		t.Errorf("prepare ran %d time(s); it must run before every build round", runs)
	}
}

func TestAFailingPrepareStopsImmediately(t *testing.T) {
	// Dependency resolution is not repairable; burning rounds on it wastes the
	// budget that real errors need.
	root := t.TempDir()
	plan := &Plan{Root: ".", Prepare: "exit 1", Build: "true",
		Files: []PlannedFile{{Path: "a.go", Purpose: "x"}}}
	p := &fakeProvider{}
	_, _, err := BuildAndRepair(p, plan, root, "platform",
		[]GeneratedFile{{Path: "a.go", Content: "package main\n"}}, nil, nil, nil)
	if err == nil {
		t.Fatal("a failing prepare was not reported")
	}
	if !strings.Contains(err.Error(), "dependency resolution") {
		t.Errorf("the failure does not name the step: %v", err)
	}
	if p.calls != 0 {
		t.Errorf("made %d model calls for an unrepairable failure", p.calls)
	}
}

// TestRepairRetriesAContractViolation — repair used to give up on a file after
// one bad response, so a reply that came back as prose cost the whole round and
// the next round re-read the same unrepaired errors. Three files were lost that
// way in one run.
func TestRepairRetriesAContractViolation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := "package main\n\nfunc main() { undefinedCall() }\n"
	fixed := "package main\n\nfunc main() {}\n"
	// First reply is prose, second is the file.
	p := &fakeProvider{responses: []string{
		"I'll rewrite the file and verify it compiles first.",
		fixed,
	}}
	plan := &Plan{Root: ".", Build: "go build ./...",
		Files: []PlannedFile{{Path: "main.go", Purpose: "entry"}}}
	result, _, err := BuildAndRepair(p, plan, root, "platform",
		[]GeneratedFile{{Path: "main.go", Content: broken}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("the build did not recover after a prose reply: %s", result.Output)
	}
	if p.calls != 2 {
		t.Errorf("made %d calls; the rejected reply should have been retried within the round", p.calls)
	}
}

// TestRepairStopsWhenProgressStalls — a fixed round count was the wrong exit
// condition. It stopped runs that were still reducing errors and kept spending
// calls on ones that had stalled. The criterion is whether verification is
// getting closer.
func TestRepairStopsWhenProgressStalls(t *testing.T) {
	root := t.TempDir()
	// Build always reports the same two errors: no progress is possible.
	plan := &Plan{Root: ".", Build: "echo './a.go:1:1: x' >&2; echo './a.go:2:2: y' >&2; exit 1",
		Files: []PlannedFile{{Path: "a.go", Purpose: "x"}}}
	replies := make([]string, 40)
	for i := range replies {
		replies[i] = "package main\n"
	}
	p := &fakeProvider{responses: replies}
	var stallDetail string
	_, _, err := BuildAndRepair(p, plan, root, "platform",
		[]GeneratedFile{{Path: "a.go", Content: "package main\n"}},
		func(pr Progress) {
			if pr.Status == "failed" {
				stallDetail = pr.Detail
			}
		}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stallDetail, "no progress") {
		t.Errorf("a stalled repair was not reported as such: %q", stallDetail)
	}
	// It must give up well before the runaway ceiling.
	if p.calls > 6 {
		t.Errorf("made %d calls on a target with no possible progress", p.calls)
	}
}

// TestRepairKeepsGoingWhileErrorsFall — the previous fixed limit of three
// stopped runs that were converging.
func TestRepairKeepsGoingWhileErrorsFall(t *testing.T) {
	root := t.TempDir()
	counter := filepath.Join(root, "n")
	// Each build reports one fewer error than the last, reaching zero on the 5th.
	build := "n=$(cat " + counter + " 2>/dev/null || echo 5); " +
		"n=$((n-1)); echo $n > " + counter + "; " +
		"if [ $n -le 0 ]; then exit 0; fi; " +
		"i=0; while [ $i -lt $n ]; do echo \"./a.go:$i:1: err\" >&2; i=$((i+1)); done; exit 1"
	plan := &Plan{Root: ".", Build: build, Files: []PlannedFile{{Path: "a.go", Purpose: "x"}}}
	replies := make([]string, 40)
	for i := range replies {
		replies[i] = "package main\n"
	}
	p := &fakeProvider{responses: replies}
	result, _, err := BuildAndRepair(p, plan, root, "platform",
		[]GeneratedFile{{Path: "a.go", Content: "package main\n"}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("a converging repair was abandoned before it finished: %s", result.Output)
	}
}

// TestCompilingIsNotConforming is the loop's exit condition, corrected.
//
// The loop used to end the moment `go build` succeeded, and the caller then
// evaluated the checklist and printed it. So a hub that compiled and violated
// assertions from the very blueprints it was generated from reported success:
// the verification checklist was a report, and the compiler was the contract.
func TestCompilingIsNotConforming(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module hub\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Compiles, and the type is missing a JSON key the blueprint requires.
	before := "package main\n\ntype ErrorResponse struct {\n\tError string `json:\"error\"`\n}\n\nfunc main() {}\n"
	after := "package main\n\ntype ErrorResponse struct {\n\tError string `json:\"error\"`\n\tCode  string `json:\"code,omitempty\"`\n}\n\nfunc main() {}\n"

	plan := &Plan{Root: ".", Build: "go build ./...",
		Files: []PlannedFile{{Path: "main.go", Purpose: "protocol types"}}}
	checklist := []ChecklistItem{{Source: "protocol/types.md",
		Text: "ErrorResponse includes `error` and `code` fields with exact JSON keys"}}

	p := &fakeProvider{responses: []string{after}}
	var conformed []string
	result, files, err := BuildAndRepair(p, plan, dir, "", []GeneratedFile{{Path: "main.go", Content: before}},
		func(pr Progress) {
			if pr.Status == "repairing" {
				conformed = append(conformed, pr.Path)
			}
		}, checklist, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("build failed: %s", result.Output)
	}
	if len(conformed) == 0 {
		t.Fatal("the build passed and a failing assertion did not start a repair round")
	}
	if !strings.Contains(files[0].Content, `json:"code`) {
		t.Errorf("the conformance repair was not applied:\n%s", files[0].Content)
	}
	// And it stops: a second evaluation finds nothing failing, so no further call.
	if p.calls != 1 {
		t.Errorf("provider called %d times, want 1 — the loop did not stop when the assertion held", p.calls)
	}
}

func TestConformanceRepairStopsWhenItIsNotConverging(t *testing.T) {
	// A model that returns the same non-conforming file forever must not spend
	// twelve rounds proving it.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module hub\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stuck := "package main\n\ntype ErrorResponse struct {\n\tError string `json:\"error\"`\n}\n\nfunc main() {}\n"
	plan := &Plan{Root: ".", Build: "go build ./...",
		Files: []PlannedFile{{Path: "main.go", Purpose: "protocol types"}}}
	checklist := []ChecklistItem{{Source: "protocol/types.md",
		Text: "ErrorResponse includes `error` and `code` fields with exact JSON keys"}}

	p := &fakeProvider{responses: []string{stuck, stuck, stuck, stuck, stuck, stuck, stuck, stuck}}
	result, _, err := BuildAndRepair(p, plan, dir, "", []GeneratedFile{{Path: "main.go", Content: stuck}},
		nil, checklist, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("build failed: %s", result.Output)
	}
	// Rounds 1 and 2 see the same count, so the stall trips on round 3 at the
	// latest. Three calls, not twelve.
	if p.calls > 3 {
		t.Errorf("provider called %d times — the stall detector did not trip", p.calls)
	}
}

func TestAnAssertionNamingNoPlannedFileIsNotRepairedBlindly(t *testing.T) {
	// A conformance repair aimed at the wrong file edits correct code to satisfy
	// something it does not control. Reporting the failure is better.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module hub\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "package main\n\nfunc main() {}\n"
	plan := &Plan{Root: ".", Build: "go build ./...", Files: []PlannedFile{{Path: "main.go"}}}
	// Behavioural: nothing structural applies, so it is unchecked, not failed.
	checklist := []ChecklistItem{{Source: "architecture/storage.md", Text: "All stores survive process restart"}}

	p := &fakeProvider{}
	result, _, err := BuildAndRepair(p, plan, dir, "", []GeneratedFile{{Path: "main.go", Content: src}}, nil, checklist, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("build failed: %s", result.Output)
	}
	if p.calls != 0 {
		t.Errorf("provider called %d times for an unchecked assertion — the loop repaired against nothing", p.calls)
	}
}
