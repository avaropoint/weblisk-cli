package dispatch

// One declaration block per blueprint.
//
// # What this replaces
//
// Ten parsers read a blueprint: frontmatter for requires, a YAML block for
// bindings, `### Name` headings for types, `Name:` keys for other types,
// `- name:` entries for behaviours, one table for endpoints, a second reader
// over the same table, another table for store operations, prose for the
// checklist, and a regex over an English sentence for component groups.
//
// Each fact in a different shape, so each needed its own parser, and each
// parser needed calibrating against forms nobody had written down. One rule —
// "does this binding resolve" — reported 389, then 134, then 74 across six
// attempts, purely because a type is declared three ways.
//
// A declaration block is one shape, declared rather than discovered. See
// CONTRACT_BLOCK_PLAN.md.
//
// # Coexistence is deliberate
//
// A blueprint with a declaration is read from it. One without is read the old way.
// Nothing is deleted until the corpus has migrated, so a half-migrated corpus
// still builds — which is what makes this reversible at any point.

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// BlueprintDeclaration is everything a machine reads from a blueprint.
type BlueprintDeclaration struct {
	Requires []DeclaredRequire `yaml:"requires"`
	Declares DeclaredDeclares  `yaml:"declares"`
	Serves   []DeclaredServe   `yaml:"serves"`
	// Checks are machine-verifiable predicates with a DECLARED SUBJECT.
	//
	// # Why the subject is declared and not extracted
	//
	// The structural checker inferred what to verify by pattern-matching an
	// assertion's English prose — pulling type names, JSON keys and routes out
	// of the sentence, then deciding which check applied from whatever it
	// managed to extract. 95 of 132 assertions matched nothing and were graded
	// only by a model's opinion.
	//
	// Worse, it was silently sensitive to formatting. JSON keys are read from
	// BACKTICKED spans, so
	//
	//	`RegisterResponse` includes `agent_id`, `token`
	//
	// verifies, and
	//
	//	RegisterResponse includes agent_id, token
	//
	// extracts no keys, fails the check's `applies` test, and is never checked
	// at all. 35 of 59 route-bearing assertions in the corpus are unbackticked.
	//
	// A check that names its own subject cannot have either fault. The prose
	// checklist stays exactly where it is, as the human-readable statement of
	// expectations; this is the machine-verifiable part, and it does not read
	// English.
	Checks []DeclaredCheck `yaml:"checks"`
}

// DeclaredRequire is one dependency and what this blueprint uses from it.
type DeclaredRequire struct {
	Blueprint string            `yaml:"blueprint"`
	Version   string            `yaml:"version"`
	Bindings  DeclaredBindings  `yaml:"bindings"`
	OnChange  map[string]string `yaml:"on_change"`
}

// DeclaredBindings is what is consumed from a dependency.
type DeclaredBindings struct {
	Types     []DeclaredTypeBinding `yaml:"types"`
	Behaviors []DeclaredNamed       `yaml:"behaviors"`
}

// DeclaredTypeBinding names a type and the fields actually used.
//
// fields_used is at FIELD granularity because that is what makes a change
// assessable: a type gaining a field this component does not read has not
// reached it.
type DeclaredTypeBinding struct {
	Name       string   `yaml:"name"`
	FieldsUsed []string `yaml:"fields_used"`
}

// DeclaredNamed is a behaviour reference or declaration.
type DeclaredNamed struct {
	Name  string   `yaml:"name"`
	Rules []string `yaml:"rules,omitempty"`
}

// DeclaredCheck is one predicate and what it applies to.
//
// `check` names the expectation and is platform-NEUTRAL. HOW it is evaluated is
// the platform's — see the platform blueprint — which is the same split already
// in force for declared names: the architecture names the operation, the
// platform states its spelling.
type DeclaredCheck struct {
	Check   string `yaml:"check"`
	Subject string `yaml:"subject,omitempty"`
	// Type and Keys carry a type check's subject explicitly, so nothing is
	// parsed out of a sentence.
	Type string   `yaml:"type,omitempty"`
	Keys []string `yaml:"keys,omitempty"`
	// Endpoint is "METHOD /path" for a routing check.
	Endpoint string `yaml:"endpoint,omitempty"`
	// Why records the assertion this check exists to verify. Documentation, not
	// a key — a check is not matched to prose by text.
	Why string `yaml:"why,omitempty"`
}

// DeclaredDeclares is what this blueprint defines for others to bind.
type DeclaredDeclares struct {
	// Types maps a type name to its fields. The shape is here, not in prose,
	// because generation needs the wire contract and a reader needs the same
	// thing — two statements of one type is how they stop agreeing.
	Types      map[string]map[string]DeclaredField `yaml:"types"`
	Behaviors  []DeclaredNamed                     `yaml:"behaviors"`
	Operations []string                            `yaml:"operations"`
}

// DeclaredField is one field of a declared type.
type DeclaredField struct {
	Type        string `yaml:"type"`
	Required    bool   `yaml:"required"`
	Description string `yaml:"description,omitempty"`
	// Serialised is false for a field that exists in memory and never on the
	// wire. An em dash in a JSON Key column used to mean this, and was read as
	// a key named "—".
	Serialised *bool `yaml:"serialised,omitempty"`
}

// DeclaredServe is one endpoint this blueprint's component serves.
type DeclaredServe struct {
	Method    string `yaml:"method"`
	Path      string `yaml:"path"`
	Operation string `yaml:"operation"`
	Auth      string `yaml:"auth"`
	Purpose   string `yaml:"purpose,omitempty"`
}

// Wire is the endpoint as requirements list it.
func (c DeclaredServe) Wire() string { return c.Method + " " + c.Path }

// ExtractDeclaration reads a blueprint's declaration block, or nil when it has none.
//
// Returns an error only when a declaration is PRESENT and malformed. An absent
// contract is not an error — it means the blueprint has not migrated, and the
// old parsers answer for it.
func ExtractDeclaration(blueprintPath, blueprint string) (*BlueprintDeclaration, error) {
	for _, block := range YAMLBlocks(blueprint) {
		if !strings.HasPrefix(strings.TrimSpace(block), "declaration:") {
			continue
		}
		var doc struct {
			Declaration BlueprintDeclaration `yaml:"declaration"`
		}
		if err := yaml.Unmarshal([]byte(block), &doc); err != nil {
			return nil, fmt.Errorf("%s: the declaration block does not parse: %w", blueprintPath, err)
		}
		return &doc.Declaration, nil
	}
	return nil, nil
}

// HasDeclaration reports whether a blueprint has migrated.
func HasDeclaration(blueprint string) bool {
	for _, block := range YAMLBlocks(blueprint) {
		if strings.HasPrefix(strings.TrimSpace(block), "declaration:") {
			return true
		}
	}
	return false
}

// Bindings renders a declaration's dependencies in the shape the rest of the
// pipeline already consumes, so a migrated blueprint needs no other changes.
func (c *BlueprintDeclaration) Bindings() []Binding {
	var out []Binding
	for _, r := range c.Requires {
		for _, t := range r.Bindings.Types {
			out = append(out, Binding{From: r.Blueprint, Kind: "type", Type: t.Name, FieldsUsed: t.FieldsUsed})
		}
		for _, b := range r.Bindings.Behaviors {
			out = append(out, Binding{From: r.Blueprint, Kind: "behavior", Type: b.Name})
		}
	}
	return out
}

// EndpointOperations renders what this component serves.
func (c *BlueprintDeclaration) EndpointOperations() []EndpointOperation {
	var out []EndpointOperation
	for _, s := range c.Serves {
		out = append(out, EndpointOperation{
			Method:    strings.ToUpper(s.Method),
			Path:      s.Path,
			Operation: s.Operation,
		})
	}
	return out
}

// DeclaredNames is every name this blueprint defines — the single answer to a
// question that previously had three.
func (c *BlueprintDeclaration) DeclaredNames() []string {
	var out []string
	for name := range c.Declares.Types {
		out = append(out, name)
	}
	for _, b := range c.Declares.Behaviors {
		out = append(out, b.Name)
	}
	out = append(out, c.Declares.Operations...)
	return out
}
