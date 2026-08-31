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
	}
	return roots
}
