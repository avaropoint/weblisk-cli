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
	"github.com/avaropoint/weblisk-cli/internal/platform"
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
	// A shell, because a Build line from a platform blueprint is one string with
	// operators in it — "cd server && go build -o orchestrator ." — not an argv.
	// Which shell is a platform question: there is no sh on Windows, so every
	// build and every conformance repair failed there before the command ran.
	shell, shellArgs := platform.ShellCommand(command)
	cmd := exec.CommandContext(ctx, shell, shellArgs...)
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
func repairPrompt(f PlannedFile, plan *Plan, current string, errs, general []string,
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
	if plan.Module != "" {
		// The repair loop needs the module path for the same reason generation
		// does, and it did not have it. One file imported a module name from an
		// earlier run; the loop rewrote it three times, each time guessing the
		// same wrong prefix, and stalled on a fault it had no way to see.
		fmt.Fprintf(&b, "Module path: %s — every import of this project's own "+
			"packages begins with it.\n", plan.Module)
	}
	if len(f.Declares) > 0 {
		fmt.Fprintf(&b, "It MUST still define: %s\n", strings.Join(f.Declares, ", "))
	}
	if len(f.Serves) > 0 {
		fmt.Fprintf(&b, "It MUST still serve: %s\n", strings.Join(f.Serves, ", "))
	}
	if keep := currentDeclarations(f.Path, current); keep != "" {
		// Everything the file declares TODAY, not just what the plan named.
		//
		// A repair of manifest.go fixed its own two errors and dropped
		// ValidateIdentifier and manifestMaxIdentifierLen — helpers it had added
		// during generation, which four other files had come to call. Six errors
		// became eleven, and the loop stalled fixing a file that was no longer
		// the problem.
		//
		// The plan's `declares` cannot cover this: a helper invented while
		// writing the file was never in the plan. What the file actually declares
		// is read from the file.
		b.WriteString("\nThis file currently declares the following, and other files call them. " +
			"Keep every one unless an error above says to remove it:\n")
		b.WriteString(keep)
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
	onProgress ProgressFunc, checklist []ChecklistItem, spec map[string]string,
	conformance func([]GeneratedFile) ([]ConformanceResult, string, error)) (BuildResult, []GeneratedFile, error) {
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

	// Runtime failures get their own counters. A component that will not start
	// is a different question from one whose source violates an assertion, and
	// one counter would read the transition between them as a regression.
	previousRuntime := -1
	runtimeStall := 0

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
				// A dependency-resolution failure used to end the run. Most of
				// them should: a network failure or a missing toolchain is not
				// something a model can fix.
				//
				// But one kind is ordinary source: an import path that does not
				// exist. A generated hub imported
				// github.com/cloudflare/circl/sign/mldsa65 — plausible, and wrong;
				// the package is under sign/mldsa/mldsa65 — and `go mod tidy`
				// refused with "module found, but does not contain package". The
				// whole run ended on a one-line fault in one file, with 52 correct
				// files already written.
				targets := filesWithBadImports(prep.Output, content)
				if len(targets) == 0 {
					return prep, files, fmt.Errorf("dependency resolution failed: %s\n%s",
						prepare, prep.Output)
				}
				repaired := false
				for _, path := range targets {
					f := byPath[path]
					onProgress(Progress{Path: path, Status: "repairing", Attempt: round,
						Detail: firstLine([]string{importFaultLine(prep.Output)})})
					prompt := importRepairPrompt(f, content[path], prep.Output, platBP, plan.Module)
					accepted, aerr := askForFile(provider, prompt, f)
					if aerr != nil {
						return prep, files, fmt.Errorf("repairing imports in %s: %w", path, aerr)
					}
					if accepted != "" {
						content[path] = accepted
						repaired = true
					}
				}
				if !repaired {
					return prep, files, fmt.Errorf("dependency resolution failed: %s\n%s",
						prepare, prep.Output)
				}
				if err := writeContent(dir, content, order); err != nil {
					return prep, rebuildList(content, order), err
				}
				continue
			}
		}

		onProgress(Progress{Path: "build", Status: "building", Attempt: round})
		result = RunBuild(root, plan.Build)
		if result.OK {
			onProgress(Progress{Path: "build", Status: "built", Attempt: round})

			// Compiling is not running. Before asking whether the source
			// satisfies the assertions, find out whether the thing starts —
			// a hub once passed every gate and died two seconds in on a
			// namespace guard that rejected its owner.
			if conformance != nil {
				results, startupOut, cerr := conformance(rebuildList(content, order))
				if cerr != nil {
					targets := FilesBehindRuntimeFailure(startupOut, content)
					if len(targets) == 0 {
						onProgress(Progress{Path: "run", Status: "failed", Attempt: round,
							Detail: "does not run, and the failure names nothing in this plan: " + cerr.Error()})
						return result, rebuildList(content, order), nil
					}
					if previousRuntime >= 0 && len(targets) >= previousRuntime {
						runtimeStall++
					} else {
						runtimeStall = 0
					}
					if runtimeStall >= 2 {
						onProgress(Progress{Path: "run", Status: "failed", Attempt: round,
							Detail: "does not run, and two rounds of repair changed nothing"})
						return result, rebuildList(content, order), nil
					}
					previousRuntime = len(targets)

					for _, path := range targets {
						f := byPath[path]
						onProgress(Progress{Path: path, Status: "repairing", Attempt: round,
							// The FAILURE, not the first line of captured output —
							// which is a startup warning and told a reader nothing
							// about why the component died.
							Detail: firstLine(failureLines(startupOut))})
						accepted, aerr := askForFile(provider,
							runtimeRepairPrompt(f, content[path], startupOut, platBP, plan.Module), f)
						if aerr != nil {
							return result, rebuildList(content, order), fmt.Errorf("repairing %s: %w", path, aerr)
						}
						if accepted != "" {
							content[path] = accepted
						}
					}
					if err := writeContent(dir, content, order); err != nil {
						return result, rebuildList(content, order), err
					}
					continue
				}
				if bad := FailedConformance(results); len(bad) > 0 {
					onProgress(Progress{Path: "run", Status: "progress", Attempt: round,
						Detail: fmt.Sprintf("runs, %d conformance test(s) failing", len(bad))})
				}
			}

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
				accepted, aerr := askForFile(provider, conformancePrompt(f, content[path], byFile[path], platBP, plan.Module), f)
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

		// A contract between two files cannot be repaired one file at a time.
		//
		// An unsatisfied interface, a symbol declared twice, a method that does
		// not match — the error names one file and the fix needs both. Repaired
		// singly, each rewrite is locally reasonable and the pair never agrees:
		// a real build reported the same three errors on rounds 3, 4 and 5 and
		// stopped, having been asked five times to fix a disagreement while given
		// authority over one side of it.
		//
		// So the round begins by resolving errors to the files they implicate and
		// repairing any group of more than one TOGETHER. Groups are handled first
		// because a resolved contract usually removes the single-file errors that
		// were its symptoms.
		repaired := map[string]bool{}
		for _, path := range targets {
			if repaired[path] {
				continue
			}
			errsFor := append(append([]string{}, byFile[path]...), byFile[""]...)
			group := ImplicatedFiles(errsFor, content, plan)
			if len(group) < 2 {
				continue
			}
			var planned []PlannedFile
			for _, gp := range group {
				planned = append(planned, byPath[gp])
			}
			onProgress(Progress{Path: strings.Join(group, " + "), Status: "repairing", Attempt: round,
				Detail: "together: " + firstLine(byFile[path])})

			allErrs := map[string]bool{}
			var merged []string
			for _, gp := range group {
				for _, e := range byFile[gp] {
					if !allErrs[e] {
						allErrs[e] = true
						merged = append(merged, e)
					}
				}
			}
			decls := map[string][]Declaration{}
			for _, pth := range order {
				if d := ExtractDeclarations(pth, content[pth]); len(d) > 0 {
					decls[pth] = d
				}
			}
			prompt := repairGroupPrompt(planned, plan, content, merged, byFile[""], decls, order, platBP)
			got, gerr := askForFiles(provider, prompt, planned)
			if gerr != nil {
				// Not fatal. A group repair that comes back incomplete falls
				// through to the single-file path, which is worse at this fault and
				// better than nothing.
				onProgress(Progress{Path: strings.Join(group, " + "), Status: "failed", Attempt: round,
					Detail: gerr.Error()})
				continue
			}
			for _, g := range got {
				content[g.Path] = g.Content
				repaired[g.Path] = true
			}
		}

		for _, path := range targets {
			if repaired[path] {
				continue
			}
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
			prompt := repairPrompt(f, plan, content[path], byFile[path], byFile[""], others, otherOrder, platBP)
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
		if v := contractViolation(candidate, f, nil); v != "" {
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
func conformancePrompt(f PlannedFile, current string, failures []Violation, platBP, module string) string {
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
	if module != "" {
		fmt.Fprintf(&b, "\nModule path: %s — every import of this project's own "+
			"packages begins with it.\n", module)
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

// reMissingPackage matches a resolver complaint about an import that does not
// exist, in the two shapes Go emits.
var reMissingPackage = regexp.MustCompile(
	`(?m)(?:no required module provides package|does not contain package|cannot find package)\s+"?([\w./-]+)"?`)

// reImportingPackage matches the "X imports Y" line that names the offender.
var reImportingPackage = regexp.MustCompile(`(?m)^\s*([\w./-]+) imports\s*$`)

// filesWithBadImports finds the generated files importing a package the resolver
// could not find.
//
// Resolver output names a PACKAGE, not a file — "weblisk/internal/identity
// imports github.com/…/mldsa65". So the offending import path is taken from the
// error and matched against the source of every generated file, which is exact:
// a file either contains that import or it does not.
func filesWithBadImports(output string, content map[string]string) []string {
	var missing []string
	for _, m := range reMissingPackage.FindAllStringSubmatch(output, -1) {
		if p := strings.TrimSpace(m[1]); p != "" && strings.Contains(p, "/") {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	var out []string
	for path, body := range content {
		for _, pkg := range missing {
			if strings.Contains(body, `"`+pkg+`"`) {
				if !containsString(out, path) {
					out = append(out, path)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// importFaultLine is the first resolver line worth showing a reader.
func importFaultLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "does not contain package") ||
			strings.Contains(line, "no required module provides package") {
			return strings.TrimSpace(line)
		}
	}
	return firstLine(strings.Split(output, "\n"))
}

// importRepairPrompt asks for one file back with its imports corrected.
//
// Deliberately narrow: the resolver's own words, the file, and an instruction to
// change nothing else. A wrong import is a one-token fault, and inviting a
// rewrite of a file that is otherwise correct is how a repair becomes a
// regression.
func importRepairPrompt(f PlannedFile, current, resolverOutput, platBP, module string) string {
	var b strings.Builder
	b.WriteString("The dependency resolver rejected an import in this file.\n\n")
	b.WriteString("Resolver output:\n\n")
	b.WriteString(indentBlock(resolverOutput, "    "))
	b.WriteString("\n\nThe import path does not exist. The platform blueprint's Primitive " +
		"Mapping table gives the exact package path for every required module — use it.\n\n")
	fmt.Fprintf(&b, "File: %s\nPurpose: %s\n", f.Path, f.Purpose)
	if module != "" {
		fmt.Fprintf(&b, "Module path: %s — every import of this project's own "+
			"packages begins with it.\n", module)
	}
	b.WriteString("\nCurrent content:\n\n")
	b.WriteString(current)
	b.WriteString("\n\nReturn the file with its imports corrected and NOTHING else changed. " +
		"Every declaration, signature and line of logic stays exactly as it is.\n")
	if platBP != "" {
		b.WriteString("\nPlatform blueprint:\n\n")
		b.WriteString(platBP)
	}
	return b.String()
}

// currentDeclarations renders what a file declares right now.
//
// Read from the file rather than taken from the plan, because the plan lists
// what the model promised and a file also contains what it invented on the way —
// and the invented helpers are exactly the ones a repair drops, because nothing
// told it they mattered.
func currentDeclarations(path, content string) string {
	decls := ExtractDeclarations(path, content)
	if len(decls) == 0 {
		return ""
	}
	var b strings.Builder
	for _, d := range decls {
		b.WriteString("  " + d.String() + "\n")
	}
	return b.String()
}
