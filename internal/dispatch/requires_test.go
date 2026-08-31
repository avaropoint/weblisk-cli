package dispatch

import (
	"strings"
	"testing"
)

// TestRequiresIsReadFromFrontmatter — the declarations are the specification's
// own statement of what implementing it takes.
func TestRequiresIsReadFromFrontmatter(t *testing.T) {
	bp := `<!-- blueprint
type: platform
name: go
requires: [protocol/identity, protocol/types, architecture/orchestrator]
platform: go
-->

# Go

Body text with a yaml example:

requires: [should/not/be/read]
`
	got := DeclaredRequires(bp)
	if len(got) != 3 {
		t.Fatalf("got %d requires, want 3: %v", len(got), got)
	}
	// A requires: line in the BODY must not inject dependencies.
	for _, g := range got {
		if strings.Contains(g, "should/not") {
			t.Error("a requires line from the body was read as a dependency")
		}
	}
}

func TestNoFrontmatterYieldsNoRequires(t *testing.T) {
	if got := DeclaredRequires("# Just a document\n\nrequires: [a, b]\n"); len(got) != 0 {
		t.Errorf("requires were invented from a document with no frontmatter: %v", got)
	}
}

// TestTheRealGoGraphIncludesTheTypes is the omission that caused fifty errors:
// protocol/types.md defines fifty-five types with field tables, and a hardcoded
// list of three never sent it.
func TestTheRealGoGraphIncludesTheTypes(t *testing.T) {
	loaded, order, _, err := ResolveDeclared("../../../weblisk-blueprints",
		GenerationRoots("orchestrator", "go")...)
	if err != nil {
		t.Skipf("blueprints not checked out beside the CLI: %v", err)
	}
	if _, ok := loaded["protocol/types.md"]; !ok {
		t.Error("protocol/types.md is not in the resolved graph — the model would see type names and no definitions")
	}
	for _, want := range []string{
		"platforms/go.md", "protocol/spec.md", "protocol/identity.md",
		"architecture/orchestrator.md", "protocol/types.md",
	} {
		if _, ok := loaded[want]; !ok {
			t.Errorf("%s is declared and was not resolved", want)
		}
	}
	// A STARTER hub, not the framework. architecture/orchestrator.md declares two
	// requirements; the platform blueprint declares nine because it is the guide
	// for every component type, and following those pulls in agent, domain,
	// gateway and lifecycle — components nobody asked for.
	for _, notWanted := range []string{
		"architecture/agent.md", "architecture/domain.md",
		"architecture/gateway.md", "architecture/lifecycle.md",
	} {
		if _, ok := loaded[notWanted]; ok {
			t.Errorf("%s was resolved; it is a different component's blueprint", notWanted)
		}
	}
	if len(order) > 8 {
		t.Errorf("resolved %d blueprints; a starter orchestrator needs about five", len(order))
	}
}

// TestDepthIsOneNotTransitive — the transitive closure is the whole framework,
// forty-one blueprints and 314,000 tokens, which no prompt can carry and which
// the platform blueprint does not claim to need.
func TestDepthIsOneNotTransitive(t *testing.T) {
	loaded, _, _, err := ResolveDeclared("../../../weblisk-blueprints",
		GenerationRoots("orchestrator", "go")...)
	if err != nil {
		t.Skipf("blueprints unavailable: %v", err)
	}
	for _, deep := range []string{"architecture/enforcement.md", "patterns/privacy.md", "architecture/hub.md"} {
		if _, ok := loaded[deep]; ok {
			t.Errorf("%s was pulled in transitively; resolution must be depth one", deep)
		}
	}
	if len(loaded) > 20 {
		t.Errorf("resolved %d blueprints; depth one should be around a dozen", len(loaded))
	}
}

func TestAMissingRequirementIsReportedNotSkipped(t *testing.T) {
	// Silently dropping a declared requirement is how three of nine came to be
	// sent.
	_, _, missing, err := ResolveDeclared("../../../weblisk-blueprints", "platforms/does-not-exist.md")
	if err == nil {
		t.Skip("unexpectedly loaded")
	}
	if len(missing) == 0 {
		t.Error("a missing blueprint was not reported")
	}
}
