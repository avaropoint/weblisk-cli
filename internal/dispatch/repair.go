package dispatch

// Building the result, and feeding failures back.
//
// # Why generating blind does not work
//
// A generator asked a model for eleven files, checked each superficially, wrote
// them, and reported success. The result did not compile. Nothing in that loop
// ever told the model what the compiler said — it wrote every file without once
// seeing whether the previous one built.
//
// No developer works that way. They write, compile, read the error, fix. The
// pipeline denied the model the only feedback that actually establishes
// correctness, and then a human ran `go build` by hand and found two faults in
// thirty seconds.
//
// This is architecture/generation.md Layer 2's build step, plus the repair loop
// that makes it useful. A build failure is not a verdict; it is information the
// model has not been given yet.

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// buildTimeout bounds one build. Generous for a cold module cache, finite
// because a wedged toolchain would otherwise hang the run with no output.
const buildTimeout = 5 * time.Minute

// repairRoundCeiling is a runaway guard, not the exit condition.
//
// The loop ends when VERIFICATION passes — the build succeeds — or when it stops
// making progress. A fixed count was the wrong criterion: it stopped a run that
// was still reducing errors round on round, and it kept spending calls on one
// that had stalled. The verification checklist and the build say whether an
// artifact is complete; a number picked in the tooling says nothing.
//
// This ceiling exists only so a pathological case terminates.
const repairRoundCeiling = 12

// maxRepairRounds is retained for callers that reason about the budget.
const maxRepairRounds = repairRoundCeiling

// BuildResult is the outcome of running a target's build command.
type BuildResult struct {
	OK      bool
	Output  string
	Command string
}

// RunBuild executes the plan's build command.
//
// runDir is the PROJECT ROOT, not the target directory. Build commands come from
// a platform blueprint's Build and Run section and are written from the root —
// "cd server && go build -o orchestrator ." — so running them inside the target
// looks for server/server and fails on a cd before the compiler is ever invoked.
func RunBuild(runDir, command string) BuildResult {
	command = strings.TrimSpace(command)
	if command == "" {
		return BuildResult{OK: true, Command: ""}
	}
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = runDir
	out, err := cmd.CombinedOutput()
	return BuildResult{OK: err == nil, Output: string(out), Command: command}
}

// reBuildError matches "path.go:12:3: message" as emitted by Go and many other
// toolchains.
var reBuildError = regexp.MustCompile(`(?m)^\.?/?([\w./-]+\.\w+):(\d+):(?:(\d+):)?\s*(.+)$`)

// ErrorsByFile groups compiler output by the file it blames.
//
// Errors naming no file — a missing module, a linker failure — are returned
// under the empty key, because they still have to reach somebody.
func ErrorsByFile(output string) map[string][]string {
	return errorsByFile(output, "")
}

// ErrorsByFileIn is ErrorsByFile with the target root stripped from each path,
// so "server/main.go:12" matches the plan's "main.go".
//
// Necessary because the build runs from the project root while the plan names
// files relative to its own root, and a mismatch means the repair loop finds
// nothing to repair and silently gives up.
func ErrorsByFileIn(output, targetRoot string) map[string][]string {
	return errorsByFile(output, targetRoot)
}

func errorsByFile(output, targetRoot string) map[string][]string {
	byFile := map[string][]string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := reBuildError.FindStringSubmatch(line); m != nil {
			path := m[1]
			if targetRoot != "" && targetRoot != "." {
				path = strings.TrimPrefix(path, targetRoot+"/")
			}
			byFile[path] = append(byFile[path], line)
			continue
		}
		byFile[""] = append(byFile[""], line)
	}
	return byFile
}

// filesToRepair returns the paths a build blamed, most-blamed first, restricted
// to files the plan actually declares.
//
// Ordering by count because a file with fourteen errors is usually the cause and
// the others its symptoms — repairing it first often resolves them.
func filesToRepair(byFile map[string][]string, plan *Plan) []string {
	inPlan := map[string]bool{}
	for _, f := range plan.Files {
		inPlan[f.Path] = true
	}
	var paths []string
	for p := range byFile {
		if p != "" && inPlan[p] {
			paths = append(paths, p)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		if len(byFile[paths[i]]) != len(byFile[paths[j]]) {
			return len(byFile[paths[i]]) > len(byFile[paths[j]])
		}
		return paths[i] < paths[j]
	})
	return paths
}

// repairPrompt asks for one file again, with what the compiler said about it.
func repairPrompt(f PlannedFile, plan *Plan, errs, general []string,
	decls map[string][]Declaration, order []string, platBP string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Rewrite %s so the implementation builds.\n\n", f.Path)
	b.WriteString("Output the complete corrected file and nothing else. No explanation, " +
		"no plan, no summary of what you changed, no code fence. The first character " +
		"of your response is the first character of the file.\n\n")
	b.WriteString("Compiler errors naming this file:\n")
	for _, e := range errs {
		b.WriteString("  " + e + "\n")
	}
	if len(general) > 0 {
		b.WriteString("\nOther build output, which may be caused by this file:\n")
		for _, e := range general {
			b.WriteString("  " + e + "\n")
		}
	}
	fmt.Fprintf(&b, "\nPurpose: %s\n", f.Purpose)
	if len(f.Declares) > 0 {
		fmt.Fprintf(&b, "It MUST still define: %s\n", strings.Join(f.Declares, ", "))
	}
	if len(f.Serves) > 0 {
		fmt.Fprintf(&b, "It MUST still serve: %s\n", strings.Join(f.Serves, ", "))
	}
	if d := FormatDeclarations(decls, order); d != "" {
		b.WriteString("\nSymbols declared by the OTHER files. Do not redeclare them, " +
			"and call them exactly as shown:\n")
		b.WriteString(d)
	}
	b.WriteString("\n--- PLATFORM BLUEPRINT ---\n")
	b.WriteString(platBP)
	return b.String()
}

// BuildAndRepair builds the target and, on failure, regenerates the files the
// compiler blamed — with the errors — until it builds or the rounds run out.
//
// Returns the final build result and the files as they stand.
// BuildAndRepair builds the target and, on failure, regenerates the files the
// compiler blamed.
//
// root is where the build command runs; the files live at root/plan.Root.
func BuildAndRepair(provider Provider, plan *Plan, root, platBP string, files []GeneratedFile,
	onProgress ProgressFunc, checklist []ChecklistItem, spec map[string]string) (BuildResult, []GeneratedFile, error) {
	dir := filepath.Join(root, plan.Root)
	if onProgress == nil {
		onProgress = func(Progress) {}
	}
	byPath := map[string]PlannedFile{}
	for _, f := range plan.Files {
		byPath[f.Path] = f
	}
	content := map[string]string{}
	var order []string
	for _, f := range files {
		content[f.Path] = f.Content
		order = append(order, f.Path)
	}

	// Write before the first build. The caller has usually done this already,
	// but depending on that made the function silently do nothing when it had
	// not — a build over an empty directory succeeds, and reports a target that
	// was never compiled as sound.
	if err := writeContent(dir, content, order); err != nil {
		return BuildResult{}, files, err
	}

	prepare := strings.TrimSpace(plan.Prepare)

	// Progress is measured, not assumed: the error count must fall. Two rounds
	// with no reduction means the model is not converging, and further rounds
	// spend calls to produce the same output.
	previousErrors := -1
	stalled := 0

	// The checklist gets its own progress counters. Build errors falling to zero
	// and then assertions appearing is not a regression, and one counter would
	// read it as one and declare the loop stalled at the moment it started doing
	// the more interesting half of its job.
	previousDefects := -1
	defectStall := 0

	var result BuildResult
	for round := 1; round <= repairRoundCeiling; round++ {
		// Resolve before EVERY build, not once. A repair changes imports — adding
		// a package, dropping one — and the lockfile then needs updating again.
		// Running it once produced "go: updates to go.mod needed" on round two,
		// which names no file, so the loop correctly found nothing to repair and
		// stopped on a fault no repair could have addressed.
		if prepare != "" {
			onProgress(Progress{Path: "prepare", Status: "preparing", Attempt: round})
			if prep := RunBuild(root, prepare); !prep.OK {
				return prep, files, fmt.Errorf("dependency resolution failed: %s\n%s",
					prepare, prep.Output)
			}
		}

		onProgress(Progress{Path: "build", Status: "building", Attempt: round})
		result = RunBuild(root, plan.Build)
		if result.OK {
			onProgress(Progress{Path: "build", Status: "built", Attempt: round})

			// Compiling is not conforming, and the blueprints say what conforming
			// means.
			//
			// This used to evaluate structural checks written in Go. They found a
			// real bug and produced ten false failures, each a re-reading of an
			// assertion that was subtly not what the assertion said. The assertions
			// now go to the model verbatim and it reads them against its own
			// output — the same act a reviewer performs, with no transcription of
			// the requirement into a second language. See selfverify.go.
			verdicts, verr := SelfVerify(provider, rebuildList(content, order), checklist)
			if verr != nil {
				// Verification failing is not the implementation failing. Report it
				// and stop, rather than treating an unreadable response as a pass.
				onProgress(Progress{Path: "verify", Status: "failed", Attempt: round,
					Detail: verr.Error()})
				return result, rebuildList(content, order), nil
			}
			yes, no, unverifiable, unanswered := VerdictSummary(verdicts, len(checklist))
			onProgress(Progress{Path: "verify", Status: "progress", Attempt: round,
				Detail: fmt.Sprintf("%d satisfied, %d unmet, %d unverifiable, %d unanswered",
					yes, no, unverifiable, unanswered)})

			bad := Violations(verdicts, checklist)
			if len(bad) == 0 {
				return result, rebuildList(content, order), nil
			}

			if previousDefects >= 0 && len(bad) >= previousDefects {
				defectStall++
			} else {
				defectStall = 0
			}
			if defectStall >= 2 {
				onProgress(Progress{Path: "verify", Status: "failed", Attempt: round,
					Detail: fmt.Sprintf("no progress over two rounds — still %d assertion(s)", len(bad))})
				return result, rebuildList(content, order), nil
			}
			previousDefects = len(bad)

			byFile, orphans := violationTargets(bad, plan)
			if len(byFile) == 0 {
				onProgress(Progress{Path: "verify", Status: "failed", Attempt: round,
					Detail: fmt.Sprintf("%d assertion(s) unmet, none attributed to a planned file", len(bad))})
				return result, rebuildList(content, order), nil
			}
			if len(orphans) > 0 {
				onProgress(Progress{Path: "verify", Status: "progress", Attempt: round,
					Detail: fmt.Sprintf("%d assertion(s) name no planned file and are left to review", len(orphans))})
			}

			for _, path := range sortedViolationKeys(byFile) {
				f := byPath[path]
				onProgress(Progress{Path: path, Status: "repairing", Attempt: round,
					Detail: byFile[path][0].Item.Text})
				accepted, aerr := askForFile(provider, conformancePrompt(f, content[path], byFile[path], platBP), f)
				if aerr != nil {
					return result, rebuildList(content, order), fmt.Errorf("conforming %s: %w", path, aerr)
				}
				if accepted == "" {
					continue
				}
				content[path] = accepted
			}
			if err := writeContent(dir, content, order); err != nil {
				return result, rebuildList(content, order), err
			}
			continue
		}

		byFile := ErrorsByFileIn(result.Output, plan.Root)

		// Verification-driven exit: keep going while the count is falling.
		errorCount := 0
		for _, errs := range byFile {
			errorCount += len(errs)
		}
		if previousErrors >= 0 {
			if errorCount >= previousErrors {
				stalled++
			} else {
				stalled = 0
			}
		}
		onProgress(Progress{Path: "build", Status: "progress", Attempt: round,
			Detail: fmt.Sprintf("%d error(s) remaining", errorCount)})
		if stalled >= 2 {
			onProgress(Progress{Path: "build", Status: "failed", Attempt: round,
				Detail: fmt.Sprintf("no progress over two rounds — still %d error(s)", errorCount)})
			return result, rebuildList(content, order), nil
		}
		previousErrors = errorCount

		targets := filesToRepair(byFile, plan)
		if len(targets) == 0 {
			// Nothing the plan owns was blamed — a missing dependency or a
			// toolchain fault. Repairing a file cannot fix it, and pretending
			// otherwise would burn rounds on the wrong thing.
			onProgress(Progress{Path: "build", Status: "failed", Attempt: round,
				Detail: "the build failed without naming a file this plan owns"})
			return result, rebuildList(content, order), nil
		}

		for _, path := range targets {
			f := byPath[path]
			onProgress(Progress{Path: path, Status: "repairing", Attempt: round,
				Detail: firstLine(byFile[path])})

			// Every other file's declarations, so the repair matches them.
			others := map[string][]Declaration{}
			var otherOrder []string
			for _, p := range order {
				if p == path {
					continue
				}
				if d := ExtractDeclarations(p, content[p]); len(d) > 0 {
					others[p] = d
					otherOrder = append(otherOrder, p)
				}
			}
			// Retry a contract violation, exactly as generation does. Repair
			// previously gave up on the file after one bad response, so a reply
			// that came back as prose cost the whole ROUND for that file — and the
			// next round then re-read the same unrepaired errors. Three files were
			// lost that way in one run.
			prompt := repairPrompt(f, plan, byFile[path], byFile[""], others, otherOrder, platBP)
			accepted, aerr := askForFile(provider, prompt, f)
			if aerr != nil {
				return result, rebuildList(content, order), fmt.Errorf("repairing %s: %w", path, aerr)
			}
			if accepted == "" {
				onProgress(Progress{Path: path, Status: "failed", Attempt: round,
					Detail: "the model would not return the file itself"})
				continue
			}
			content[path] = accepted
		}

		if err := writeContent(dir, content, order); err != nil {
			return result, rebuildList(content, order), err
		}
	}

	if prepare != "" {
		_ = RunBuild(root, prepare)
	}
	result = RunBuild(root, plan.Build)
	return result, rebuildList(content, order), nil
}

func rebuildList(content map[string]string, order []string) []GeneratedFile {
	out := make([]GeneratedFile, 0, len(order))
	for _, p := range order {
		out = append(out, GeneratedFile{Path: p, Content: content[p], Lang: inferLang(p)})
	}
	return out
}

func writeContent(dir string, content map[string]string, order []string) error {
	files := rebuildList(content, order)
	_, err := writeGeneratedFiles(dir, files)
	return err
}

func firstLine(errs []string) string {
	if len(errs) == 0 {
		return ""
	}
	return errs[0]
}

// askForFile requests one file and retries a response that is not the file.
//
// Shared by build repair and conformance repair. Repair once gave up on a file
// after a single bad response, so a reply that came back as prose cost the whole
// ROUND for that file and the next round re-read the same unrepaired errors —
// three files were lost that way in one run.
func askForFile(provider Provider, prompt string, f PlannedFile) (string, error) {
	var lastViolation string
	for attempt := 1; attempt <= maxFileAttempts; attempt++ {
		ask := prompt
		if lastViolation != "" {
			ask = "Your previous response was rejected: " + lastViolation +
				"\nOutput the file itself, nothing else.\n\n" + prompt
		}
		raw, err := provider.Chat([]Message{
			{Role: "system", Content: fileSystemPrompt},
			{Role: "user", Content: ask},
		})
		if err != nil {
			return "", err
		}
		candidate := stripFence(raw)
		if v := contractViolation(candidate, f); v != "" {
			lastViolation = v
			continue
		}
		return candidate, nil
	}
	return "", nil
}

// violationTargets groups unmet assertions by the planned file they are about.
//
// Assertions naming no planned file are returned separately rather than assigned
// somewhere plausible. A conformance repair aimed at the wrong file edits correct
// code to satisfy something it does not control, which is worse than an
// unrepaired failure that is reported.
func violationTargets(bad []Violation, plan *Plan) (map[string][]Violation, []Violation) {
	planned := map[string]bool{}
	for _, f := range plan.Files {
		planned[f.Path] = true
	}
	byFile := map[string][]Violation{}
	var orphans []Violation
	for _, r := range bad {
		// The model names the file it found the fault in. A name that is not in
		// the plan is not corrected to one that is: repairing the wrong file edits
		// correct code to satisfy something it does not control.
		if p := filepath.Base(strings.TrimSpace(r.File)); planned[p] {
			byFile[p] = append(byFile[p], r)
			continue
		}
		orphans = append(orphans, r)
	}
	return byFile, orphans
}

func sortedViolationKeys(m map[string][]Violation) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// conformancePrompt asks for one file again, to satisfy assertions it violates.
//
// It states the assertion VERBATIM and names the blueprint it came from. A
// paraphrase would be the tooling's opinion of a requirement standing in for the
// requirement, which is the failure mode that produced four false positives in
// the checking layer already.
func conformancePrompt(f PlannedFile, current string, failures []Violation, platBP string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The file %s compiles but violates verification assertions from the blueprints it implements.\n\n", f.Path)
	fmt.Fprintf(&b, "Purpose: %s\n\n", f.Purpose)
	b.WriteString("Assertions this file must satisfy, quoted from their blueprint:\n")
	for _, r := range failures {
		fmt.Fprintf(&b, "\n  From %s:\n    %s\n", r.Item.Source, r.Item.Text)
		if r.Evidence != "" {
			fmt.Fprintf(&b, "    What was found: %s\n", r.Evidence)
		}
	}
	b.WriteString("\nCurrent content of the file:\n\n")
	b.WriteString(current)
	b.WriteString("\n\nRewrite the file so every assertion above holds. Keep everything else " +
		"unchanged — other files depend on the symbols this one declares, and a " +
		"rename here becomes a build failure there.\n")
	if platBP != "" {
		b.WriteString("\nPlatform requirements:\n\n")
		b.WriteString(platBP)
	}
	return b.String()
}
