package dispatch

// Following the dependency graph the blueprints declare.
//
// # What this replaces, and why it mattered
//
// Generation loaded a hardcoded list of three blueprints:
//
//	{"protocol/spec.md", "architecture/orchestrator.md", "protocol/identity.md"}
//
// platforms/go.md declares nine:
//
//	requires: [protocol/identity, protocol/types, architecture/orchestrator,
//	           architecture/agent, architecture/domain, architecture/lifecycle,
//	           architecture/storage, architecture/gateway, patterns/deployment]
//
// The most damaging omission was protocol/types.md. It defines fifty-five types
// with their field tables and exact JSON keys — and it was read only to extract
// type NAMES for the requirements list. Its content never reached a prompt.
//
// So the model was handed fifty-five names and no definitions, and asked to
// implement them. It invented AgentEntry.Name and used it six times, and a run
// ended with fifty errors that looked like a model failing to keep files
// consistent. It was not. The definitions existed, in a blueprint the pipeline
// declined to send.
//
// # The rule
//
// The blueprints declare their own dependencies in frontmatter. Those
// declarations are the specification's own statement of what is needed to
// implement it, and they are resolved transitively rather than replaced by a
// shorter list somebody wrote in the tooling.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var reRequires = regexp.MustCompile(`(?m)^requires:\s*\[([^\]]*)\]`)

// DeclaredRequires reads a blueprint's frontmatter dependencies.
//
// Names are returned as the blueprint references them — "protocol/types" — since
// that is how they appear in a requires list; the ".md" is added when loading.
func DeclaredRequires(blueprint string) []string {
	// Only the frontmatter block, so a requires: line in a YAML example inside the
	// body cannot inject dependencies.
	end := strings.Index(blueprint, "-->")
	if end < 0 {
		return nil
	}
	m := reRequires.FindStringSubmatch(blueprint[:end])
	if m == nil {
		return nil
	}
	var out []string
	for _, part := range strings.Split(m[1], ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// blueprintFileName turns a requires entry into a loadable path.
func blueprintFileName(name string) string {
	name = strings.TrimSpace(name)
	if strings.HasSuffix(name, ".md") {
		return name
	}
	return name + ".md"
}

// ResolveRequires loads roots and everything they transitively require.
//
// Returns the blueprints keyed by path and a stable order. A dependency that
// cannot be loaded is reported rather than skipped: silently dropping a declared
// requirement is how the pipeline came to send three of nine.
func ResolveRequires(root string, roots ...string) (map[string]string, []string, []string, error) {
	loaded := map[string]string{}
	var order []string
	var missing []string
	seen := map[string]bool{}

	var visit func(name string) error
	visit = func(name string) error {
		file := blueprintFileName(name)
		if seen[file] {
			return nil
		}
		seen[file] = true

		body, err := LoadBlueprint(root, file)
		if err != nil {
			// Recorded, not fatal: a blueprint set may legitimately reference
			// something this installation does not carry, and refusing to
			// generate at all would be worse than generating with a stated gap.
			missing = append(missing, file)
			return nil
		}
		loaded[file] = body
		order = append(order, file)

		for _, dep := range DeclaredRequires(body) {
			if err := visit(dep); err != nil {
				return err
			}
		}
		return nil
	}

	for _, r := range roots {
		if err := visit(r); err != nil {
			return nil, nil, nil, err
		}
	}
	if len(loaded) == 0 {
		return nil, nil, missing, fmt.Errorf("no blueprints could be loaded from %v", roots)
	}
	sort.Strings(missing)
	return loaded, order, missing, nil
}

// ResolveDeclared loads the roots plus everything they DIRECTLY require.
//
// Depth one, deliberately. The transitive closure of platforms/go.md is
// forty-one blueprints and 314,000 tokens — the entire framework, including
// federation, privacy and approval — which no prompt can carry and which the
// platform blueprint does not claim to need. Its own requires list is nine
// entries, and that is the specification's statement of what implementing it
// takes.
//
// Depth one for an orchestrator resolves to eleven blueprints and about 79,000
// tokens, and it contains protocol/types.md, which the previous hardcoded list
// of three omitted entirely.
func ResolveDeclared(root string, roots ...string) (map[string]string, []string, []string, error) {
	loaded := map[string]string{}
	var order []string
	var missing []string
	seen := map[string]bool{}

	add := func(name string) {
		file := blueprintFileName(name)
		if seen[file] {
			return
		}
		seen[file] = true
		body, err := LoadBlueprint(root, file)
		if err != nil {
			missing = append(missing, file)
			return
		}
		loaded[file] = body
		order = append(order, file)
	}

	// Roots first.
	for _, r := range roots {
		add(r)
	}
	// Then requirements declared by the TARGET's blueprints only.
	//
	// A platform blueprint's requires list covers every component type it
	// describes — orchestrator, agent, domain, gateway — because it is the guide
	// for building any of them. Following it while generating a starter hub pulls
	// in architecture/agent, architecture/domain, architecture/gateway and
	// architecture/lifecycle: blueprints for components nobody asked for. The
	// objective is a STARTER hub, not the whole framework.
	//
	// architecture/orchestrator.md requires exactly two things. That is the
	// specification's own statement of what an orchestrator needs, and it is the
	// one to follow.
	for _, r := range roots {
		if isPlatformBlueprint(r) {
			continue
		}
		body, ok := loaded[blueprintFileName(r)]
		if !ok {
			continue
		}
		for _, dep := range DeclaredRequires(body) {
			add(dep)
		}
	}
	if len(loaded) == 0 {
		return nil, nil, missing, fmt.Errorf("no blueprints could be loaded from %v", roots)
	}
	sort.Strings(missing)
	return loaded, order, missing, nil
}

// isPlatformBlueprint reports whether a path is a platform guide, whose requires
// list describes every component type rather than the one being built.
func isPlatformBlueprint(path string) bool {
	return strings.HasPrefix(path, "platforms/")
}

// GenerationRoots are the blueprints a target is generated from, before their
// declared requirements are added.
//
// Not a curated list of everything needed — that is what requires: is for. These
// are only the entry points: the platform, and the architecture of the thing
// being built.
func GenerationRoots(target, platform string) []string {
	roots := []string{PlatformBlueprint(platform), "protocol/spec.md"}
	switch target {
	case "orchestrator":
		return append(roots, "architecture/orchestrator.md")
	case "agent":
		return append(roots, "architecture/agent.md")
	case "domain":
		return append(roots, "architecture/domain.md")
	case "gateway":
		return append(roots, "architecture/gateway.md")
	case "content":
		return append(roots, "architecture/content.md")
	}
	return roots
}

// BlueprintGraph is the blueprint set one implementation is generated from,
// together with where each copy came from.
type BlueprintGraph struct {
	Map      map[string]string // path → content
	Order    []string          // stable order, roots first
	Missing  []string          // declared requirements this installation lacks
	Sources  []Source          // directories searched, in precedence order
	ServedBy map[string]Source // path → the source that actually answered
	// Deferred are requirements declared by a blueprint IN the graph that were
	// not loaded, because resolution stops at depth one from the target.
	//
	// Reported rather than left implicit. architecture/storage.md declares seven
	// requirements — domain, gateway, workflow, task and lifecycle among them —
	// because it documents fourteen stores and names their consumers. Following
	// them would pull the whole framework into a starter hub, so declining is
	// correct. Declining SILENTLY is how a pipeline comes to look complete while
	// sending a fraction of what was declared, which is the exact fault that cost
	// this one protocol/types.md.
	Deferred map[string][]string // blueprint → its unloaded requirements
}

// Joined renders the graph as one document, for prompts that take the corpus
// whole rather than per file.
func (g *BlueprintGraph) Joined() string {
	parts := make([]string, 0, len(g.Order))
	for _, name := range g.Order {
		parts = append(parts, g.Map[name])
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// ResolveGraph is the ONE place a target's blueprint set is decided.
//
// # Why this exists rather than three lists
//
// Three parts of generation each answered "which blueprints?" separately, and
// they disagreed:
//
//   - the PLAN was made from a hardcoded three
//   - GENERATION sent the declared graph of six
//   - the CHECKLIST — which is the loop's termination condition — came from the
//     hardcoded three plus the platform and types
//
// So architecture/storage.md was sent to the model and its fourteen assertions
// were not in what the loop verified, while the plan that decided which files
// exist was made without ever seeing it. Each list was defensible alone. Held
// together they meant the pipeline was planning against one specification,
// building against a second, and grading against a third.
//
// One resolution, used by all three.
func ResolveGraph(root, target, platform string) (*BlueprintGraph, error) {
	srcs := ResolveSources(root)
	bpMap, order, missing, err := ResolveDeclared(root, GenerationRoots(target, platform)...)
	if err != nil {
		return nil, err
	}
	g := &BlueprintGraph{Map: bpMap, Order: order, Missing: missing, Sources: srcs,
		ServedBy: map[string]Source{}, Deferred: map[string][]string{}}

	// What the graph declares and does not carry.
	for _, name := range order {
		for _, dep := range DeclaredRequires(bpMap[name]) {
			file := blueprintFileName(dep)
			if _, loaded := bpMap[file]; loaded {
				continue
			}
			if containsString(missing, file) {
				continue // already reported as absent from this installation
			}
			g.Deferred[name] = append(g.Deferred[name], file)
		}
		sort.Strings(g.Deferred[name])
	}
	// Which source answered for each blueprint, not merely which were searched.
	//
	// Precedence means a graph can be assembled from more than one copy — a
	// project override of two files with the cached repo supplying the rest. A
	// list of directories cannot express that, and the mixed case is precisely
	// the one where a number disagrees with the file you just edited.
	for _, name := range order {
		for _, s := range srcs {
			if _, err := os.Stat(filepath.Join(s.Dir, name)); err == nil {
				g.ServedBy[name] = s
				break
			}
		}
	}
	return g, nil
}

// Describe renders the graph and its provenance for run output.
//
// Printed on every generation, because a specification pipeline that will not
// say which copy of the specification it read cannot be held to its result.
func (g *BlueprintGraph) Describe() string {
	b := &strings.Builder{}
	// Label each source once, so a blueprint can be attributed in a column
	// rather than by repeating a path six times.
	label := map[string]string{}
	for i, s := range g.Sources {
		label[s.Dir] = fmt.Sprintf("[%d]", i+1)
	}

	fmt.Fprintf(b, "  Blueprints: %d, as declared by requires:\n", len(g.Order))
	for _, name := range g.Order {
		src, ok := g.ServedBy[name]
		tag := "[?]"
		if ok {
			tag = label[src.Dir]
		}
		fmt.Fprintf(b, "    %s %s\n", tag, name)
	}
	if len(g.Deferred) > 0 {
		b.WriteString("  Declared and not followed (depth one from the target):\n")
		names := make([]string, 0, len(g.Deferred))
		for name := range g.Deferred {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(b, "    %s requires %s\n", name, strings.Join(g.Deferred[name], ", "))
		}
	}
	b.WriteString("  Read from:\n")
	for i, s := range g.Sources {
		used := 0
		for _, served := range g.ServedBy {
			if served.Dir == s.Dir {
				used++
			}
		}
		fmt.Fprintf(b, "    [%d] %s — %d of %d\n", i+1, s.Describe(), used, len(g.Order))
	}
	return b.String()
}
