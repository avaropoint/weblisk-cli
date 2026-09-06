package dispatch

// The names a blueprint declares.
//
// # Why this file exists
//
// A generator writes code, and code is made of names. Every name it produces
// either came from a blueprint or was invented, and only the first is
// repeatable. schemas/common's "Declared Names" states the rule; this reads the
// declarations so the rule can be enforced instead of hoped for.
//
// It was not read, and the cost was measurable. architecture/storage has always
// declared its store operations by name —
//
//	| Agent Registry | Orchestrator | PutAgent, GetAgent, DeleteAgent, ListAgents |
//
// — and nothing extracted them, so they reached the model only as prose inside a
// blueprint it was also reading for a dozen other reasons. One plan used Get,
// Put and List; the next used GetAgent, PutAgent and ListAgents. Neither file
// was wrong and every file was stale: ten compliant files were regenerated
// because a plan had been made again.
//
// The blueprint was explicit the whole time. The pipeline was not listening.

import (
	"regexp"
	"strings"
)

// EndpointOperation is one row of a component's `## Endpoints` table.
type EndpointOperation struct {
	Method    string
	Path      string
	Operation string // the declared name, PascalCase; "" when the table predates the column
}

// Wire is the endpoint as the requirements list it: "POST /v1/register".
func (e EndpointOperation) Wire() string { return e.Method + " " + e.Path }

// ExtractEndpointOperations reads the declared name of every endpoint a
// component's own blueprint says it serves.
//
// By column name, so a table gaining a column moves nothing. A blueprint
// written before the Operation column existed still parses, with Operation
// empty — reported as a gap rather than guessed at.
func ExtractEndpointOperations(blueprint string) []EndpointOperation {
	section := endpointsSection(blueprint)
	if section == "" {
		return nil
	}
	var out []EndpointOperation
	seen := map[string]bool{}
	for _, t := range TablesWithColumns(section, "method", "path") {
		for _, row := range t.Rows {
			e := EndpointOperation{
				Method:    strings.ToUpper(strings.TrimSpace(row["method"])),
				Path:      strings.Trim(strings.TrimSpace(row["path"]), "`"),
				Operation: strings.TrimSpace(row["operation"]),
			}
			if !isHTTPMethod(e.Method) || !strings.HasPrefix(e.Path, "/") {
				continue
			}
			if seen[e.Wire()] {
				continue
			}
			seen[e.Wire()] = true
			out = append(out, e)
		}
	}
	return out
}

// endpointsSection is the `## Endpoints` section of a blueprint, "" if absent.
func endpointsSection(blueprint string) string {
	i := headingIndex(blueprint, "## Endpoints")
	if i < 0 {
		return ""
	}
	rest := blueprint[i+len("## Endpoints"):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// StoreContract is one row of a store summary table.
type StoreContract struct {
	Store      string
	Owner      string
	Operations []string
}

// ExtractStoreContracts reads the store summary table from a storage blueprint.
//
// Selected by its columns rather than by position on the page: `## Interfaces`
// may carry more than one table, and "the first one" is not a specification.
func ExtractStoreContracts(blueprint string) []StoreContract {
	i := headingIndex(blueprint, "## Interfaces")
	if i < 0 {
		return nil
	}
	rest := blueprint[i+len("## Interfaces"):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	var out []StoreContract
	for _, t := range TablesWithColumns(rest, "store", "owner", "operations") {
		for _, row := range t.Rows {
			var ops []string
			for _, op := range strings.Split(row["operations"], ",") {
				op = strings.TrimSpace(op)
				// Identifiers only. A cell of prose is a description, and
				// treating its words as operation names would require a
				// component to implement "Why this index exists".
				if op != "" && reIdentifier.MatchString(op) {
					ops = append(ops, op)
				}
			}
			if len(ops) == 0 {
				continue
			}
			out = append(out, StoreContract{
				Store:      strings.TrimSpace(row["store"]),
				Owner:      strings.TrimSpace(row["owner"]),
				Operations: ops,
			})
		}
	}
	return out
}

// reIdentifier is a single PascalCase identifier and nothing else.
var reIdentifier = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)

// OperationsOwnedBy returns the store operations a component must implement.
//
// Matched on the Owner column, because architecture/storage's "A component
// implements only the stores it owns" is the rule that keeps a hub from
// growing an agent's persistence layer.
func OperationsOwnedBy(contracts []StoreContract, target string) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range contracts {
		if !ownerIs(c.Owner, target) {
			continue
		}
		for _, op := range c.Operations {
			if !seen[op] {
				seen[op] = true
				out = append(out, op)
			}
		}
	}
	return out
}

// ownerIs matches an Owner cell against a target name.
//
// The table writes owners as prose — "Orchestrator", "Lifecycle Agent",
// "Task Agent" — and the target is a slug. Compared on the leading word,
// lowercased, so "Orchestrator" owns the orchestrator's stores and "Lifecycle
// Agent" does not.
func ownerIs(owner, target string) bool {
	if target == "" {
		return false
	}
	first := owner
	if i := strings.IndexByte(owner, ' '); i > 0 {
		first = owner[:i]
	}
	return strings.EqualFold(strings.TrimSpace(first), target)
}

// Reading a markdown table by COLUMN NAME.
//
// # Why position is not good enough
//
// The Auth column was read as "the third cell". Adding the Operation column to
// the endpoints table — which schemas/architecture now requires — made the
// third cell the operation, and every protected endpoint in the corpus
// silently became unprotected. The prober then expected no token where the
// blueprint says one is required.
//
// A blueprint's table is a contract with named columns, and gaining a column is
// an ordinary edit an author will make. Anything reading one MUST find its
// cells by header, so an inserted column moves nothing.

// MarkdownTable is one parsed table: its headers, and its rows as maps.
type MarkdownTable struct {
	Headers []string
	Rows    []map[string]string
}

// ParseMarkdownTables reads every pipe table in a block of markdown.
//
// A table is a header row, a separator row of dashes, then body rows. Rows with
// a different cell count than the header are skipped rather than padded: a
// mismatch means the table is malformed, and inventing empty cells for it
// produces a confident answer about a document nobody can read.
func ParseMarkdownTables(md string) []MarkdownTable {
	var out []MarkdownTable
	lines := strings.Split(md, "\n")
	for i := 0; i < len(lines); i++ {
		if !isTableRow(lines[i]) || i+1 >= len(lines) || !isSeparatorRow(lines[i+1]) {
			continue
		}
		t := MarkdownTable{Headers: tableCells(lines[i])}
		i += 2
		for ; i < len(lines) && isTableRow(lines[i]); i++ {
			cells := tableCells(lines[i])
			if len(cells) != len(t.Headers) {
				continue
			}
			row := make(map[string]string, len(cells))
			for j, h := range t.Headers {
				row[strings.ToLower(h)] = cells[j]
			}
			t.Rows = append(t.Rows, row)
		}
		i--
		if len(t.Rows) > 0 {
			out = append(out, t)
		}
	}
	return out
}

func isTableRow(line string) bool {
	l := strings.TrimSpace(line)
	return strings.HasPrefix(l, "|") && strings.Count(l, "|") >= 2
}

func isSeparatorRow(line string) bool {
	l := strings.TrimSpace(line)
	if !isTableRow(l) {
		return false
	}
	for _, c := range tableCells(l) {
		c = strings.TrimSpace(c)
		if c == "" || strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return true
}

// tableCells splits a row on unescaped pipes, dropping the leading and trailing
// empties a pipe table produces.
func tableCells(line string) []string {
	l := strings.TrimSpace(line)
	l = strings.TrimPrefix(l, "|")
	l = strings.TrimSuffix(l, "|")
	parts := strings.Split(l, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// TablesWithColumns returns the tables carrying every named column, so a caller
// selects the table it means rather than the first one on the page.
func TablesWithColumns(md string, columns ...string) []MarkdownTable {
	var out []MarkdownTable
	for _, t := range ParseMarkdownTables(md) {
		have := map[string]bool{}
		for _, h := range t.Headers {
			have[strings.ToLower(h)] = true
		}
		ok := true
		for _, c := range columns {
			if !have[strings.ToLower(c)] {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, t)
		}
	}
	return out
}

// reAdoption reads the frontmatter field that says whether a blueprint's
// surface is served by every deployment.
var reAdoption = regexp.MustCompile(`(?m)^adoption:\s*([a-z-]+)`)

// AdoptionOf reports a blueprint's adoption: "required" (the default) or
// "opt-in".
//
// An opt-in surface exists only for a deployment that configures it —
// federation is the case that named the field: it exists only for
// communicating with other hubs, and a deployment that does not federate
// serves none of it and is complete without it.
//
// Read from frontmatter ONLY, so an `adoption:` line inside a YAML example in
// the body cannot change what a blueprint claims about itself.
func AdoptionOf(blueprint string) string {
	end := strings.Index(blueprint, "-->")
	if end < 0 {
		return "required"
	}
	if m := reAdoption.FindStringSubmatch(blueprint[:end]); m != nil {
		return m[1]
	}
	return "required"
}
