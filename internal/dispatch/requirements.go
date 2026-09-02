package dispatch

// What the blueprints require, extracted rather than authored.
//
// # Why nothing is hand-written here
//
// A previous version of this pipeline carried a manifest: a checked-in file
// listing which files an implementation must contain and which symbols each must
// define. It was removed, and this replaced it, for two reasons.
//
// It NARROWED the specification while appearing to sharpen it. Its
// `must_define` for protocol.go named four types. protocol/types.md enumerates
// fifty-five. A generator reading the manifest learned about four.
//
// And it fixed a structure the specification has no business fixing. What a
// caller observes — endpoints, types, properties — is the contract. How that is
// divided into files is an implementation decision, and a hand-authored list
// prevented a model from making a better one.
//
// So requirements are read from the blueprints that already state them, and the
// FILE STRUCTURE is proposed by the model and validated against these. See
// architecture/generation.md.

import (
	"regexp"
	"sort"
	"strings"
)

// Requirements is what any conformant implementation must contain.
type Requirements struct {
	// Types are what the TARGET's blueprint declares it consumes, from its
	// binding contracts — not every type the protocol defines.
	Types []string
	// Bindings are those declarations in full, with the fields used.
	Bindings []Binding
	// UnboundTypes are types the sent blueprints define that no binding claims.
	// Reported as a gap in the blueprint's contract, never added to Types.
	UnboundTypes []string
	Endpoints    []string        // from protocol/spec.md, for this target
	Checklist    []ChecklistItem // from every blueprint being read, scoped to this target
	// Excluded are assertions a blueprint addresses to a DIFFERENT component.
	//
	// Kept rather than dropped so they can be reported. An assertion left out
	// silently is indistinguishable from one that was never written.
	Excluded []ChecklistItem
}

var (
	reTypeHeading     = regexp.MustCompile(`(?m)^###\s+([A-Z][A-Za-z0-9]*)\s*$`)
	reEndpointHeading = regexp.MustCompile(`(?m)^###\s+((?:GET|POST|PUT|DELETE|PATCH)\s+/v1/[^\s]+)\s*$`)
)

// ExtractTypes reads every type protocol/types.md enumerates.
func ExtractTypes(typesBlueprint string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reTypeHeading.FindAllStringSubmatch(typesBlueprint, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// ExtractEndpoints reads the endpoints defined under a named section of
// protocol/spec.md — "Orchestrator Endpoints" or "Agent Endpoints".
//
// Scoped to a section because the spec defines both, and an orchestrator that
// implemented the agent endpoints would be a different component.
func ExtractEndpoints(spec, section string) []string {
	i := strings.Index(spec, "## "+section)
	if i < 0 {
		return nil
	}
	rest := spec[i+len(section)+3:]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range reEndpointHeading.FindAllStringSubmatch(rest, -1) {
		ep := strings.Join(strings.Fields(m[1]), " ")
		if !seen[ep] {
			seen[ep] = true
			out = append(out, ep)
		}
	}
	sort.Strings(out)
	return out
}

// GatherRequirements assembles what an implementation of one target must satisfy,
// from the blueprints that target is actually generated from.
//
// The graph is the argument, not a target name, so requirements cannot be
// gathered from a different set than the one sent. Every blueprint in the graph
// contributes its checklist: an assertion is what the loop terminates on, and
// omitting a sent blueprint's assertions means grading against a specification
// narrower than the one the model was given. That omission cost
// architecture/storage.md's fourteen assertions — including the store contract
// whose AgentEntry schema the generated code kept inventing fields for.
func GatherRequirements(g *BlueprintGraph, target string) *Requirements {
	req := &Requirements{}

	// What this component declares it consumes. The blueprint's own statement,
	// read rather than reconstructed — see bindings.go for what scraping every
	// type heading instead cost.
	if body, ok := g.Map[targetBlueprint(target)]; ok {
		req.Bindings = ExtractBindings(body)
		req.Types = BoundTypes(req.Bindings)
	}
	if types, ok := g.Map["protocol/types.md"]; ok {
		req.UnboundTypes = UnboundTypes(ExtractTypes(types), req.Types)
	}
	// A component's own blueprint is the authoritative statement of what it
	// serves. protocol/spec names the protocol surface; the architecture document
	// adds whatever else that component owns — the orchestrator's administrative
	// endpoints, for instance, which protocol/spec does not and should not carry.
	seenEndpoint := map[string]bool{}
	addEndpoints := func(in []string) {
		for _, e := range in {
			if !seenEndpoint[e] {
				seenEndpoint[e] = true
				req.Endpoints = append(req.Endpoints, e)
			}
		}
	}
	// Only the two components protocol/spec names have a section in it. A
	// component the protocol does not describe declares its whole surface in its
	// own blueprint — inheriting the orchestrator's section by default would
	// require it to serve /v1/register and /v1/channel, which belong to the
	// trust anchor and to nothing else.
	if spec, ok := g.Map["protocol/spec.md"]; ok {
		switch target {
		case "orchestrator":
			addEndpoints(ExtractEndpoints(spec, "Orchestrator Endpoints"))
		case "agent":
			addEndpoints(ExtractEndpoints(spec, "Agent Endpoints"))
		}
	}
	if body, ok := g.Map[targetBlueprint(target)]; ok {
		addEndpoints(ExtractTableEndpoints(body))
	}
	sort.Strings(req.Endpoints)

	// Every blueprint in the graph, in the graph's order — then scoped to this
	// target, because a protocol blueprint's checklist covers both ends of the
	// conversation and an orchestrator does not serve POST /v1/describe.
	var all []ChecklistItem
	for _, name := range g.Order {
		all = append(all, ExtractChecklist(name, g.Map[name])...)
	}
	req.Checklist, req.Excluded = ScopeChecklist(all, target)
	return req
}

// Summary is a one-line description for progress output.
func (r *Requirements) Summary() string {
	var parts []string
	if n := len(r.UnboundTypes); n > 0 {
		parts = append(parts, itoa(n)+" defined but unbound")
	}
	if n := len(r.Types); n > 0 {
		parts = append(parts, plural(n, "bound type"))
	}
	if n := len(r.Endpoints); n > 0 {
		parts = append(parts, plural(n, "endpoint"))
	}
	if n := len(r.Checklist); n > 0 {
		parts = append(parts, plural(n, "checklist assertion"))
	}
	if n := len(r.Excluded); n > 0 {
		parts = append(parts, itoa(n)+" excluded ("+ExcludedSummary(r.Excluded)+")")
	}
	return strings.Join(parts, ", ")
}

func plural(n int, word string) string {
	s := ""
	if n != 1 {
		s = "s"
	}
	return itoa(n) + " " + word + s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// targetBlueprint is the architecture blueprint whose bindings describe a target.
func targetBlueprint(target string) string {
	switch target {
	case "orchestrator", "agent", "domain", "gateway", "admin", "content":
		return "architecture/" + target + ".md"
	}
	return ""
}

// reTableEndpoint matches an endpoint row in a component's Endpoints table:
//
//	| POST | /v1/admin/operators/register | — | Register an operator |
//	| GET  | /v1/services                 | yes | Service directory   |
var reTableEndpoint = regexp.MustCompile(`(?m)^\|\s*(GET|POST|PUT|PATCH|DELETE)\s*\|\s*` + "`?" + `(/v\d[\w/{}.*\-]*)` + "`?" + `\s*\|`)

// ExtractTableEndpoints reads the Endpoints section of a component's own
// blueprint.
//
// A component's architecture document is the authoritative statement of what it
// serves. protocol/spec carries the protocol surface every implementation shares;
// anything else a component owns — the orchestrator's administrative endpoints —
// is declared where that component is specified, and reading only protocol/spec
// misses it entirely.
func ExtractTableEndpoints(blueprint string) []string {
	i := strings.Index(blueprint, "\n## Endpoints")
	if i < 0 {
		return nil
	}
	rest := blueprint[i+len("\n## Endpoints"):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range reTableEndpoint.FindAllStringSubmatch(rest, -1) {
		ep := m[1] + " " + m[2]
		if !seen[ep] {
			seen[ep] = true
			out = append(out, ep)
		}
	}
	return out
}
