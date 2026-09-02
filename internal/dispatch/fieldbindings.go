package dispatch

// Checking that a declared binding names a field the type actually has.
//
// Generation reads bindings to decide what a component must implement. A
// binding naming a field its type does not define therefore instructs the model
// to implement something the protocol does not have — and the model complies.
//
// This was found by a clean generation failing L1-01. architecture/observability
// bound HealthStatus as [status, component, version, uptime_seconds, checks] and
// asserted those names in its checklist; protocol/types defines the type with
// name, status, version, uptime, metrics, timestamp. The generated orchestrator
// satisfied observability and failed the type definition. It could not do both.
//
// A sweep then found the same class in numbers: ErrorResponse bound as `message`
// in twenty-five blueprints when the field is `error`, ServiceDirectory bound as
// `agents` when the field is `services`, HealthStatus bound as `component` and
// `uptime_seconds` in every platform blueprint. That is the generator of the
// wire mismatches that were being fixed one at a time in generated code.

import (
	"regexp"
	"sort"
	"strings"
)

var (
	// reTypeDefHeading opens a type definition: "### AgentManifest".
	reTypeDefHeading = regexp.MustCompile(`(?m)^### ([A-Z][A-Za-z0-9]+)\s*$`)
	// reFieldRow reads the JSON key column of a field table:
	//
	//	| Name | string | `name` | yes | Component name |
	reFieldRow = regexp.MustCompile("(?m)^\\|\\s*\\w[\\w ]*\\s*\\|[^|]*\\|\\s*`?([a-z_][a-z0-9_]*)`?\\s*\\|")
)

// TypeFields indexes every type definition in a corpus to the JSON keys it
// declares.
//
// Definitions of one name in several blueprints are UNIONED rather than
// resolved. That is deliberate: this function reports what the corpus says a
// type has, and two blueprints defining one type differently is a separate
// fault which unioning makes visible instead of silently preferring one.
func TypeFields(blueprints map[string]string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, body := range blueprints {
		heads := reTypeDefHeading.FindAllStringSubmatchIndex(body, -1)
		for i, h := range heads {
			name := body[h[2]:h[3]]
			end := len(body)
			if i+1 < len(heads) {
				end = heads[i+1][0]
			}
			section := body[h[1]:end]
			for _, row := range reFieldRow.FindAllStringSubmatch(section, -1) {
				if out[name] == nil {
					out[name] = map[string]bool{}
				}
				out[name][row[1]] = true
			}
		}
	}
	return out
}

// FieldBindingFault is one binding naming a field its type does not define.
type FieldBindingFault struct {
	Blueprint string
	Type      string
	Fields    []string
}

// Key is the stable identity of a fault, for pinning.
func (f FieldBindingFault) Key() string {
	return f.Blueprint + " " + f.Type + " " + strings.Join(f.Fields, ",")
}

// CheckFieldBindings reports every binding whose fields_used names something the
// type does not define.
//
// A type with no indexed fields is skipped, not reported: an unparsed table is
// this checker's gap and not the blueprint's fault, and reporting it would bury
// the real faults among noise.
func CheckFieldBindings(blueprints map[string]string) []FieldBindingFault {
	types := TypeFields(blueprints)
	var out []FieldBindingFault
	for path, body := range blueprints {
		for _, b := range ExtractBindings(body) {
			if b.Kind == "behavior" || len(b.FieldsUsed) == 0 {
				continue
			}
			known := types[b.Type]
			if len(known) == 0 {
				continue
			}
			var missing []string
			for _, f := range b.FieldsUsed {
				if !known[f] {
					missing = append(missing, f)
				}
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				out = append(out, FieldBindingFault{Blueprint: path, Type: b.Type, Fields: missing})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}
