package dispatch

// Reading the dependency contracts blueprints already declare.
//
// # The intervention this replaces
//
// Requirements gathering scraped every `### TypeName` heading out of
// protocol/types.md — 54 of them — and the plan prompt then said:
//
//	Types the protocol defines (54). Every one must be declared by
//	exactly one file
//
// with ValidatePlan REJECTING any plan that omitted one.
//
// architecture/orchestrator.md declares what it actually consumes:
//
//	bindings:
//	  types:
//	    - name: RegisterRequest
//	      fields_used: [manifest, signature, timestamp]
//	    - name: ServiceDirectory
//	      fields_used: [agents, routing_table, namespaces]
//	    ... ten in total
//
// Ten declared. Fifty-four demanded. So the model was required to implement
// WorkflowPhase, OperationIntent, EnforcementDecision, TaskRequest, Finding and
// DeadLetterEntry into an orchestrator — types belonging to domain controllers,
// the enforcement layer and agents.
//
// That is why one plan had nine files, the next sixteen and the next twenty,
// with store_execution.go, store_gateway.go and store_lifecycle.go inside an
// orchestrator. The model was not being inconsistent. It was being told to build
// four components' worth of types into one.
//
// # Why bindings are the right source, and not merely a smaller one
//
// architecture/change-management.md's entire cascade runs on them: a blueprint
// change is assessed by mapping each diff entry against the dependent's
// bindings, and that assessment drives the reconciliation wavefront and the
// blue-green cutover. Generation that ignores bindings does not just over-ask —
// it produces components whose declared consumption does not match their real
// consumption, and change management cannot work on those at all.
//
// # What is NOT done here
//
// A type the sent blueprints reference but no binding declares is REPORTED, not
// added. protocol/orchestrator binds no ErrorResponse and every endpoint returns
// one, which is a gap in the blueprint's contract — and quietly supplying it
// would hide the gap while re-introducing the tooling's judgement about what a
// component needs.

import (
	"sort"
	"strings"
)

// Binding is one declared consumption from a dependency.
//
// Kind distinguishes a type this component implements from a behaviour it
// inherits. Both are real consumption and both belong in the contract, but only
// a type is something the model must define. Conflating them once made
// `content-digest` and `read-without-mutation` — behaviour names — arrive at the
// planner as types that must exist, which is the same fault as demanding all
// fifty-four protocol types, in a smaller place.
type Binding struct {
	From       string   // blueprint the consumption comes from, e.g. "protocol/types"
	Kind       string   // "type" or "behavior"
	Type       string   // type or behaviour name
	FieldsUsed []string // the fields this component actually consumes (types only)
}

// ExtractBindings reads a blueprint's declared dependency contract.
//
// Delegates to a real YAML parser. It previously walked the block line-wise,
// and the reason recorded here was that the file is not YAML — true, and the
// answer was to parse the BLOCK rather than to stop parsing. See yamlblock.go
// for what the line-wise reader could not see, including a `fields_used` list
// wrapped across two lines, which it returned as EMPTY so the fields of
// MetricsInfo, SearchQuery and DataContract reached generation as nothing.
//
// Errors are dropped here, for callers that want only what parsed. Generation
// uses ParseBindings and refuses on error: an unparsed contract is one nobody
// read.
func ExtractBindings(blueprint string) []Binding {
	// A DECLARATION supersedes the `## Dependencies` section it replaced.
	//
	// A migrated blueprint removes that section in the same edit that adds the
	// declaration — leaving both is two statements of one dependency, and the
	// generator reproduces faults faithfully, so fixing one copy leaves the
	// other wrong.
	//
	// Answering here rather than at each call site means every caller —
	// requirements gathering, buildable-component derivation, the validator —
	// gets the same answer without knowing which form a blueprint uses. Three
	// callers read the section directly and returned nothing the moment the
	// orchestrator migrated.
	if d, err := ExtractDeclaration("", blueprint); err == nil && d != nil {
		return d.Bindings()
	}
	bs, _ := ParseBindings("", blueprint)
	return bs
}

// BoundTypes returns the distinct type names a blueprint declares it consumes.
func BoundTypes(bindings []Binding) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range bindings {
		if b.Kind == "behavior" {
			continue // a behaviour is inherited, not defined
		}
		if !seen[b.Type] {
			seen[b.Type] = true
			out = append(out, b.Type)
		}
	}
	sort.Strings(out)
	return out
}

// FormatBindings renders declared consumption for a prompt.
//
// The fields matter as much as the names. "You consume AgentManifest" leaves the
// shape to be guessed at from a 55 KB catalogue; "AgentManifest{name, type,
// version, url, public_key, capabilities, publishes, subscriptions}" is the
// contract.
func FormatBindings(bindings []Binding) string {
	if len(bindings) == 0 {
		return ""
	}
	byBP := map[string][]Binding{}
	var order []string
	for _, b := range bindings {
		if _, seen := byBP[b.From]; !seen {
			order = append(order, b.From)
		}
		byBP[b.From] = append(byBP[b.From], b)
	}
	sort.Strings(order)

	var sb strings.Builder
	sb.WriteString("This component's blueprint declares exactly what it consumes " +
		"from each dependency. Implement these, with these fields:\n")
	for _, bp := range order {
		sb.WriteString("\nFrom " + bp + ":\n")
		for _, b := range byBP[bp] {
			sb.WriteString("  " + b.Type)
			if len(b.FieldsUsed) > 0 {
				sb.WriteString("{" + strings.Join(b.FieldsUsed, ", ") + "}")
			}
			if b.Kind == "behavior" {
				sb.WriteString("  (behaviour to follow, not a type to define)")
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// UnboundTypes returns types the sent blueprints define that no binding declares.
//
// Reported, never added. A component that returns ErrorResponse from every
// endpoint and binds no ErrorResponse has an incomplete contract, and the
// blueprint is where that gets fixed.
func UnboundTypes(defined, bound []string) []string {
	have := map[string]bool{}
	for _, b := range bound {
		have[b] = true
	}
	var out []string
	for _, d := range defined {
		if !have[d] {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}
