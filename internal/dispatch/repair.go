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
	"regexp"
	"sort"
	"strings"
	"time"
)

// buildTimeout bounds one build. Generous for a cold module cache, finite
// because a wedged toolchain would otherwise hang the run with no output.
const buildTimeout = 5 * time.Minute

// maxRepairRounds bounds how many times the pipeline will feed errors back.
//
// Three is enough for the usual causes — a missing dependency, a file returned
// in the wrong format, a name that drifted — and few enough that a target the
// model cannot fix fails visibly rather than looping.
const maxRepairRounds = 3

// BuildResult is the outcome of running a target's build command.
type BuildResult struct {
	OK      bool
	Output  string
	Command string
}

// RunBuild executes the plan's build command in the target directory.
func RunBuild(dir, command string) BuildResult {
	command = strings.TrimSpace(command)
	if command == "" {
		return BuildResult{OK: true, Command: ""}
	}
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
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
	byFile := map[string][]string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := reBuildError.FindStringSubmatch(line); m != nil {
			byFile[m[1]] = append(byFile[m[1]], line)
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
	fmt.Fprintf(&b, "The generated implementation does not build. Rewrite %s to fix it.\n\n", f.Path)
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
func BuildAndRepair(provider Provider, plan *Plan, dir, platBP string, files []GeneratedFile,
	onProgress ProgressFunc) (BuildResult, []GeneratedFile, error) {
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

	var result BuildResult
	for round := 1; round <= maxRepairRounds; round++ {
		onProgress(Progress{Path: "build", Status: "building", Attempt: round})
		result = RunBuild(dir, plan.Build)
		if result.OK {
			onProgress(Progress{Path: "build", Status: "built", Attempt: round})
			return result, rebuildList(content, order), nil
		}

		byFile := ErrorsByFile(result.Output)
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
			prompt := repairPrompt(f, plan, byFile[path], byFile[""], others, otherOrder, platBP)
			raw, err := provider.Chat([]Message{
				{Role: "system", Content: fileSystemPrompt},
				{Role: "user", Content: prompt},
			})
			if err != nil {
				return result, rebuildList(content, order), fmt.Errorf("repairing %s: %w", path, err)
			}
			candidate := stripFence(raw)
			if v := contractViolation(candidate, f); v != "" {
				// Leave the old content; the next round sees the same errors.
				onProgress(Progress{Path: path, Status: "failed", Attempt: round, Detail: v})
				continue
			}
			content[path] = candidate
		}

		if err := writeContent(dir, content, order); err != nil {
			return result, rebuildList(content, order), err
		}
	}

	result = RunBuild(dir, plan.Build)
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
