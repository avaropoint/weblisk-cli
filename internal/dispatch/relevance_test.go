package dispatch

import (
	"strings"
	"testing"
)

func corpus() map[string]string {
	return map[string]string{
		"protocol/spec.md":             "SPEC BODY",
		"protocol/identity.md":         "IDENTITY BODY",
		"architecture/orchestrator.md": "ORCHESTRATOR BODY",
	}
}

func TestAModuleFileDoesNotGetTheCryptoSpecification(t *testing.T) {
	// The cost this exists to remove: go.mod received the full ML-DSA-65
	// specification on a call that writes three lines.
	// The plan gives go.mod no declares and no serves — it has no interface to
	// conform to, so no protocol blueprint can help it.
	f := PlannedFile{Path: "go.mod", Purpose: "Go module definition declaring dependencies"}
	sel := relevantBlueprints(f, corpus())
	if _, sent := sel["protocol/identity.md"]; sent {
		t.Error("go.mod was sent the identity specification")
	}
	if len(sel) >= len(corpus()) {
		t.Errorf("no filtering happened: %d of %d sent", len(sel), len(corpus()))
	}
}

func TestAnIdentityFileGetsTheIdentitySpecification(t *testing.T) {
	f := PlannedFile{Path: "identity.go",
		Purpose:  "ML-DSA-65 key management, signing, WLT tokens",
		Declares: []string{"Sign", "Verify"}}
	sel := relevantBlueprints(f, corpus())
	if _, sent := sel["protocol/identity.md"]; !sent {
		t.Error("the identity file was not sent the identity specification")
	}
}

func TestServingAnEndpointAlwaysGetsTheProtocol(t *testing.T) {
	// Whatever the prose says, a file that serves endpoints needs the wire
	// contract.
	f := PlannedFile{Path: "x.go", Purpose: "opaque description with no keywords",
		Serves: []string{"POST /v1/register"}}
	sel := relevantBlueprints(f, corpus())
	if _, sent := sel["protocol/spec.md"]; !sent {
		t.Error("a file serving an endpoint was not sent the protocol")
	}
}

func TestAnUnmatchedFileGetsEverything(t *testing.T) {
	// Sending too much costs time; sending too little costs correctness. The
	// default must be inclusion.
	// Declares something, so it has an interface — an empty keyword match means
	// the table is incomplete, not that the file is trivial.
	f := PlannedFile{Path: "mystery.go", Purpose: "zzz", Declares: []string{"Thing"}}
	sel := relevantBlueprints(f, corpus())
	if len(sel) != len(corpus()) {
		t.Errorf("an unmatched file got %d of %d blueprints; it should get all", len(sel), len(corpus()))
	}
}

func TestAnUnknownBlueprintIsAlwaysSent(t *testing.T) {
	// A blueprint this table does not know about must not be silently dropped.
	c := corpus()
	c["patterns/something-new.md"] = "NEW BODY"
	f := PlannedFile{Path: "go.mod", Purpose: "Go module definition", Declares: []string{"x"}}
	sel := relevantBlueprints(f, c)
	if _, sent := sel["patterns/something-new.md"]; !sent {
		t.Error("an unrecognised blueprint was dropped rather than sent")
	}
}

func TestNothingIsRewritten(t *testing.T) {
	// Filtering, not summarising: a blueprint is sent whole or not at all. A
	// paraphrase between the specification and the implementation would defeat
	// the premise that the blueprint is the source of truth.
	f := PlannedFile{Path: "identity.go", Purpose: "signing and keys"}
	sel := relevantBlueprints(f, corpus())
	if body := sel["protocol/identity.md"]; body != "IDENTITY BODY" {
		t.Errorf("blueprint content was altered: %q", body)
	}
	rendered := joinBlueprints(sel, []string{"protocol/identity.md"})
	if !strings.Contains(rendered, "IDENTITY BODY") || !strings.Contains(rendered, "protocol/identity.md") {
		t.Errorf("rendering lost content or its source: %q", rendered)
	}
}
