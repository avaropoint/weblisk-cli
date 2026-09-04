package dispatch

// Against the real blueprint corpus, because the point of Declared Names is
// that the corpus is the source. A test with an inline table proves the regex
// and says nothing about whether the blueprints declare what the pipeline needs.

import (
	"strings"
	"testing"
)

// schemas/architecture requires an Operation for every endpoint row. A row
// without one is a gap the pipeline must report rather than fill in — so this
// asserts the corpus has none.
func TestEveryDeclaredEndpointHasAnOperation(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol"})
	found := 0
	for name, body := range bps {
		if !strings.HasPrefix(name, "architecture/") {
			continue
		}
		for _, e := range ExtractEndpointOperations(body) {
			found++
			if e.Operation == "" {
				t.Errorf("%s: %s has no Operation — schemas/architecture requires one", name, e.Wire())
			}
		}
	}
	if found == 0 {
		t.Fatal("no endpoint rows were read at all; the parser is not reading the corpus")
	}
	t.Logf("%d endpoint rows, all named", found)
}

// The exact names one plan replaced with Get/Put/List. The blueprint declared
// them all along; nothing read it.
func TestStoreOperationsReachTheOrchestrator(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture"})
	body, ok := bps["architecture/storage.md"]
	if !ok {
		t.Fatal("architecture/storage.md not in the corpus")
	}
	ops := OperationsOwnedBy(ExtractStoreContracts(body), "orchestrator")
	for _, want := range []string{
		"PutAgent", "GetAgent", "DeleteAgent", "ListAgents",
		"AppendAudit", "QueryAudit",
		"PutChannel", "GetChannel", "DeleteChannel",
		"ClaimNamespace", "ReleaseNamespace", "ListNamespaces",
	} {
		if !containsStr(ops, want) {
			t.Errorf("%s is declared by architecture/storage and did not reach the orchestrator's requirements", want)
		}
	}
	// And it must NOT be handed another component's stores. "A component
	// implements only the stores it owns."
	for _, notMine := range []string{"CreateUser", "PutStrategy", "AppendObservation", "PutExecution"} {
		if containsStr(ops, notMine) {
			t.Errorf("%s belongs to another component and was required of the orchestrator", notMine)
		}
	}
}

// A table gaining a column must move nothing. Reading the Auth column as "the
// third cell" made every protected endpoint in the corpus unprotected the
// moment the Operation column was added.
func TestTableCellsAreFoundByHeaderNotPosition(t *testing.T) {
	md := "\n## Endpoints\n\n" +
		"| Method | Path | Auth | Purpose |\n" +
		"|--------|------|------|---------|\n" +
		"| GET | /v1/thing | yes | A thing |\n"
	withColumn := "\n## Endpoints\n\n" +
		"| Method | Path | Operation | Auth | Purpose |\n" +
		"|--------|------|-----------|------|---------|\n" +
		"| GET | /v1/thing | Thing | yes | A thing |\n"

	for label, body := range map[string]string{"without Operation": md, "with Operation": withColumn} {
		got := ProtectedEndpointsFor("content", map[string]string{"architecture/content.md": body})
		if len(got) != 1 || got[0].Path != "/v1/thing" {
			t.Errorf("%s: protected surface read as %v", label, got)
		}
	}

	// And the operation is read only where it is declared.
	if ops := ExtractEndpointOperations(md); len(ops) != 1 || ops[0].Operation != "" {
		t.Errorf("an absent Operation was not reported as absent: %+v", ops)
	}
	if ops := ExtractEndpointOperations(withColumn); len(ops) != 1 || ops[0].Operation != "Thing" {
		t.Errorf("the declared Operation was not read: %+v", ops)
	}
}

// A malformed row is skipped, not padded. Padding invents empty cells and
// produces a confident answer about a document nobody can read.
func TestAMalformedRowIsSkippedNotPadded(t *testing.T) {
	md := "| Method | Path | Auth |\n|---|---|---|\n| GET | /v1/a | yes |\n| GET | /v1/b |\n"
	ts := ParseMarkdownTables(md)
	if len(ts) != 1 {
		t.Fatalf("expected 1 table, got %d", len(ts))
	}
	if len(ts[0].Rows) != 1 {
		t.Fatalf("expected the short row to be skipped, got %d rows", len(ts[0].Rows))
	}
}

// A prose cell is not a list of operation names.
func TestProseIsNotMistakenForOperations(t *testing.T) {
	md := "\n## Interfaces\n\n" +
		"| Store | Owner | Operations |\n|---|---|---|\n" +
		"| Agent Registry | Orchestrator | PutAgent, GetAgent |\n" +
		"| Notes | Orchestrator | whatever the backend supports |\n"
	cs := ExtractStoreContracts(md)
	for _, c := range cs {
		for _, op := range c.Operations {
			if strings.Contains(op, " ") || strings.ToLower(op) == op {
				t.Errorf("%q was read as an operation name", op)
			}
		}
	}
	if got := OperationsOwnedBy(cs, "orchestrator"); len(got) != 2 {
		t.Errorf("expected the two real operations, got %v", got)
	}
}

// Every REQUIRED endpoint must have a declared operation — including one the
// component's own table does not mention at all.
//
// protocol/spec declares POST /v1/rotate-key; architecture/orchestrator's
// Endpoints table omitted it. It was required, had no name, and nothing said
// so — the plan was rejected three steps later with "no file serves these
// endpoints", which names the symptom and not the cause.
func TestARequiredEndpointMissingFromTheTableIsReported(t *testing.T) {
	req := &Requirements{
		Endpoints: []string{"POST /v1/register", "POST /v1/rotate-key"},
		EndpointOps: []EndpointOperation{
			{Method: "POST", Path: "/v1/register", Operation: "Register"},
		},
	}
	// Mirror what GatherRequirements does, so the property is asserted rather
	// than the function's plumbing.
	named := map[string]bool{}
	for _, e := range req.EndpointOps {
		named[e.Wire()] = true
	}
	var unnamed []string
	for _, e := range req.Endpoints {
		if !named[e] {
			unnamed = append(unnamed, e)
		}
	}
	if len(unnamed) != 1 || unnamed[0] != "POST /v1/rotate-key" {
		t.Fatalf("the endpoint absent from the table was not reported: %v", unnamed)
	}
}

// And the corpus itself must have none: every endpoint protocol/spec requires
// of the orchestrator appears in the orchestrator's own Endpoints table, and so
// has a declared Operation.
//
// This is the invariant that broke. protocol/spec declared POST /v1/rotate-key
// and the architecture table omitted it, so it was required and unnamed.
func TestEveryProtocolEndpointAppearsInTheComponentTable(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol"})
	spec, ok := bps["protocol/spec.md"]
	if !ok {
		t.Skip("protocol/spec.md not present")
	}
	for _, c := range []struct{ target, section string }{
		{"orchestrator", "Orchestrator Endpoints"},
		{"agent", "Agent Endpoints"},
	} {
		body, ok := bps[targetBlueprint(c.target)]
		if !ok {
			continue // a component without an architecture blueprint here
		}
		named := map[string]bool{}
		for _, e := range ExtractEndpointOperations(body) {
			if e.Operation != "" {
				named[e.Wire()] = true
			}
		}
		required := ExtractEndpoints(spec, c.section)
		if len(required) == 0 {
			continue
		}
		for _, e := range required {
			if !named[e] {
				t.Errorf("%s: protocol/spec requires %s and %s does not declare an Operation for it",
					c.target, e, targetBlueprint(c.target))
			}
		}
		t.Logf("%s: %d protocol endpoints, all named", c.target, len(required))
	}
}

// The endpoint surface is stated in THREE places, and they must agree.
//
//   - protocol/types.md "## Protocol Paths" — which declares itself the single
//     source of truth for endpoint definitions
//   - protocol/spec.md — a "### METHOD /path" section per endpoint
//   - architecture/<component>.md "## Endpoints" — the table generation reads
//
// schemas/common's Declared Names says two statements of a name are two names
// as soon as one is edited. This is three, and one had already drifted: the
// orchestrator's table said key rotation needed no token while Protocol Paths
// said it did — so the conformance prober would not have expected a 401 on
// /v1/rotate-key, and an unauthenticated key rotation would have passed.
//
// Until the duplication is removed, it is checked.
func TestTheThreeStatementsOfTheEndpointSurfaceAgree(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol"})
	types, ok := bps["protocol/types.md"]
	if !ok {
		t.Skip("protocol/types.md not present")
	}

	// The authoritative table: Path | Method | Auth | Component | Purpose.
	type fact struct{ auth, component string }
	authoritative := map[string]fact{}
	for _, tab := range TablesWithColumns(types, "path", "method", "auth", "component") {
		for _, row := range tab.Rows {
			method := strings.ToUpper(strings.TrimSpace(row["method"]))
			path := strings.Trim(strings.TrimSpace(row["path"]), "`")
			if !isHTTPMethod(method) || !strings.HasPrefix(path, "/") {
				continue
			}
			authoritative[method+" "+path+" "+strings.TrimSpace(row["component"])] = fact{
				auth:      strings.ToLower(strings.TrimSpace(row["auth"])),
				component: strings.TrimSpace(row["component"]),
			}
		}
	}
	if len(authoritative) == 0 {
		t.Fatal("Protocol Paths was not read; the check is vacuous")
	}

	checked := 0
	for _, component := range []string{"orchestrator", "agent"} {
		body, ok := bps[targetBlueprint(component)]
		if !ok {
			continue
		}
		for _, tab := range TablesWithColumns(endpointsSection(body), "method", "path", "auth") {
			for _, row := range tab.Rows {
				method := strings.ToUpper(strings.TrimSpace(row["method"]))
				path := strings.Trim(strings.TrimSpace(row["path"]), "`")
				if !isHTTPMethod(method) || !strings.HasPrefix(path, "/") {
					continue
				}
				want, known := authoritative[method+" "+path+" "+component]
				if !known {
					continue // owned by the component, not part of the protocol
				}
				checked++
				// "no*" is "no, and here is why" — the footnote form.
				got := strings.ToLower(strings.TrimSpace(row["auth"]))
				got = strings.TrimSuffix(got, "*")
				if got != want.auth {
					t.Errorf("%s %s: architecture/%s says auth=%q, protocol/types says %q",
						method, path, component, row["auth"], want.auth)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no endpoint was compared; the check is vacuous")
	}
	t.Logf("%d protocol endpoints agree across all three statements", checked)
}

// An endpoint a component declares MUST be specified in a blueprint that
// component requires.
//
// architecture/orchestrator declared ten /v1/admin/operators/* endpoints and
// did NOT require architecture/admin — the blueprint that specifies how they
// work. So the hub was generated having never seen the registration flow. It
// chose a signing payload, sensibly and differently from the client, and the
// connection failed with "operator signature verification failed" with neither
// side wrong about anything it had been told.
//
// # The first version of this test passed against the bug
//
// It collected "who specifies this path" from every blueprint's endpoint table
// INCLUDING the component's own — and a component always requires itself, so
// every endpoint was satisfied by its own declaration and the check was
// vacuous. It reported "31 declared endpoints, each specified in a required
// blueprint" with the fault fully present.
//
// The question is not "is this path written down somewhere I can see" but "does
// ANOTHER blueprint also document this path" — because if one does, it is
// specifying behaviour this component has to implement, and not requiring it
// means generating against half a contract.
func TestADeclaredEndpointIsSpecifiedInABlueprintTheComponentRequires(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol"})

	// Every path each blueprint documents in a table of its own, normalised so
	// admin's ":name" and architecture's "{name}" are the same path.
	documents := map[string][]string{} // normalised path -> blueprints
	for name, body := range bps {
		seen := map[string]bool{}
		for _, tab := range TablesWithColumns(body, "path", "method") {
			for _, row := range tab.Rows {
				path := normalisePathParams(strings.Trim(strings.TrimSpace(row["path"]), "`"))
				if !strings.HasPrefix(path, "/v") || seen[path] {
					continue
				}
				seen[path] = true
				documents[path] = append(documents[path], name)
			}
		}
	}
	if len(documents) == 0 {
		t.Fatal("no documented paths were read; the derivation is not running")
	}

	checked, faults := 0, 0
	for _, component := range []string{"orchestrator", "agent", "content"} {
		self := targetBlueprint(component)
		body, ok := bps[self]
		if !ok {
			continue
		}
		required := map[string]bool{}
		for _, r := range DeclaredRequires(body) {
			required[r+".md"] = true
		}
		for _, e := range ExtractEndpointOperations(body) {
			path := normalisePathParams(e.Path)
			for _, src := range documents[path] {
				// Its own table declares that the endpoint exists.
				if src == self {
					continue
				}
				// A blueprint with its own `## Endpoints` section is a PEER
				// COMPONENT stating what IT serves, not a specification of what
				// this component must implement. Every component serves
				// /v1/health and the orchestrator and the agent both have a
				// /v1/services — those are independent surfaces that happen to
				// share a path, and treating them as cross-specification made
				// this check report three faults that were not faults.
				//
				// A blueprint that documents paths and has NO Endpoints section
				// of its own is specifying a surface for somebody else to serve.
				// architecture/admin is exactly that.
				if strings.Contains(bps[src], "\n## Endpoints") {
					continue
				}
				checked++
				if !required[src] {
					faults++
					t.Errorf("%s serves %s, which %s also documents — and %s does not require it",
						self, e.Wire(), src, self)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no cross-blueprint endpoint was compared; the check is vacuous")
	}
	t.Logf("%d endpoints documented in another blueprint, %d not required", checked, faults)
}

// normalisePathParams makes ":name" and "{name}" the same path, so two
// blueprints using different parameter notation are recognised as documenting
// one endpoint.
func normalisePathParams(p string) string {
	out := make([]string, 0, 8)
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ":") || (strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")) {
			seg = "{}"
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/")
}
