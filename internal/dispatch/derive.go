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
	// By column NAME. This read the third cell, and adding the Operation column
	// that schemas/architecture requires made the third cell the operation —
	// so every protected endpoint in the corpus read as unprotected and the
	// prober expected no token where the blueprint requires one. A table
	// gaining a column is an ordinary edit; nothing may depend on cell order.
	add := func(tables []MarkdownTable, protected func(row map[string]string) bool) {
		for _, t := range tables {
			for _, row := range t.Rows {
				method := strings.ToUpper(strings.TrimSpace(row["method"]))
				path := strings.Trim(strings.TrimSpace(row["path"]), "`")
				if !isHTTPMethod(method) || !strings.HasPrefix(path, "/") || !protected(row) {
					continue
				}
				key := method + " " + path
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, ProtectedEndpoint{Method: method, Path: path})
			}
		}
	}
	// An Auth column: "yes" means a token is required.
	add(TablesWithColumns(body, "method", "path", "auth"), func(row map[string]string) bool {
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(row["auth"])), "yes")
	})
	// A Capability column: architecture/orchestrator.md declares its admin
	// surface in a SECOND table headed Capability rather than Auth, with cells
	// like `admin:read`. A required capability is an auth requirement — no
	// token, no capability — and reading only Auth-headed tables left every
	// admin endpoint invisible to the protection probe on the real corpus.
	add(TablesWithColumns(body, "method", "path", "capability"), func(row map[string]string) bool {
		c := strings.ToLower(strings.Trim(strings.TrimSpace(row["capability"]), "`"))
		return c != "" && !unprotectedCell(c)
	})
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

// isHTTPMethod recognises the methods a blueprint's endpoint table may name.
func isHTTPMethod(s string) bool {
	switch s {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}

// unprotectedCell reports whether an Auth/Capability cell says "anyone may
// call this". The corpus writes `no` and `no*` (footnoted: identity-verified
// registration, but no token); the other words are the ordinary ways a
// blueprint author would say the same thing. Anything else — `yes`, a
// capability like `admin:read` — is a requirement.
func unprotectedCell(c string) bool {
	switch {
	case c == "", c == "-", c == "—", c == "n/a":
		return true
	case strings.HasPrefix(c, "no"): // no, none, no*, not required
		return true
	case c == "public", c == "anonymous", c == "open", c == "unauthenticated":
		return true
	}
	return false
}
