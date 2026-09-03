package dispatch

// Reading from the corpus what the tooling was deciding for itself.
//
// Three lists lived in Go: which components can be built, which of a
// component's endpoints require a token, and which component names a checklist
// group may address. Each is already stated in the blueprints, so holding a
// copy meant the tooling could disagree with the specification and win —
// silently, because nothing compared them.
//
// A list that must be edited in Go when a blueprint changes is a second source
// of truth. These read the first one.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// reCapabilityBullet matches a standard-capability bullet in protocol/types.md,
// so the capability vocabulary is read from the blueprint rather than copied
// into the tooling:
//
//   - `content:read` — read entries and list a content repository
var reCapabilityBullet = regexp.MustCompile("(?m)^-\\s+`([a-z][a-z0-9]*:[a-z*][a-z0-9-]*)`")

// reEndpointRow reads a row of a component's Endpoints table, with its auth
// column:
//
//	| GET | /v1/content | yes | List entries beneath a path |
var reEndpointRow = regexp.MustCompile(`(?m)^\|\s*(GET|POST|PUT|PATCH|DELETE)\s*\|\s*` + "`?" +
	`(/v\d[\w/{}.*\-]*)` + "`?" + `\s*\|\s*([^|]*)\|`)

// ProtectedEndpoint is one method+path a component serves, and whether it
// requires a token.
type ProtectedEndpoint struct {
	Method string
	Path   string
}

// ProtectedEndpointsFor reads a component's token-protected surface from the
// Auth column of its own Endpoints table.
//
// Keyed by METHOD and path, because auth is a property of the pair. The
// orchestrator's table has two rows for /v1/register — POST is "no*",
// identity-verified rather than token-protected, and DELETE is "yes" — so
// treating auth as a property of the path alone marks registration protected
// and a prober then expects 401 from the one endpoint that must answer without
// a token.
func ProtectedEndpointsFor(component string, blueprints map[string]string) []ProtectedEndpoint {
	body, ok := blueprints[targetBlueprint(component)]
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var out []ProtectedEndpoint
	for _, m := range reEndpointRow.FindAllStringSubmatch(body, -1) {
		method, path := m[1], m[2]
		auth := strings.ToLower(strings.TrimSpace(m[3]))
		if !strings.HasPrefix(auth, "yes") {
			continue
		}
		key := method + " " + path
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ProtectedEndpoint{Method: method, Path: path})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// ProtectedGETsFor is the subset a GET prober can legitimately probe.
//
// A prober that issues GET against a path whose only protected row is DELETE
// learns nothing: the component answers 405, which is neither a pass nor a
// failure of the assertion being made.
func ProtectedGETsFor(component string, blueprints map[string]string) []string {
	var out []string
	for _, e := range ProtectedEndpointsFor(component, blueprints) {
		if e.Method == "GET" {
			out = append(out, e.Path)
		}
	}
	return out
}

// BuildableComponents returns the components this corpus can generate: an
// architecture blueprint that states an HTTP surface and declares what it
// consumes.
//
// Replaces a map in Go that had to be edited to add a component. A blueprint
// that meets the contract IS a buildable component; needing a code change as
// well meant the corpus could describe something the tool refused to build.
func BuildableComponents(blueprints map[string]string) []string {
	var out []string
	for path, body := range blueprints {
		name := strings.TrimSuffix(strings.TrimPrefix(path, "architecture/"), ".md")
		if !strings.HasPrefix(path, "architecture/") || name == "README" {
			continue
		}
		if !strings.Contains(body, "\n## Endpoints") {
			continue
		}
		if len(ExtractBindings(body)) == 0 {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// reComponentGroupList reads the closed list of component names a checklist
// group may address, from the sentence in schemas/common.md that declares it.
var reComponentGroupList = regexp.MustCompile(
	"with a component name — ((?:`[A-Za-z]+`(?:, )?)+)")

// DeclaredComponentGroups returns the component names schemas/common.md says a
// checklist group heading may address.
//
// Returns nil when the sentence is absent or reworded, and callers keep their
// own list in that case: silently returning an empty set would make every
// grouped assertion address every component, which is the opposite of what the
// declaration is for.
func DeclaredComponentGroups(blueprints map[string]string) []string {
	body, ok := blueprints["schemas/common.md"]
	if !ok {
		return nil
	}
	m := reComponentGroupList.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	var out []string
	for _, w := range strings.Split(m[1], ",") {
		w = strings.ToLower(strings.Trim(strings.TrimSpace(w), "`"))
		if w != "" {
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}

// LoadArchitectureCorpus reads every architecture blueprint this installation
// carries, so "which components exist" is answered by the corpus.
//
// Precedence is the same as any other load: a project copy shadows the cached
// one, so an installation that has amended a blueprint sees its own version.
func LoadArchitectureCorpus(root string) map[string]string {
	out := map[string]string{}
	for _, dir := range resolvedSources(root) {
		entries, err := os.ReadDir(filepath.Join(dir, "architecture"))
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".md") || name == "README.md" {
				continue
			}
			rel := "architecture/" + name
			if _, already := out[rel]; already {
				continue // an earlier source already answered; precedence holds
			}
			if b, rerr := os.ReadFile(filepath.Join(dir, rel)); rerr == nil {
				out[rel] = string(b)
			}
		}
	}
	return out
}
