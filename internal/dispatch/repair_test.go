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
// the checklist was a report, and the compiler was the contract.
//
// Verification is now the model reading the assertions verbatim against its own
// output, rather than structural checks written in Go. The exit condition is
// unchanged; the authority moved back to the blueprint.
func TestCompilingIsNotConforming(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module hub\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	before := "package main\n\ntype ErrorResponse struct {\n\tError string `json:\"error\"`\n}\n\nfunc main() {}\n"
	after := "package main\n\ntype ErrorResponse struct {\n\tError string `json:\"error\"`\n\tCode  string `json:\"code,omitempty\"`\n}\n\nfunc main() {}\n"

	plan := &Plan{Root: ".", Build: "go build ./...",
		Files: []PlannedFile{{Path: "main.go", Purpose: "protocol types"}}}
	checklist := []ChecklistItem{{Source: "protocol/types.md",
		Text: "ErrorResponse includes `error` and `code` fields with exact JSON keys"}}

	p := &fakeProvider{responses: []string{
		`[{"index":1,"holds":"no","file":"main.go","evidence":"ErrorResponse has no code field"}]`,
		after,
		`[{"index":1,"holds":"yes","file":"main.go","evidence":"Code string with a json code tag"}]`,
	}}
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
		t.Fatal("the build passed and an unmet assertion did not start a repair round")
	}
	if !strings.Contains(files[0].Content, `json:"code`) {
		t.Errorf("the conformance repair was not applied:\n%s", files[0].Content)
	}
	// Verify, repair, verify. It stops once the assertion holds.
	if p.calls != 3 {
		t.Errorf("provider called %d times, want 3 — the loop did not stop when the assertion held", p.calls)
	}
}

func TestConformanceRepairStopsWhenItIsNotConverging(t *testing.T) {
	// A model that keeps reporting the same assertion unmet, and keeps returning a
	// file that does not fix it, must not spend twelve rounds proving it.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module hub\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stuck := "package main\n\nfunc main() {}\n"
	plan := &Plan{Root: ".", Build: "go build ./...",
		Files: []PlannedFile{{Path: "main.go", Purpose: "protocol types"}}}
	checklist := []ChecklistItem{{Source: "protocol/types.md",
		Text: "ErrorResponse includes `error` and `code` fields with exact JSON keys"}}

	unmet := `[{"index":1,"holds":"no","file":"main.go","evidence":"ErrorResponse is absent"}]`
	p := &fakeProvider{responses: []string{unmet, stuck, unmet, stuck, unmet, stuck, unmet, stuck}}
	result, _, err := BuildAndRepair(p, plan, dir, "", []GeneratedFile{{Path: "main.go", Content: stuck}},
		nil, checklist, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("build failed: %s", result.Output)
	}
	// Rounds 1 and 2 report the same count, so the stall trips by round 3.
	if p.calls > 5 {
		t.Errorf("provider called %d times — the stall detector did not trip", p.calls)
	}
}

func TestAnAssertionNamingNoPlannedFileIsNotRepairedBlindly(t *testing.T) {
	// A conformance repair aimed at the wrong file edits correct code to satisfy
	// something it does not control. The model's file name is used as given and
	// never corrected to a plausible one.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module hub\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "package main\n\nfunc main() {}\n"
	plan := &Plan{Root: ".", Build: "go build ./...", Files: []PlannedFile{{Path: "main.go"}}}
	checklist := []ChecklistItem{{Source: "architecture/storage.md", Text: "All stores survive process restart"}}

	p := &fakeProvider{responses: []string{
		`[{"index":1,"holds":"no","file":"storage.go","evidence":"no store exists"}]`,
	}}
	result, _, err := BuildAndRepair(p, plan, dir, "", []GeneratedFile{{Path: "main.go", Content: src}}, nil, checklist, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("build failed: %s", result.Output)
	}
	// One call — the verification. No repair: storage.go is not in the plan.
	if p.calls != 1 {
		t.Errorf("provider called %d times; want 1 — a file outside the plan was repaired", p.calls)
	}
}

func TestAYesWithoutEvidenceIsNotAPass(t *testing.T) {
	// A model asked "did you satisfy this?" will tend to say yes. Requiring the
	// construct that satisfies it makes agreement cost something.
	v, err := ParseVerdicts(`[{"index":1,"holds":"yes","file":"a.go","evidence":""}]`, 1)
	if err != nil {
		t.Fatal(err)
	}
	if v[0].Holds != "unverifiable" {
		t.Errorf("holds = %q, want unverifiable — a yes with no evidence is an opinion", v[0].Holds)
	}
	// And an unmet verdict with no fault named cannot be repaired against.
	v, err = ParseVerdicts(`[{"index":1,"holds":"no","file":"a.go","evidence":"  "}]`, 1)
	if err != nil {
		t.Fatal(err)
	}
	if v[0].Holds != "unverifiable" {
		t.Errorf("holds = %q, want unverifiable", v[0].Holds)
	}
}

func TestUnansweredAssertionsAreNotUnverifiable(t *testing.T) {
	// A model returning one verdict for five assertions has not judged four of
	// them. That is a gap in the verification, not a finding about the assertions,
	// and folding the two reports an incomplete check as a complete one.
	v, err := ParseVerdicts(`[{"index":1,"holds":"yes","file":"a.go","evidence":"present"}]`, 5)
	if err != nil {
		t.Fatal(err)
	}
	yes, no, unverifiable, unanswered := VerdictSummary(v, 5)
	if yes != 1 || no != 0 || unverifiable != 0 || unanswered != 4 {
		t.Errorf("summary = %d/%d/%d/%d, want 1/0/0/4", yes, no, unverifiable, unanswered)
	}
}

func TestAVerdictForAnAssertionThatDoesNotExistIsDropped(t *testing.T) {
	v, err := ParseVerdicts(`[{"index":1,"holds":"yes","file":"a.go","evidence":"ok"},`+
		`{"index":9,"holds":"no","file":"b.go","evidence":"x"},`+
		`{"index":1,"holds":"no","file":"c.go","evidence":"dup"}]`, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 1 || v[0].Index != 1 || v[0].Holds != "yes" {
		t.Errorf("verdicts = %+v, want only the first entry for index 1", v)
	}
}

func TestAWrongImportPathIsRepairedNotFatal(t *testing.T) {
	// The fault: 53 files generated correctly, and the run ended on one of them
	// importing github.com/cloudflare/circl/sign/mldsa65 — plausible, and wrong;
	// the package is under sign/mldsa/mldsa65. `go mod tidy` refused with
	// "module found, but does not contain package", which names a PACKAGE and no
	// file, so nothing could be repaired and 52 correct files were discarded.
	output := `go: finding module for package github.com/cloudflare/circl/sign/mldsa65
go: weblisk/internal/identity imports
	github.com/cloudflare/circl/sign/mldsa65: module github.com/cloudflare/circl@latest found (v1.6.5), but does not contain package github.com/cloudflare/circl/sign/mldsa65`

	content := map[string]string{
		"internal/identity/keys.go":  "package identity\n\nimport \"github.com/cloudflare/circl/sign/mldsa65\"\n",
		"internal/identity/token.go": "package identity\n\nimport \"github.com/cloudflare/circl/sign/mldsa65\"\n",
		"internal/protocol/types.go": "package protocol\n\nimport \"encoding/json\"\n",
		"internal/storage/jsonl.go":  "package storage\n\nimport \"os\"\n",
	}
	got := filesWithBadImports(output, content)
	if len(got) != 2 {
		t.Fatalf("found %v; want the two files importing the missing package", got)
	}
	for _, want := range []string{"internal/identity/keys.go", "internal/identity/token.go"} {
		if !containsString(got, want) {
			t.Errorf("%s imports the missing package and was not found", want)
		}
	}
	// Files that do not import it must be left alone — a repair aimed at a
	// correct file is how a fix becomes a regression.
	for _, never := range []string{"internal/protocol/types.go", "internal/storage/jsonl.go"} {
		if containsString(got, never) {
			t.Errorf("%s does not import the missing package and was targeted", never)
		}
	}
}

func TestAResolverFailureNobodyCanFixIsStillFatal(t *testing.T) {
	// Most resolution failures are not source faults. A network error or a
	// missing toolchain is not something a model can repair, and pretending
	// otherwise spends calls to produce the same failure.
	for _, output := range []string{
		"go: github.com/x/y@v1.0.0: dial tcp: lookup proxy.golang.org: no such host",
		"go: updates to go.mod needed; to update it:\n\tgo mod tidy",
		"go: go.mod file not found in current directory or any parent directory",
	} {
		if got := filesWithBadImports(output, map[string]string{"a.go": "package main\n"}); len(got) != 0 {
			t.Errorf("a non-source resolver failure was treated as repairable: %q -> %v", output, got)
		}
	}
}

func TestTheImportRepairAsksForNothingElse(t *testing.T) {
	// A wrong import is a one-token fault. Inviting a rewrite of a file that is
	// otherwise correct is how a repair becomes a regression.
	p := importRepairPrompt(
		PlannedFile{Path: "internal/identity/keys.go", Purpose: "ML-DSA-65 keys"},
		"package identity\n\nfunc Generate() {}\n",
		"does not contain package github.com/cloudflare/circl/sign/mldsa65",
		"PLATFORM", "avaropoint")
	for _, want := range []string{"NOTHING else changed", "Primitive Mapping", "does not contain package", "func Generate()", "Module path: avaropoint"} {
		if !strings.Contains(p, want) {
			t.Errorf("the import-repair prompt is missing %q", want)
		}
	}
}
