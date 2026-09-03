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
	// reTypeDefHeading opens a type definition: "### AgentManifest", and also
	// "### AgentEntry (internal)" — a heading whose suffix is commentary. An
	// anchored regex rejected the second and manufactured a false accusation
	// against every consumer of that type.
	reTypeDefHeading = regexp.MustCompile(`(?m)^### ([A-Z][A-Za-z0-9]+)(?:\s+\([^)]*\))?\s*$`)
	// reFieldRow reads the JSON key column of a field table:
	//
	//	| Name | string | `name` | yes | Component name |
	// reFieldRow reads a field table row. Two shapes are in the corpus:
	//
	//	| Name | string | `name` | yes | Component name |   ← json key in column 3
	//	| LastSeen | int64 | Last health check timestamp |  ← no json key column
	//
	// Both columns are harvested: the declared JSON key when present, and the
	// snake_case of the field name always. Reading only column three missed
	// every three-column table and accused their consumers.
	reFieldRow = regexp.MustCompile("(?m)^\\|\\s*([A-Za-z][A-Za-z0-9_ ]*?)\\s*\\|([^|\\n]*)\\|([^|\\n]*)(\\|)?")
	// reYAMLTypeKey opens a YAML type block: "  ScopeDeclaration:".
	reYAMLTypeKey = regexp.MustCompile(`^(\s+)([A-Z][A-Za-z0-9]+):\s*$`)
	// reYAMLListField is a field declared as a list entry: "- name: context".
	reYAMLListField = regexp.MustCompile(`^\s*-\s*name:\s*([a-z_][a-z0-9_]*)\s*$`)
	// reYAMLMapField is a field declared as a key: "      algorithm:".
	reYAMLMapField = regexp.MustCompile(`^\s+([a-z_][a-z0-9_]*):\s*$`)
)

// TypeFields indexes every type definition in a corpus to the field names it
// declares.
//
// Three shapes, because the corpus uses three and reading only one produces
// FALSE accusations. A checker that missed the YAML list form reported
// architecture/content as binding a field ScopeDeclaration does not have, when
// patterns/scope declares it as `- name: context` — and acting on that would
// have broken a correct blueprint to satisfy a broken checker.
//
//	markdown table   | Level | ScopeLevel | `level` | yes | ... |
//	yaml list        - name: context
//	yaml map           algorithm:
//
// Definitions of one name in several blueprints are UNIONED rather than
// resolved. That is deliberate: this reports what the corpus says a type has,
// and two blueprints defining one type differently is a separate fault which
// unioning makes visible instead of silently preferring one.
func TypeFields(blueprints map[string]string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	add := func(typ, field string) {
		if out[typ] == nil {
			out[typ] = map[string]bool{}
		}
		out[typ][field] = true
	}

	for _, body := range blueprints {
		// Shape 1: "### TypeName" followed by a field table.
		heads := reTypeDefHeading.FindAllStringSubmatchIndex(body, -1)
		for i, h := range heads {
			name := body[h[2]:h[3]]
			end := len(body)
			if i+1 < len(heads) {
				end = heads[i+1][0]
			}
			for _, row := range reFieldRow.FindAllStringSubmatch(body[h[1]:end], -1) {
				col1 := strings.TrimSpace(row[1])
				if col1 == "" || col1 == "Field" || col1 == "Name" && strings.Contains(row[3], "Description") {
					continue // the header row
				}
				add(name, snakeCase(col1))
				if k := jsonKeyColumn(row[3]); k != "" {
					add(name, k)
				}
			}
		}
		// Shapes 2 and 3: a YAML type block, whose name is a key rather than a
		// heading. Tracked by indentation because a type's body ends where the
		// indentation returns to the type's own level.
		var typ string
		var typIndent int
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			indent := len(line) - len(strings.TrimLeft(line, " \t"))
			if m := reYAMLTypeKey.FindStringSubmatch(line); m != nil {
				typ, typIndent = m[2], indent
				continue
			}
			if typ == "" {
				continue
			}
			if indent <= typIndent {
				typ = "" // dedented out of the type body
				continue
			}
			if m := reYAMLListField.FindStringSubmatch(line); m != nil {
				add(typ, m[1])
				continue
			}
			if m := reYAMLMapField.FindStringSubmatch(line); m != nil {
				add(typ, m[1])
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
// bindingsFrom supplies the contracts to check; typesFrom supplies the type
// definitions to check them against. They differ: only a component's OWN
// bindings reach the model, but a type it binds may be defined anywhere in the
// corpus, so checking a contract against its own file alone would accuse every
// cross-blueprint binding there is.
func CheckFieldBindings(bindingsFrom, typesFrom map[string]string) []FieldBindingFault {
	if typesFrom == nil {
		typesFrom = bindingsFrom
	}
	types := TypeFields(typesFrom)
	var out []FieldBindingFault
	for path, body := range bindingsFrom {
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

// snakeCase converts a declared field name to its conventional JSON key:
// LastSeen → last_seen. Used for tables that declare no JSON key column.
func snakeCase(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	var b strings.Builder
	for i := 0; i < len(r); i++ {
		c := r[i]
		if c == ' ' {
			b.WriteByte('_')
			continue
		}
		if c >= 'A' && c <= 'Z' {
			// A boundary opens before a capital that follows a lower-case letter,
			// or that begins a new word after an acronym: ChannelID → channel_id,
			// not channel_i_d, and TargetURL → target_url.
			prevLower := i > 0 && r[i-1] >= 'a' && r[i-1] <= 'z'
			nextLower := i+1 < len(r) && r[i+1] >= 'a' && r[i+1] <= 'z'
			prevUpper := i > 0 && r[i-1] >= 'A' && r[i-1] <= 'Z'
			if i > 0 && r[i-1] != ' ' && (prevLower || (prevUpper && nextLower)) {
				b.WriteByte('_')
			}
			b.WriteRune(c - 'A' + 'a')
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// jsonKeyColumn returns the JSON key a table cell declares, or "".
func jsonKeyColumn(cell string) string {
	cell = strings.TrimSpace(cell)
	cell = strings.Trim(cell, "`")
	if cell == "" || strings.ContainsAny(cell, " |") {
		return "" // prose, not a key
	}
	for _, r := range cell {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return ""
		}
	}
	return cell
}
