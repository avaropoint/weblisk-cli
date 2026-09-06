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
// caller observes — endpoints, types, properties — is the declaration. How that is
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
	// FieldFaults are bindings in this component's own contract that name a
	// field the type does not define. Generation MUST NOT proceed with one:
	// FormatBindings passes fields_used to the model verbatim, so a fault here
	// instructs it to implement something the protocol has no name for — and it
	// complies. See architecture/generation, "A binding is not a wish".
	FieldFaults []FieldBindingFault
	// FromDeclaration records that this component's requirements came from a
	// declared declaration block rather than from the scattered parsers.
	FromDeclaration bool
	// DeclarationError is a declaration that is present and malformed. It stops the
	// run: falling back would generate from the old sections while the author
	// believes the declaration is in force.
	DeclarationError error
	// DeclarationOmissions are endpoints protocol/spec requires of this component
	// that its contract does not declare. A hole in the declaration, reported
	// rather than filled in.
	DeclarationOmissions []string
	// Types are what the TARGET's blueprint declares it consumes, from its
	// binding contracts — not every type the protocol defines.
	Types []string
	// Bindings are those declarations in full, with the fields used.
	Bindings []Binding
	// UnboundTypes are types the sent blueprints define that no binding claims.
	// Reported as a gap in the blueprint's contract, never added to Types.
	UnboundTypes []string
	Endpoints    []string // from protocol/spec.md, for this target
	// EndpointOps are those endpoints with the name their blueprint declares
	// for them, from the Operation column of the serving component's
	// `## Endpoints` table.
	//
	// The wire fact and the name are different things. "POST /v1/register" is
	// what a client sends; "Register" is what every symbol in the generated
	// artifact is spelled from. Without the second, a generator names the
	// endpoint from its path and names it differently every run.
	EndpointOps []EndpointOperation
	// Operations are store operations this component owns, exactly as
	// architecture/storage declares them.
	//
	// These were never read. The blueprint said PutAgent, GetAgent,
	// DeleteAgent, ListAgents all along, and one plan used Get, Put and List
	// instead — no file wrong, every file stale.
	Operations []string
	// UnnamedEndpoints are endpoints whose blueprint has no Operation for
	// them. A gap in the blueprint, reported as one: schemas/architecture
	// requires the column, and inventing the missing name here is how a
	// pipeline quietly becomes the specification.
	UnnamedEndpoints []string
	Checklist        []ChecklistItem // from every blueprint being read, scoped to this target
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
	i := headingIndex(spec, "## "+section)
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
	// The CONTRACT when the blueprint has one, the scattered parsers when it
	// does not. Nothing is deleted until the corpus has migrated, so a
	// half-migrated corpus still builds — see CONTRACT_BLOCK_PLAN.md.
	if body, ok := g.Map[targetBlueprint(target)]; ok && HasDeclaration(body) {
		c, cerr := ExtractDeclaration(targetBlueprint(target), body)
		if cerr != nil {
			// A malformed contract is a fault in the blueprint and MUST stop
			// the run. Falling back would generate from the old sections while
			// the author believes the declaration is in force.
			req.DeclarationError = cerr
		} else if c != nil {
			req.FromDeclaration = true
			req.Bindings = c.Bindings()
			req.Types = BoundTypes(req.Bindings)
			req.EndpointOps = c.EndpointOperations()
			req.FieldFaults = CheckFieldBindings(
				map[string]string{targetBlueprint(target): body}, g.Map)
		}
	}
	if body, ok := g.Map[targetBlueprint(target)]; ok && !req.FromDeclaration {
		req.Bindings = ExtractBindings(body)
		req.Types = BoundTypes(req.Bindings)
		// Only this blueprint's own bindings reach the model, so only its own
		// faults can misinstruct it. Scoping the check here is what makes it
		// actionable: "your contract names a field that does not exist" rather
		// than a hundred findings in blueprints this component never binds.
		req.FieldFaults = CheckFieldBindings(map[string]string{targetBlueprint(target): body}, g.Map)
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
	// A contract SEEDS the set rather than adding to it.
	//
	// Appending produced 24 endpoints where there are 17: the declaration carries
	// every endpoint this component serves, including the seven protocol/spec
	// names, and adding both counted those seven twice. The first real build
	// on a declaration found it.
	//
	// protocol/spec is still read, but now as a CROSS-CHECK: an endpoint the
	// protocol requires of this component and the declaration omits is a hole in
	// the declaration, and is reported rather than quietly filled in. A contract
	// that is completed by the thing it replaced is not a declaration.
	if req.FromDeclaration {
		for _, e := range req.EndpointOps {
			seenEndpoint[e.Wire()] = true
			req.Endpoints = append(req.Endpoints, e.Wire())
		}
	}
	// Only the two components protocol/spec names have a section in it. A
	// component the protocol does not describe declares its whole surface in its
	// own blueprint — inheriting the orchestrator's section by default would
	// require it to serve /v1/register and /v1/channel, which belong to the
	// trust anchor and to nothing else.
	if spec, ok := g.Map["protocol/spec.md"]; ok {
		var required []string
		switch target {
		case "orchestrator":
			required = ExtractEndpoints(spec, "Orchestrator Endpoints")
		case "agent":
			required = ExtractEndpoints(spec, "Agent Endpoints")
		}
		if req.FromDeclaration {
			for _, e := range required {
				if !seenEndpoint[e] {
					req.DeclarationOmissions = append(req.DeclarationOmissions, e)
				}
			}
		}
		addEndpoints(required)
	}
	if body, ok := g.Map[targetBlueprint(target)]; ok && !req.FromDeclaration {
		// The declared name of each endpoint, alongside the wire fact.
		for _, e := range ExtractEndpointOperations(body) {
			if e.Operation == "" {
				req.UnnamedEndpoints = append(req.UnnamedEndpoints, e.Wire())
				continue
			}
			req.EndpointOps = append(req.EndpointOps, e)
		}
		addEndpoints(ExtractTableEndpoints(body))
	}
	sort.Strings(req.Endpoints)

	// Every REQUIRED endpoint must have a declared operation — including one
	// the component's table does not mention at all.
	//
	// UnnamedEndpoints was populated only from rows present in the table with
	// an empty Operation cell, which is the narrower fault. The one that
	// actually occurred was wider: protocol/spec declares POST /v1/rotate-key
	// and architecture/orchestrator's Endpoints table omitted it entirely, so
	// it was required, had no name, and nothing said so — the model planned no
	// file for it and the plan was rejected with "no file serves these
	// endpoints" three steps later, naming the symptom rather than the cause.
	//
	// A check that reports a narrower thing than its name suggests is the same
	// hazard as a check that cannot read the artifact: it is silent exactly
	// where it is needed.
	named := map[string]bool{}
	for _, e := range req.EndpointOps {
		named[e.Wire()] = true
	}
	for _, e := range req.Endpoints {
		if !named[e] && !containsString(req.UnnamedEndpoints, e) {
			req.UnnamedEndpoints = append(req.UnnamedEndpoints, e)
		}
	}
	sort.Strings(req.UnnamedEndpoints)

	// Store operations this component owns, named as architecture/storage names
	// them. Read from whichever storage blueprint is in the graph rather than
	// from a fixed path, because a component may be specified against a
	// different storage contract.
	for _, name := range g.Order {
		if !strings.HasSuffix(name, "storage.md") {
			continue
		}
		req.Operations = append(req.Operations,
			OperationsOwnedBy(ExtractStoreContracts(g.Map[name]), target)...)
	}

	// EVERY blueprint's assertions come from its prose Verification Checklist,
	// including the target's.
	//
	// A declaration block briefly carried them too. That was churn: the
	// checklist is the one surface already consistent — prose in all 87
	// blueprints that have one, parsed reliably, never drifted — and it was
	// never one of the scattered facts a declaration exists to fix. Moving it
	// made the pipeline read the same assertions from two places.
	//
	// It also makes no difference to the model: FormatChecklist renders either
	// form into the identical ACCEPTANCE CRITERIA block, so the model never
	// sees YAML or markdown, only the sentences.
	//
	// What a declaration DOES carry is `checks:` — machine-verifiable
	// predicates with declared subjects, which is what prose cannot express.
	var all []ChecklistItem
	for _, name := range g.Order {
		all = append(all, ExtractChecklist(name, g.Map[name])...)
	}

	mine, others := ScopeChecklist(all, target)
	// Then by BINDING, not only by group heading.
	//
	// A group heading scopes an assertion to a named component. It cannot scope
	// one that is about a TYPE: "TaskRequest requires id, from, action..." is
	// addressed to every implementation, so an orchestrator that binds nothing
	// from TaskRequest was graded on it and reported unmet. A clean generation
	// came back with fifteen such — WorkflowPhase, TaskResult, Finding,
	// DeadLetterEntry, ScopeLevel, PolicyDecision, OperationIntent — none of
	// which the orchestrator's own contract claims.
	//
	// That is the fifty-four-types fault in the grader rather than the planner:
	// the same list the pipeline correctly reports as "defined but unbound" was
	// being used to mark the component non-conformant.
	kept, unbound := SplitByBoundTypes(mine, req.Types, req.UnboundTypes)
	req.Checklist = kept
	req.Excluded = append(others, unbound...)
	return req
}

// SplitByBoundTypes separates assertions this component's contract answers for
// from assertions about types it binds nothing from.
//
// Conservative on purpose: an assertion is set aside only when it names at
// least one unbound type and no bound type. One naming both is kept, because a
// sentence relating a bound type to an unbound one is still about the bound one.
func SplitByBoundTypes(items []ChecklistItem, bound, unbound []string) (kept, setAside []ChecklistItem) {
	if len(unbound) == 0 {
		return items, nil
	}
	boundSet := make(map[string]bool, len(bound))
	for _, t := range bound {
		boundSet[t] = true
	}
	for _, it := range items {
		namesUnbound, namesBound := false, false
		for _, t := range unbound {
			if boundSet[t] {
				continue
			}
			if mentionsType(it.Text, t) {
				namesUnbound = true
			}
		}
		for _, t := range bound {
			if mentionsType(it.Text, t) {
				namesBound = true
			}
		}
		if namesUnbound && !namesBound {
			setAside = append(setAside, it)
			continue
		}
		kept = append(kept, it)
	}
	return kept, setAside
}

// mentionsType reports whether an assertion names a type, on a word boundary so
// "Observation" does not match inside "ObservationStore".
func mentionsType(text, typeName string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], typeName)
		if j < 0 {
			return false
		}
		j += i
		beforeOK := j == 0 || !isIdentRune(rune(text[j-1]))
		end := j + len(typeName)
		afterOK := end >= len(text) || !isIdentRune(rune(text[end]))
		if beforeOK && afterOK {
			return true
		}
		i = j + 1
	}
}

func isIdentRune(r rune) bool {
	return r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
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
	if n := len(r.Operations); n > 0 {
		parts = append(parts, plural(n, "declared operation"))
	}
	if n := len(r.UnnamedEndpoints); n > 0 {
		// Surfaced in the run's own header, because it is a gap in a blueprint
		// and the person reading the output is the one who can fix it.
		parts = append(parts, itoa(n)+" endpoint(s) with no declared Operation")
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
	i := headingIndex(blueprint, "## Endpoints")
	if i < 0 {
		return nil
	}
	rest := blueprint[i+len("## Endpoints"):]
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
