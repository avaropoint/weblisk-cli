package dispatch

// Repairing the files an error implicates, together.
//
// # The stall this ends
//
// A clean tenant build produced:
//
//	internal/storage/store.go:143:9: cannot use p (variable of type
//	*jsonlPersister) as persister value in assignment: *jsonlPersister does not
//	implement persister (wrong type for method load)
//
// and did not converge. Rounds 3, 4 and 5 each reported the same three errors,
// and the loop stopped — correctly, because it had made no progress.
//
// It could not make progress. The error names ONE file, store.go, where the
// interface is declared; the method that does not match is in jsonl.go. Asked
// to rewrite store.go alone, a model can only change the interface to match
// whatever it believes the implementer looks like — and asked next round to
// rewrite jsonl.go, it changes the method to match the interface it just saw.
// Each rewrite is locally reasonable and the pair never agrees.
//
// A contract between two files cannot be repaired one file at a time. So the
// error is resolved to every file it implicates, and those files are asked for
// in ONE response.
//
// # Why not simply send more context
//
// The single-file prompt already carries the other files' declarations, names
// and signatures included. It was not enough, and could not be: knowing what
// jsonl.go declares does not permit changing it. The model was being shown the
// conflict and given authority over one side of it.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	// reNotImplement matches Go's interface-satisfaction failure. Both named
	// types matter: one is the concrete type, the other the interface.
	//
	//	*jsonlPersister does not implement persister (wrong type for method load)
	reNotImplement = regexp.MustCompile(`\*?(\w+) does not implement \*?(\w+)`)

	// reOtherDeclaration matches the second half of a redeclaration, which the
	// compiler reports on its own line:
	//
	//	./registry.go:16:6: other declaration of DomainStatus
	reOtherDeclaration = regexp.MustCompile(`(?:^|\s)([\w./\\-]+\.\w+):\d+:\d+: other declaration of (\w+)`)

	// reUndefined matches a reference to something that does not exist. The file
	// that must change is usually the one that should DECLARE it, not the one
	// that called it.
	reUndefined = regexp.MustCompile(`undefined: (\w+)`)

	// reMethodOn matches a method reported against a named type.
	//
	//	method Registry.Load already declared at ./registry.go:60:20
	reMethodAlreadyDeclared = regexp.MustCompile(`method (\w+)\.(\w+) already declared at ([\w./\\-]+\.\w+):`)
)

// ImplicatedFiles returns every planned file that must change together to fix
// the given build errors, keyed so the caller can group repairs.
//
// The file an error NAMES is always included. Beyond that:
//
//   - an unsatisfied interface implicates the file declaring the interface and
//     the file declaring the concrete type
//   - a redeclaration implicates both declaring files
//   - an undefined symbol implicates whichever file declares it now, or is
//     planned to
//
// Files not in the plan are dropped: generation may only rewrite what it wrote.
func ImplicatedFiles(errs []string, content map[string]string, plan *Plan) []string {
	inPlan := map[string]bool{}
	for _, f := range plan.Files {
		inPlan[f.Path] = true
	}
	// symbol → the file that declares it, from the files as they stand.
	declaredIn := map[string]string{}
	typeMethodsIn := map[string]string{}
	for path, body := range content {
		for _, d := range ExtractDeclarations(path, body) {
			if _, taken := declaredIn[d.Name]; !taken {
				declaredIn[d.Name] = path
			}
		}
		for _, t := range declaredTypeNames(body) {
			if _, taken := typeMethodsIn[t]; !taken {
				typeMethodsIn[t] = path
			}
		}
	}
	// A planned declaration counts too: a symbol that does not exist yet is
	// still owed by exactly one file, and that is the file to repair.
	for _, f := range plan.Files {
		for _, d := range f.Declares {
			if _, taken := declaredIn[d]; !taken {
				declaredIn[d] = f.Path
			}
		}
	}

	set := map[string]bool{}
	add := func(p string) {
		if p != "" && inPlan[p] {
			set[p] = true
		}
	}
	addSymbol := func(sym string) {
		if p, ok := declaredIn[sym]; ok {
			add(p)
			return
		}
		if p, ok := typeMethodsIn[sym]; ok {
			add(p)
		}
	}

	for _, line := range errs {
		if m := reBuildError.FindStringSubmatch(line); m != nil {
			add(strings.TrimPrefix(m[1], "./"))
		}
		if m := reNotImplement.FindStringSubmatch(line); m != nil {
			addSymbol(m[1]) // the concrete type
			addSymbol(m[2]) // the interface
		}
		if m := reOtherDeclaration.FindStringSubmatch(line); m != nil {
			add(strings.TrimPrefix(m[1], "./"))
			addSymbol(m[2])
		}
		if m := reMethodAlreadyDeclared.FindStringSubmatch(line); m != nil {
			addSymbol(m[1])
			add(strings.TrimPrefix(m[3], "./"))
		}
		for _, m := range reUndefined.FindAllStringSubmatch(line, -1) {
			addSymbol(m[1])
		}
	}

	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// reTypeDecl finds a type name a Go file declares, so a type named in an error
// can be traced to its file even when it declares no exported symbol.
var reTypeDecl = regexp.MustCompile(`(?m)^\s*type\s+(\w+)\s`)

func declaredTypeNames(body string) []string {
	var out []string
	for _, m := range reTypeDecl.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// repairGroupPrompt asks for several files in one response, so a contract
// between them can be changed on both sides at once.
func repairGroupPrompt(group []PlannedFile, plan *Plan, content map[string]string,
	errs, general []string, decls map[string][]Declaration, order []string, platBP string) string {

	var b strings.Builder
	paths := make([]string, 0, len(group))
	for _, f := range group {
		paths = append(paths, f.Path)
	}
	fmt.Fprintf(&b, "These %d files must change TOGETHER so the implementation builds:\n  %s\n\n",
		len(group), strings.Join(paths, "\n  "))
	b.WriteString("The compiler reports a disagreement BETWEEN them — an interface and its\n" +
		"implementer, or one symbol declared twice. Repairing either alone cannot\n" +
		"resolve it: whichever side is changed, the other still disagrees. Decide\n" +
		"which side is correct, and make the other match it.\n\n")

	b.WriteString("Output every file above, complete, each preceded by its path on a line of\n" +
		"its own in exactly this form:\n\n")
	b.WriteString("// filename: " + paths[0] + "\n\n")
	b.WriteString("No explanation, no summary of what you changed, no code fences. A file you\n" +
		"do not output is left as it is, which will leave the build broken.\n\n")

	b.WriteString("Compiler errors:\n")
	for _, e := range errs {
		b.WriteString("  " + e + "\n")
	}
	if len(general) > 0 {
		b.WriteString("\nOther build output:\n")
		for _, e := range general {
			b.WriteString("  " + e + "\n")
		}
	}
	if plan.Module != "" {
		fmt.Fprintf(&b, "\nModule path: %s — every import of this project's own packages begins with it.\n",
			plan.Module)
	}

	for _, f := range group {
		fmt.Fprintf(&b, "\n--- %s ---\n", f.Path)
		fmt.Fprintf(&b, "Purpose: %s\n", f.Purpose)
		if len(f.Declares) > 0 {
			fmt.Fprintf(&b, "It MUST still define: %s\n", strings.Join(f.Declares, ", "))
		}
		if len(f.Serves) > 0 {
			fmt.Fprintf(&b, "It MUST still serve: %s\n", strings.Join(f.Serves, ", "))
		}
		if keep := currentDeclarations(f.Path, content[f.Path]); keep != "" {
			b.WriteString("It currently declares, and other files call:\n")
			b.WriteString(keep)
		}
		b.WriteString("\nCurrent contents:\n")
		b.WriteString(content[f.Path])
		b.WriteString("\n")
	}

	// Declarations of the files NOT in the group, so the repair does not break
	// them while reconciling the ones that are.
	outside := map[string][]Declaration{}
	inGroup := map[string]bool{}
	for _, f := range group {
		inGroup[f.Path] = true
	}
	var outsideOrder []string
	for _, p := range order {
		if !inGroup[p] {
			outside[p] = decls[p]
			outsideOrder = append(outsideOrder, p)
		}
	}
	if d := FormatDeclarations(outside, outsideOrder); d != "" {
		b.WriteString("\nSymbols declared by the files NOT listed above. Do not redeclare them, " +
			"and call them exactly as shown:\n")
		b.WriteString(d)
	}
	b.WriteString("\n--- PLATFORM BLUEPRINT ---\n")
	b.WriteString(platBP)
	return b.String()
}

// askForFiles requests a group repair and returns the files the model produced.
//
// A response missing one of the requested files is REJECTED rather than
// partially applied: the point of a group repair is that the files agree
// afterwards, and applying half of one leaves exactly the disagreement it was
// meant to fix.
func askForFiles(provider Provider, prompt string, want []PlannedFile) ([]GeneratedFile, error) {
	raw, err := provider.Chat([]Message{
		{Role: "system", Content: fileSystemPrompt},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return nil, err
	}
	got := parseGeneratedFiles(raw)
	byPath := map[string]string{}
	for _, g := range got {
		byPath[strings.TrimPrefix(g.Path, "./")] = g.Content
	}
	var missing []string
	var out []GeneratedFile
	for _, f := range want {
		body, ok := byPath[f.Path]
		if !ok || strings.TrimSpace(body) == "" {
			missing = append(missing, f.Path)
			continue
		}
		out = append(out, GeneratedFile{Path: f.Path, Content: body, Lang: inferLang(f.Path)})
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("the group repair returned %d of %d files; missing: %s",
			len(out), len(want), strings.Join(missing, ", "))
	}
	return out, nil
}
