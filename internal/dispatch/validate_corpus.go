package dispatch

// Validating the blueprint corpus against its own schemas.
//
// # Why this is a product command and not a test
//
// Every rule below existed as a hand-written Go test. That is the wrong place
// twice over:
//
//   - A blueprint author cannot run it. The person who can fix a corpus fault
//     had no way to find one, so faults were discovered by a forty-minute
//     generation run failing at file 27.
//   - It made the tooling the authority. A check written in Go is an opinion
//     about what a blueprint should contain, and when the schema changed the
//     opinion did not. That is how a build was killed by a checker enforcing a
//     convention the blueprints had already replaced.
//
// So the rules are DERIVED. Each schema states its own required sections in a
// "Required Section Order" table — 23 rows for agent, 14 for architecture, 27
// for domain — and this reads that table. Adding a required section to a schema
// makes every blueprint of that type answerable for it with no code change, and
// no code here can require something a schema does not.
//
// What remains in code is only what a schema cannot express about ITSELF:
// whether two blueprints agree, and whether a declared surface has a server.
// Those are relationships between documents, and no single document can state
// them.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Severity separates a fault from an observation.
type Severity string

const (
	// SeverityFault — the corpus contradicts its own schema. Generation from it
	// produces an artifact nobody specified.
	SeverityFault Severity = "fault"
	// SeverityWarning — worth a person's attention, not worth refusing a build.
	SeverityWarning Severity = "warning"
)

// Finding is one thing wrong with the corpus.
type Finding struct {
	Blueprint string // the file, relative to its source
	Rule      string // which rule, named as the schema names it
	Detail    string // what is wrong, in terms the author can act on
	Authority string // the schema or blueprint that states the rule
	Severity  Severity
}

func (f Finding) String() string {
	return fmt.Sprintf("%s: %s — %s (%s)", f.Blueprint, f.Rule, f.Detail, f.Authority)
}

// SectionRule is one row of a schema's Required Section Order table.
type SectionRule struct {
	Number   int
	Name     string
	Heading  string // the literal heading, e.g. "## Endpoints"
	Form     string // "narrative" | "table" | "yaml" | "yaml:<root>"
	Required bool   // "Yes" — Conditional and Optional are not enforced here
}

// reSectionRow reads a Required Section Order row:
//
//	| 7 | Endpoints | `## Endpoints` | Conditional | HTTP surface … |
// SchemaSections is read from the table by COLUMN NAME, not by position.
//
// The table gained a Form column and every positional reader would have shifted
// silently — the same fault that made every protected endpoint in the corpus
// read as unprotected when Operation was added to the endpoints table.

// SchemaSections reads the sections a schema requires, in order.
//
// Returns nil when the schema states no such table — compliance, config and
// standard describe content rather than component structure, and inventing
// requirements for them would be this file having an opinion.
func SchemaSections(schema string) []SectionRule {
	start := headingIndex(schema, "## Required Section Order")
	if start < 0 {
		return nil
	}
	body := schema[start:]
	if end := strings.Index(body[1:], "\n## "); end >= 0 {
		body = body[:end+1]
	}
	var out []SectionRule
	for _, t := range TablesWithColumns(body, "heading") {
		for _, row := range t.Rows {
			heading := strings.Trim(strings.TrimSpace(row["heading"]), "`")
			if !strings.HasPrefix(heading, "#") {
				// Frontmatter and Title name a construct, not a heading.
				continue
			}
			n := 0
			fmt.Sscanf(strings.TrimSpace(row["#"]), "%d", &n)
			form := strings.TrimSpace(row["form"])
			if form == "" {
				// A schema not yet carrying the column imposes nothing new.
				form = "narrative"
			}
			out = append(out, SectionRule{
				Number:   n,
				Name:     strings.TrimSpace(row["section"]),
				Heading:  heading,
				Form:     form,
				Required: strings.Contains(strings.ToLower(row["required"]), "yes"),
			})
		}
	}
	return out
}

// blueprintType reads a blueprint's declared type from frontmatter.
func blueprintType(blueprint string) string {
	end := strings.Index(blueprint, "-->")
	if end < 0 {
		return ""
	}
	m := regexp.MustCompile(`(?m)^type:\s*([a-z]+)`).FindStringSubmatch(blueprint[:end])
	if m == nil {
		return ""
	}
	return m[1]
}

// ValidateCorpus checks every blueprint against the schema for its type, and
// checks the relationships no single blueprint can state about itself.
//
// schemas is keyed as "schemas/agent.md"; corpus is every other blueprint.
func ValidateCorpus(corpus map[string]string) []Finding {
	findings := ValidateStructure(corpus)
	findings = append(findings, validateDeclaredNames(corpus)...)
	findings = append(findings, validateEndpointServers(corpus)...)
	findings = append(findings, validateSectionConstructs(corpus)...)
	findings = append(findings, validateBindingsResolve(corpus)...)
	findings = append(findings, validateNoMechanismInSpecification(corpus)...)
	findings = append(findings, validateChecklistBackticks(corpus)...)
	findings = append(findings, validateNoRestatedRules(corpus)...)
	return findings
}

// validateNoRestatedRules — one requirement, one blueprint.
//
// A normative sentence appearing verbatim in two blueprints is two rules as
// soon as one is edited, and the copy that is updated alone is the one an
// implementation follows. Four were found: a path-literal prohibition in four
// platform blueprints, a 405/404 requirement in four more, an unfilled-slot
// rule in three, and password hashing in two patterns plus a second time
// within one of them.
//
// Every one arose the same way — the requirement was translated again instead
// of referenced — so the fix is always the same: the blueprint that OWNS the
// subject states it, and the others link to it.
//
// Exact matches only, and only sentences carrying MUST. A near-duplicate is a
// judgement this cannot make, and a paragraph that merely resembles another is
// usually two documents describing one thing from their own side, which is
// what a blueprint is for.
func validateNoRestatedRules(corpus map[string]string) []Finding {
	where := map[string]map[string]bool{}
	for _, name := range sortedKeys(corpus) {
		if strings.HasPrefix(name, "schemas/") {
			// A schema states rules ABOUT blueprints; a blueprint restating one
			// is the fault, and the schema is not a second copy of it.
			continue
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(corpus[name], "\n") {
			l := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*|> "))
			if len(l) < 45 || !strings.Contains(l, "MUST") || seen[l] {
				continue
			}
			seen[l] = true
			if where[l] == nil {
				where[l] = map[string]bool{}
			}
			where[l][name] = true
		}
	}
	var findings []Finding
	for _, sentence := range sortedKeys(flatten(where)) {
		files := where[sentence]
		if len(files) < 2 {
			continue
		}
		var names []string
		for f := range files {
			names = append(names, f)
		}
		sort.Strings(names)
		findings = append(findings, Finding{
			Blueprint: names[0], Rule: "one requirement, one blueprint",
			Detail: fmt.Sprintf("stated verbatim in %s — the blueprint that owns the subject "+
				"states it and the others link to it: %q",
				strings.Join(names, " and "), truncate(sentence, 90)),
			Authority: "schemas/common.md#declared-names",
			Severity:  SeverityFault,
		})
	}
	return findings
}

// flatten makes a set-of-sets keyable by sortedKeys.
func flatten(m map[string]map[string]bool) map[string]string {
	out := make(map[string]string, len(m))
	for k := range m {
		out[k] = ""
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ValidateStructure checks each blueprint against the schema for its type.
//
// # Why this is separate from the relationship rules
//
// A structure rule is answerable from ONE blueprint and the schema that governs
// it, so it is correct against any subset of the corpus. A relationship rule —
// "does anything serve this endpoint" — is only answerable against the WHOLE
// corpus.
//
// Running the relationship rules over a scoped set got it wrong immediately:
// the orchestrator's graph does not include architecture/agent, so
// /v1/describe, /v1/execute, /v1/message and /v1/event were reported as served
// by nobody. They are served by the agent. Four confident wrong findings on
// every build is how a check earns being ignored.
//
// So the build pre-flight runs THIS, and `weblisk validate` — which loads
// everything — runs both.
func ValidateStructure(corpus map[string]string) []Finding {
	var findings []Finding

	// Derived from each schema's own Required Section Order table.
	for _, name := range sortedKeys(corpus) {
		body := corpus[name]
		if strings.HasPrefix(name, "schemas/") || strings.HasSuffix(name, "README.md") {
			continue
		}
		// A document with no frontmatter is not a blueprint.
		//
		// standards/ holds nine of them — schemas/standard describes those as
		// guidance for developer-authored project blueprints, a different kind
		// of document, and none of the nine carries frontmatter. Demanding a
		// `type:` from them reported nine faults against documents that are
		// exactly what their own schema says they should be.
		if !strings.Contains(body, "<!-- blueprint") {
			continue
		}
		t := blueprintType(body)
		if t == "" {
			findings = append(findings, Finding{
				Blueprint: name, Rule: "frontmatter type",
				Detail:    "has a blueprint frontmatter block with no `type:`, so no schema governs it",
				Authority: "schemas/common.md#frontmatter", Severity: SeverityFault,
			})
			continue
		}
		schema, ok := corpus["schemas/"+t+".md"]
		if !ok {
			continue // a type with no schema is not this rule's business
		}
		for _, sec := range SchemaSections(schema) {
			if !sec.Required {
				continue
			}
			if !sectionPresent(body, sec.Heading) {
				findings = append(findings, Finding{
					Blueprint: name, Rule: "required section",
					Detail:    fmt.Sprintf("missing %s (%s)", sec.Heading, sec.Name),
					Authority: "schemas/" + t + ".md#required-section-order",
					Severity:  SeverityFault,
				})
			}
		}
	}

	return findings
}

// validateDeclaredNames — every endpoint row carries the Operation that every
// generated symbol is spelled from.
//
// A relationship rule: the schema can require the column, but only a reader of
// the whole corpus can see that a row left it empty.
func validateDeclaredNames(corpus map[string]string) []Finding {
	var findings []Finding
	for _, name := range sortedKeys(corpus) {
		if !strings.HasPrefix(name, "architecture/") && !strings.HasPrefix(name, "agents/") {
			continue
		}
		for _, e := range ExtractEndpointOperations(corpus[name]) {
			if e.Operation == "" {
				findings = append(findings, Finding{
					Blueprint: name, Rule: "declared names",
					Detail: fmt.Sprintf("%s has no Operation — nothing can derive a handler or "+
						"constant name from it, so a generator will invent one and invent a "+
						"different one next time", e.Wire()),
					Authority: "schemas/common.md#declared-names",
					Severity:  SeverityFault,
				})
			}
		}
	}
	return findings
}

// validateEndpointServers — a declared platform endpoint has a serving
// component, unless its blueprint declares the surface opt-in.
func validateEndpointServers(corpus map[string]string) []Finding {
	served := map[string]bool{}
	for name, body := range corpus {
		if !strings.HasPrefix(name, "architecture/") && !strings.HasPrefix(name, "agents/") {
			continue
		}
		for _, e := range ExtractEndpointOperations(body) {
			served[normalisePath(e.Path)] = true
		}
	}
	var findings []Finding
	for _, name := range sortedKeys(corpus) {
		body := corpus[name]
		if strings.HasPrefix(name, "architecture/") || strings.HasPrefix(name, "schemas/") {
			continue
		}
		if strings.Contains(body, "\n## Endpoints") || AdoptionOf(body) == "opt-in" {
			continue
		}
		seen := map[string]bool{}
		for _, tab := range TablesWithColumns(body, "path", "method") {
			for _, row := range tab.Rows {
				p := normalisePath(strings.Trim(strings.TrimSpace(row["path"]), "`"))
				// Only platform paths. An unprefixed path in a pattern is an
				// application surface a tenant adopts, generated per adoption —
				// patterns/api-rest declaring "/M" is the proof.
				if !strings.HasPrefix(p, "/v1/") || seen[p] || served[p] {
					continue
				}
				seen[p] = true
				findings = append(findings, Finding{
					Blueprint: name, Rule: "declared endpoint has a server",
					Detail: fmt.Sprintf("%s is declared here and no component's Endpoints table "+
						"serves it — either a component must claim it, or this blueprint must "+
						"declare `adoption: opt-in`", p),
					Authority: "schemas/architecture.md#endpoints-endpoints",
					Severity:  SeverityFault,
				})
			}
		}
	}
	return findings
}

// normalisePath makes ":name" and "{name}" the same path.
func normalisePath(p string) string {
	out := make([]string, 0, 8)
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ":") || (strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")) {
			seg = "{}"
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/")
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// FormatFindings renders findings for a person, grouped by blueprint.
func FormatFindings(findings []Finding) string {
	if len(findings) == 0 {
		return ""
	}
	byFile := map[string][]Finding{}
	for _, f := range findings {
		byFile[f.Blueprint] = append(byFile[f.Blueprint], f)
	}
	var files []string
	for k := range byFile {
		files = append(files, k)
	}
	sort.Strings(files)
	var b strings.Builder
	for _, file := range files {
		fmt.Fprintf(&b, "  %s\n", file)
		for _, f := range byFile[file] {
			fmt.Fprintf(&b, "    [%s] %s\n           %s\n", f.Severity, f.Rule, f.Detail)
			fmt.Fprintf(&b, "           stated by %s\n", f.Authority)
		}
	}
	return b.String()
}

// Faults counts findings that contradict a schema.
func Faults(findings []Finding) int {
	n := 0
	for _, f := range findings {
		if f.Severity == SeverityFault {
			n++
		}
	}
	return n
}

// sectionPresent reports whether a blueprint carries a required heading.
//
// A LEVEL-1 heading in a schema is a template for the document's title — the
// row reads `# Name`, and the actual heading is "# Cron Agent". Checking for
// the literal reported every agent in the corpus as missing its title, which
// is a validator with an opinion about a name no schema states.
//
// Level-2 and deeper headings ARE literal: "## Overview" is the section's name,
// not a placeholder for one.
func sectionPresent(body, heading string) bool {
	if strings.HasPrefix(heading, "# ") {
		return regexp.MustCompile(`(?m)^# \S`).MatchString(body)
	}
	return strings.Contains(body, "\n"+heading+"\n")
}

// reSectionSpec finds a schema's specification of one section:
//
//	### Security (`## Security`)
var reSectionSpec = regexp.MustCompile("(?m)^#{3}\\s+[^(\\n]+\\(`(#{2}[^`]+)`\\)\\s*$")

// reYAMLRoot finds the root key of a YAML block: "security:" at column 0.
var reYAMLRoot = regexp.MustCompile("(?m)^([a-z_]+):\\s*$")

// SectionConstructs reads which sections a schema specifies as a YAML block,
// and the root key that block must have.
//
// # Why this is read rather than listed
//
// schemas/architecture specifies `## Security` as a YAML construct —
// trust_model, boundaries, enforcement — and I wrote the orchestrator's as
// prose with markdown tables. It satisfied "the heading exists" and satisfied
// nothing else, and the validator had no opinion because it only checked
// headings.
//
// A prose section cannot be validated, compared between components, or read by
// anything but a person. That is the whole reason the schema states a
// construct, and a checker that accepts any content under the right heading
// makes the construct advisory.
//
// The schema shows the construct in a fenced yaml block inside its own
// specification of the section, so the requirement is already written down —
// it just was not being read.
func SectionConstructs(schema string) map[string]string {
	out := map[string]string{}
	locs := reSectionSpec.FindAllStringSubmatchIndex(schema, -1)
	// End at the NEXT HEADING OF ANY KIND, not the next section spec.
	//
	// Ending at the next spec let a schema's other content bleed in: the
	// Verification Checklist spec has no construct of its own, so the scan ran
	// on and picked up a `requires:` block belonging to something else. Every
	// blueprint was then told its checklist was missing a requires: block.
	// Fifteen findings, all wrong, from one boundary.
	nextHeading := regexp.MustCompile(`(?m)^#{1,6}\s`)
	for _, loc := range locs {
		heading := schema[loc[2]:loc[3]]
		rest := schema[loc[1]:]
		end := len(rest)
		if m := nextHeading.FindStringIndex(rest); m != nil {
			end = m[0]
		}
		body := rest[:end]
		// The first fenced yaml block in this section's specification.
		fence := regexp.MustCompile("(?s)```yaml\\n(.*?)```").FindStringSubmatch(body)
		if fence == nil {
			continue
		}
		if root := reYAMLRoot.FindStringSubmatch(fence[1]); root != nil {
			out[heading] = root[1]
		}
	}
	return out
}

// validateSectionConstructs — a section carries the FORM its schema declares.
//
// # Why the form is read and no longer inferred
//
// This inferred the required construct from the first fenced block in the
// schema's own prose. It mis-associated `## Verification Checklist` with a
// `requires:` block and reported fifteen faults that were not faults, and it
// asserted YAML over sections the corpus documents as tables — asking the
// schema to overrule practice, which is backwards.
//
// The corpus is the master. The Form column was derived by counting what
// blueprints of each type actually do, so this now checks a blueprint against
// its own corpus's convention rather than against an aspiration.
func validateSectionConstructs(corpus map[string]string) []Finding {
	var findings []Finding
	for _, name := range sortedKeys(corpus) {
		body := corpus[name]
		if strings.HasPrefix(name, "schemas/") || strings.HasSuffix(name, "README.md") {
			continue
		}
		t := blueprintType(body)
		schema, ok := corpus["schemas/"+t+".md"]
		if t == "" || !ok {
			continue
		}
		for _, sec := range SchemaSections(schema) {
			if sec.Form == "narrative" {
				continue // nothing to check: prose is prose
			}
			section := sectionBody(body, sec.Heading)
			if section == "" {
				continue // absent is the required-section rule's finding
			}
			if detail := formViolation(section, sec.Form); detail != "" {
				findings = append(findings, Finding{
					Blueprint: name, Rule: "section form",
					Detail:    fmt.Sprintf("%s %s", sec.Heading, detail),
					Authority: "schemas/" + t + ".md#required-section-order",
					Severity:  SeverityFault,
				})
			}
		}
	}
	return findings
}

// formViolation reports how a section fails its declared form, "" if it does not.
func formViolation(section, form string) string {
	switch {
	case form == "table":
		if !reMarkdownTable.MatchString(section) {
			return "is declared `table` and carries no markdown table — a table is read by " +
				"column name, and prose cannot be read at all"
		}
	case form == "structured":
		// Either machine-readable form. The section's SHAPE differs between
		// components — a flat settings list is a table, a nested policy tree is
		// YAML — and the guarantee that matters is that it is not prose.
		if !reMarkdownTable.MatchString(section) && !reYAMLFence.MatchString(section) {
			return "is declared `structured` and is prose — it must be a table or a " +
				"fenced yaml block, because prose cannot be read by anything but a person"
		}
	case form == "yaml":
		if !reYAMLFence.MatchString(section) {
			return "is declared `yaml` and carries no fenced yaml block with a root key"
		}
	case strings.HasPrefix(form, "yaml:"):
		root := strings.TrimPrefix(form, "yaml:")
		// "root:" followed by a nested block OR a scalar on the same line.
		// Requiring end-of-line rejected `override_policy: supervised`, which is
		// how eleven agent blueprints legitimately write it — the check was
		// stricter than the derivation that produced the requirement, so the
		// validator demanded a shape the corpus had never used.
		if !regexp.MustCompile("(?m)^" + regexp.QuoteMeta(root) + ":(\\s|$)").MatchString(section) {
			return fmt.Sprintf("is declared `yaml:%s` and carries no `%s:` block", root, root)
		}
	}
	return ""
}

var (
	reMarkdownTable = regexp.MustCompile(`(?m)^\|[^\n]+\|\n\|[\s:\-|]+\|`)
	reYAMLFence     = regexp.MustCompile("(?m)^```yaml\n[a-z_]+:")
)

// sectionBody returns the text under a heading, up to the next heading of the
// same level, or "" when the heading is absent.
func sectionBody(blueprint, heading string) string {
	i := headingIndex(blueprint, heading)
	if i < 0 {
		return ""
	}
	rest := blueprint[i+len(heading):]
	level := strings.Repeat("#", len(heading)-len(strings.TrimLeft(heading, "#")))
	if end := strings.Index(rest, "\n"+level+" "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// validateBindingsResolve — a binding names something the target blueprint
// actually declares.
//
// # A binding is not a wish, and this half was unchecked
//
// The pipeline already refuses a binding whose `fields_used` names a field a
// type does not define. It never checked whether the TYPE or BEHAVIOUR existed
// at all.
//
// Eight blueprints bind `architecture/storage` with `behavior: sqlite-engine`.
// No blueprint declares a behaviour by that name — architecture/storage
// declares no contracts at all — so all eight bind nothing, and did so while
// naming a storage engine that architecture/storage's own Design Principle 6
// forbids any blueprint from requiring.
//
// A binding that resolves to nothing is worse than an absent one: it reads as a
// stated dependency, it survives review, and the thing it claims to depend on
// was never specified.
func validateBindingsResolve(corpus map[string]string) []Finding {
	// What each blueprint declares, by name: types from its Types section,
	// behaviours from its Contracts section.
	// ExtractTypes, not a regex written here.
	//
	// The first version matched "- name: X" inside `## Types` and `## Contracts`.
	// protocol/types has NO `## Types` section — it declares types as `### Name`
	// headings — so nearly every binding in the corpus resolved to nothing and
	// the rule reported 389 faults, none of them real.
	//
	// ExtractTypes already answers "what does this blueprint declare", and asking
	// it is the same discipline this rule exists to enforce: one question, one
	// implementation.
	declares := map[string]map[string]bool{}
	for name, body := range corpus {
		key := strings.TrimSuffix(name, ".md")
		set := map[string]bool{}
		for _, t := range declaredNames(body) {
			set[t] = true
		}
		declares[key] = set
	}

	var findings []Finding
	for _, name := range sortedKeys(corpus) {
		if strings.HasPrefix(name, "schemas/") {
			continue
		}
		bindings, _ := ParseBindings(name, corpus[name])
		for _, b := range bindings {
			target, ok := declares[b.From]
			if !ok {
				continue // the target is not in this corpus; not this rule's business
			}
			if len(target) == 0 {
				findings = append(findings, Finding{
					Blueprint: name, Rule: "a binding is not a wish",
					Detail: fmt.Sprintf("binds %s %q from %s, which declares no types and no "+
						"contracts — there is nothing there to bind", b.Kind, b.Type, b.From),
					Authority: "architecture/generation.md", Severity: SeverityFault,
				})
				continue
			}
			if !target[b.Type] {
				findings = append(findings, Finding{
					Blueprint: name, Rule: "a binding is not a wish",
					Detail: fmt.Sprintf("binds %s %q from %s, which does not declare it",
						b.Kind, b.Type, b.From),
					Authority: "architecture/generation.md", Severity: SeverityFault,
				})
			}
		}
	}
	return findings
}

// declaredNames is every type and behaviour a blueprint declares, in ALL THREE
// forms the corpus uses.
//
// This is the part that has to be complete rather than clever. It began as one
// regex over `- name:` and reported 389 faults; then as ExtractTypes alone —
// which reads `### TypeName` headings and is correct for protocol/types, the
// document it was written for — and reported 134, because protocol/identity
// declares `SigningKeyPair:` as a mapping key inside a types block instead.
//
// A rule that asks "does the corpus declare this name" is only as good as its
// knowledge of how the corpus declares names. The three forms:
//
//	### TypeName            a heading            protocol/types
//	TypeName:               a key in types:      protocol/identity
//	- name: TypeName        a list entry         most bindings and contracts
func declaredNames(blueprint string) []string {
	var out []string
	out = append(out, ExtractTypes(blueprint)...)
	// Keys of a types: block — two-space indented, PascalCase, ending in a colon.
	for _, m := range reTypesBlock.FindAllStringSubmatch(blueprint, -1) {
		for _, k := range reTypeKey.FindAllStringSubmatch(m[1], -1) {
			out = append(out, k[1])
		}
	}
	out = append(out, declaredBehaviours(blueprint)...)
	// A `- name:` entry is NOT counted here. It appears inside `bindings:`
	// far more often than inside a declaration, and counting one made every
	// type appear declared by whoever CONSUMED it — so a binding resolved
	// against another blueprint's binding and the rule under-reported.
	return out
}

var (
	// A `types:` block at the TOP of a fenced block declares. A `types:` nested
	// under `requires:` -> `bindings:` consumes, and counting a consumption as a
	// declaration made every bound type appear declared by whoever bound it —
	// which would have hidden every genuine wish in the corpus.
	reTypesBlock = regexp.MustCompile("(?s)```yaml\\ntypes:\\n(.*?)```")
	reTypeKey    = regexp.MustCompile(`(?m)^  ([A-Z][A-Za-z0-9]*):\s*$`)
)

// declaredBehaviours reads the behaviour names a blueprint's contracts declare.
func declaredBehaviours(blueprint string) []string {
	var out []string
	for _, m := range reContractsBlock.FindAllStringSubmatch(blueprint, -1) {
		for _, n := range reBehaviourName.FindAllStringSubmatch(m[1], -1) {
			out = append(out, n[1])
		}
	}
	return out
}

var (
	reContractsBlock = regexp.MustCompile("(?s)```yaml\ncontracts:\n(.*?)```")
	reBehaviourName  = regexp.MustCompile(`(?m)^\s*-\s*name:\s*([A-Za-z][\w-]*)\s*$`)
)

// validateNoMechanismInSpecification — a specification blueprint states a
// requirement, never the product that satisfies it.
//
// architecture/storage's Design Principle 6: "the blueprint names a contract,
// never a product … no blueprint may require a particular one. A blueprint that
// mandates an engine has decided something that is not its to decide."
//
// Checked on YAML VALUES only, not prose. A blueprint explaining the rule names
// SQLite as an example of a choice a consumer makes, and that is the rule being
// stated rather than broken. A `yaml` value is a declaration, and a declaration
// naming a product is the fault.
func validateNoMechanismInSpecification(corpus map[string]string) []Finding {
	var findings []Finding
	for _, name := range sortedKeys(corpus) {
		if !strings.HasPrefix(name, "protocol/") && !strings.HasPrefix(name, "architecture/") &&
			!strings.HasPrefix(name, "patterns/") && !strings.HasPrefix(name, "agents/") {
			continue // platforms and schemas may name what they translate to
		}
		if mechanismExempt[name] {
			continue
		}
		for _, m := range reMechanismValue.FindAllStringSubmatch(corpus[name], -1) {
			findings = append(findings, Finding{
				Blueprint: name, Rule: "contract, never a product",
				Detail: fmt.Sprintf("declares %q — a specification blueprint states what a store "+
					"must provide, and the engine is the platform's default for the tenant "+
					"unless the tenant says otherwise", strings.TrimSpace(m[0])),
				Authority: "architecture/storage.md (Design Principle 6)",
				Severity:  SeverityFault,
			})
		}
	}
	return findings
}

// reMechanismValue matches a storage product named as a declared VALUE.
var reMechanismValue = regexp.MustCompile(
	`(?mi)^\s*(?:engine|backend|driver|store|provider):\s*` +
		`(sqlite|postgres(?:ql)?|mysql|mariadb|redis|mongodb|dynamodb|leveldb|rocksdb|duckdb)\b` +
		`|^\s*-?\s*behavior:\s*\w*(?:sqlite|postgres|redis|mongo)\w*`)

// mechanismExempt are blueprints whose SUBJECT is the choice itself.
//
// Listed rather than inferred, and deliberately short. Each is a blueprint that
// exists to enumerate the options a consumer picks from, so naming them is what
// the document is for.
var mechanismExempt = map[string]bool{
	// Specifies the storage contract and states the rule, naming engines as
	// examples of a consumer's choice.
	"architecture/storage.md": true,
	// Its subject is which upload providers exist and how they are swapped.
	"patterns/file-upload.md": true,
	// Its subject is client-side persistence, where the mechanism is the
	// platform facility being specified.
	"patterns/offline.md": true,
}

// validateChecklistBackticks — an identifier in an assertion is backticked.
//
// Not a style rule. The structural checker reads JSON keys from BACKTICKED
// spans only, so
//
//	`RegisterResponse` includes `agent_id`, `token`   → keys found, check runs
//	RegisterResponse includes agent_id, token         → no keys, check SKIPPED
//
// and the second reports nothing — not a failure, an absence. An unbackticked
// identifier silently disables the check that would have verified it, which is
// a measurable part of why 95 of 132 assertions were graded by opinion.
//
// Reported at WARNING, not fault. A bare identifier makes an assertion weaker,
// not wrong, and 35 of the corpus's route-bearing assertions are bare — a fault
// per line would bury everything else.
func validateChecklistBackticks(corpus map[string]string) []Finding {
	var findings []Finding
	for _, name := range sortedKeys(corpus) {
		if strings.HasPrefix(name, "schemas/") {
			continue
		}
		section := sectionBody(corpus[name], "## Verification Checklist")
		if section == "" {
			continue
		}
		bare := 0
		var first string
		for _, line := range strings.Split(section, "\n") {
			l := strings.TrimSpace(line)
			if !strings.HasPrefix(l, "- [ ]") {
				continue
			}
			// Strip backticked spans, then look for what should have been in
			// them. What survives is unquoted.
			stripped := reTickedSpan.ReplaceAllString(l, " ")
			if m := reBareIdentifier.FindString(stripped); m != "" {
				bare++
				if first == "" {
					first = strings.TrimSpace(m)
				}
			}
		}
		if bare > 0 {
			findings = append(findings, Finding{
				Blueprint: name, Rule: "identifiers are backticked",
				Detail: fmt.Sprintf("%s with an unbackticked identifier (first: %q) — "+
					"the structural checker reads keys from backticked spans, so a bare "+
					"identifier disables the check that would have verified it",
					plural(bare, "assertion"), first),
				Authority: "schemas/common.md#writing-a-verification-checklist",
				Severity:  SeverityWarning,
			})
		}
	}
	return findings
}

var (
	reTickedSpan = regexp.MustCompile("`[^`]*`")
	// A route, a snake_case field, or a PascalCase type name left unquoted.
	reBareIdentifier = regexp.MustCompile(
		`\b(?:GET|POST|PUT|PATCH|DELETE) /v\d\S*` +
			`|\b[a-z]+_[a-z_]{2,}\b` +
			`|\b[A-Z][a-z]+(?:[A-Z][a-z]+){2,}\b`)
)
