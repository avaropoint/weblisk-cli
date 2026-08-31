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
	"regexp"
	"sort"
	"strings"
)

// Binding is one declared consumption: a type, and the fields used from it.
type Binding struct {
	From       string   // blueprint the type comes from, e.g. "protocol/types"
	Type       string   // type name
	FieldsUsed []string // the fields this component actually consumes
}

var (
	reBindBlueprint = regexp.MustCompile(`^\s*-\s*blueprint:\s*(\S+)`)
	reBindName      = regexp.MustCompile(`^\s*-\s*name:\s*(\S+)`)
	reBindFields    = regexp.MustCompile(`^\s*fields_used:\s*\[([^\]]*)\]`)
	reBindSection   = regexp.MustCompile(`^\s*(types|endpoints|events|config|patterns):\s*$`)
)

// ExtractBindings reads the Dependencies block's declared type bindings.
//
// Parsed line-wise rather than as YAML: the block is fenced markdown inside a
// document, the surrounding text is not YAML, and a full parse would fail on the
// file rather than on the block. Only the `types:` section is read — endpoints,
// events and config bindings describe consumption this generator does not yet
// act on, and inventing meaning for them would be the same overreach in a new
// place.
func ExtractBindings(blueprint string) []Binding {
	i := strings.Index(blueprint, "## Dependencies")
	if i < 0 {
		return nil
	}
	rest := blueprint[i:]
	if end := strings.Index(rest[len("## Dependencies"):], "\n## "); end >= 0 {
		rest = rest[:end+len("## Dependencies")]
	}

	var out []Binding
	currentBP := ""
	section := ""
	var pending *Binding
	flush := func() {
		if pending != nil {
			out = append(out, *pending)
			pending = nil
		}
	}
	for _, line := range strings.Split(rest, "\n") {
		if m := reBindBlueprint.FindStringSubmatch(line); m != nil {
			flush()
			currentBP = m[1]
			section = ""
			continue
		}
		if m := reBindSection.FindStringSubmatch(line); m != nil {
			flush()
			section = m[1]
			continue
		}
		if section != "types" {
			continue
		}
		if m := reBindName.FindStringSubmatch(line); m != nil {
			flush()
			pending = &Binding{From: currentBP, Type: m[1]}
			continue
		}
		if m := reBindFields.FindStringSubmatch(line); m != nil && pending != nil {
			for _, f := range strings.Split(m[1], ",") {
				if f = strings.TrimSpace(f); f != "" {
					pending.FieldsUsed = append(pending.FieldsUsed, f)
				}
			}
		}
	}
	flush()
	return out
}

// BoundTypes returns the distinct type names a blueprint declares it consumes.
func BoundTypes(bindings []Binding) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range bindings {
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
