package dispatch

// The blueprint declares what it consumes; the tooling does not decide.

import (
	"os"
	"strings"
	"testing"
)

const orchestratorDeps = `<!--
requires: [protocol/identity, protocol/types]
-->
# Orchestrator

## Dependencies

` + "```yaml" + `
requires:
  - blueprint: protocol/identity
    version: ">=1.0.0 <2.0.0"
    bindings:
      types:
        - name: SigningKeyPair
          fields_used: [public_key, private_key, sign, verify]
        - name: WLToken
          fields_used: [sub, iss, iat, exp, cap]
    on_change:
      compatible: validate-and-adopt
  - blueprint: protocol/types
    version: ">=1.0.0 <2.0.0"
    bindings:
      types:
        - name: RegisterRequest
          fields_used: [manifest, signature, timestamp]
        - name: ServiceDirectory
          fields_used: [agents, routing_table, namespaces]
    on_change:
      compatible: validate-and-adopt
` + "```" + `

## Verification Checklist
- [ ] something
`

func TestBindingsCarryFieldsAndProvenance(t *testing.T) {
	b := ExtractBindings(orchestratorDeps)
	if len(b) != 4 {
		t.Fatalf("extracted %d bindings, want 4: %+v", len(b), b)
	}
	byType := map[string]Binding{}
	for _, x := range b {
		byType[x.Type] = x
	}
	sd, ok := byType["ServiceDirectory"]
	if !ok {
		t.Fatal("ServiceDirectory not bound")
	}
	if sd.From != "protocol/types" {
		t.Errorf("ServiceDirectory came from %q, want protocol/types", sd.From)
	}
	// The fields matter as much as the name: "you consume ServiceDirectory"
	// leaves the shape to be guessed from a 55 KB catalogue.
	if strings.Join(sd.FieldsUsed, ",") != "agents,routing_table,namespaces" {
		t.Errorf("fields = %v", sd.FieldsUsed)
	}
	if tok := byType["WLToken"]; tok.From != "protocol/identity" || len(tok.FieldsUsed) != 5 {
		t.Errorf("WLToken binding wrong: %+v", tok)
	}
}

func TestOnlyTypeBindingsAreRead(t *testing.T) {
	// endpoints, events and config bindings describe consumption this generator
	// does not act on. Inventing meaning for them would be the same overreach in
	// a new place.
	src := orchestratorDeps + `
` + "```yaml" + `
requires:
  - blueprint: protocol/spec
    bindings:
      endpoints:
        - path: /v1/register
          methods: [POST]
      events:
        - topic: system.agent.registered
` + "```" + `
`
	for _, b := range ExtractBindings(src) {
		if b.Type == "/v1/register" || b.Type == "system.agent.registered" {
			t.Errorf("a non-type binding was read as a type: %+v", b)
		}
	}
}

func TestUnboundTypesAreReportedNotAdded(t *testing.T) {
	// A type the protocol defines that no binding claims is a gap in the
	// blueprint's contract. Supplying it would hide the gap and put the tooling
	// back in charge of what a component needs.
	bound := BoundTypes(ExtractBindings(orchestratorDeps))
	defined := []string{"RegisterRequest", "ServiceDirectory", "WorkflowPhase", "OperationIntent", "ErrorResponse"}
	unbound := UnboundTypes(defined, bound)
	if len(unbound) != 3 {
		t.Fatalf("unbound = %v, want 3", unbound)
	}
	for _, want := range []string{"WorkflowPhase", "OperationIntent", "ErrorResponse"} {
		if !containsString(unbound, want) {
			t.Errorf("%s should be reported unbound", want)
		}
	}
	// And it must not have leaked into what is required.
	for _, b := range bound {
		if b == "WorkflowPhase" {
			t.Error("an unbound type entered the requirement set")
		}
	}
}

func TestTheRealOrchestratorRequiresTenTypesNotFiftyFour(t *testing.T) {
	// The fault this file exists for. Scraping every `### Type` heading from
	// protocol/types.md gave 54, and the plan prompt said "every one must be
	// declared by exactly one file" with ValidatePlan rejecting any plan that
	// omitted one. The orchestrator's own contract declares ten.
	//
	// Fifty-four demanded, ten declared. That is why one plan had 9 files, the
	// next 16 and the next 20, with store_execution.go and store_gateway.go
	// inside an orchestrator.
	root := "/Users/lwilson/Projects/Avaropoint/weblisk-blueprints"
	if _, err := os.Stat(root); err != nil {
		t.Skip("blueprints not present")
	}
	orch, err := os.ReadFile(root + "/architecture/orchestrator.md")
	if err != nil {
		t.Fatal(err)
	}
	bound := BoundTypes(ExtractBindings(string(orch)))
	if len(bound) == 0 {
		t.Fatal("no bindings read from the real orchestrator blueprint")
	}
	if len(bound) > 20 {
		t.Errorf("%d bound types — the binding block is not being read, and the "+
			"whole type catalogue is being demanded again: %v", len(bound), bound)
	}
	for _, want := range []string{"RegisterRequest", "ServiceDirectory", "AgentManifest", "AuditEntry", "WLToken"} {
		if !containsString(bound, want) {
			t.Errorf("%s is declared in the blueprint and was not read", want)
		}
	}
	// And the types belonging to other components must NOT be required.
	types, err := os.ReadFile(root + "/protocol/types.md")
	if err != nil {
		t.Fatal(err)
	}
	defined := ExtractTypes(string(types))
	if len(defined) < 40 {
		t.Fatalf("only %d types defined in protocol/types.md — fixture drift", len(defined))
	}
	for _, other := range []string{"WorkflowPhase", "OperationIntent", "EnforcementDecision", "Finding", "DeadLetterEntry"} {
		if containsString(bound, other) {
			t.Errorf("%s belongs to another component and is required of the orchestrator", other)
		}
	}
	unbound := UnboundTypes(defined, bound)
	if len(unbound) < 30 {
		t.Errorf("only %d unbound — the reduction is not happening", len(unbound))
	}
}
