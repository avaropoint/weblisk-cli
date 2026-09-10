package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// layout.go's whole argument is that WHERE a component goes is a property of
// the platform blueprint and not a decision the pipeline makes. That argument
// is only worth anything if the values match the blueprints, so these read the
// blueprints where they can find them and pin the values either way.

// The claim that broke the first attempt at ROADMAP item 3: a component's
// directory is not platform-independent. If it were, one Dir() would have done.
func TestALayoutIsNotPlatformIndependent(t *testing.T) {
	billing := Agent("billing")
	homes := map[string]string{}
	for _, p := range []string{"go", "node", "cloudflare", "rust"} {
		homes[p] = LayoutOf(billing, p).Home()
	}
	if homes["go"] == homes["cloudflare"] {
		t.Errorf("go and cloudflare both put an agent at %q, but go.md maps agents/<name> to "+
			"cmd/<name> + internal/agents/<name> while cloudflare.md puts a Worker at agents/<name>",
			homes["go"])
	}
	seen := map[string]bool{}
	for p, h := range homes {
		if h == "" {
			t.Errorf("%s has no home for an agent", p)
		}
		seen[h] = true
	}
	if len(seen) < 3 {
		t.Errorf("four platforms produced %d distinct agent homes: %v — the premise of layout.go "+
			"is that they differ", len(seen), homes)
	}
}

// Go, specifically, because it is the default and because the reverted switch
// got it wrong: platforms/go.md line 134 states
//
//	| agents/<name> | yes | cmd/<name> + internal/agents/<name> |
//
// and NOT agents/<name>, which is what Component.Dir() used to answer.
func TestGoFollowsItsMappingTable(t *testing.T) {
	l := LayoutOf(Agent("billing"), "go")
	for _, want := range []string{"cmd/billing", "internal/agents/billing"} {
		found := false
		for _, d := range l.Dirs {
			if d == want {
				found = true
			}
		}
		if !found {
			t.Errorf("go layout for agent billing is %v, missing %q — see platforms/go.md's mapping table", l.Dirs, want)
		}
	}
	// internal/agent is architecture/agent, the framework every agent imports.
	// Claiming it would have one agent own the shared framework package.
	for _, d := range l.Dirs {
		if d == "internal/agent" {
			t.Error("an agent claims internal/agent, which is the framework package every agent imports, " +
				"not this agent's logic — go.md draws exactly this distinction")
		}
	}
	if l.Contained {
		t.Error("go says a component carries its own build manifest; go.md argues the opposite " +
			"under \"Why one module, and not a copy per binary\"")
	}
	if l.Entry != "cmd/billing/main.go" {
		t.Errorf("entry = %q, want cmd/billing/main.go", l.Entry)
	}
}

// cloudflare and rust are the platforms where a component DOES carry its own
// build manifest, which is what makes Contained a real distinction rather than
// a constant.
func TestContainedIsTrueOnlyWhereTheBlueprintSaysSo(t *testing.T) {
	for _, tc := range []struct {
		platform string
		want     bool
	}{
		{"go", false},
		{"node", false},
		{"cloudflare", true},
		{"rust", true},
	} {
		if got := LayoutOf(Agent("billing"), tc.platform).Contained; got != tc.want {
			t.Errorf("%s Contained = %v, want %v", tc.platform, got, tc.want)
		}
	}
}

// An unrecognised platform must land where PlatformBlueprint sends it, or a
// component is generated against one blueprint and laid out per another.
func TestAnUnknownPlatformLaysOutLikeGo(t *testing.T) {
	want := LayoutOf(Agent("billing"), "go")
	for _, p := range []string{"", "haskell", "not-a-platform"} {
		got := LayoutOf(Agent("billing"), p)
		if strings.Join(got.Dirs, ",") != strings.Join(want.Dirs, ",") {
			t.Errorf("platform %q lays out at %v, want go's %v — PlatformBlueprint resolves it to go.md",
				p, got.Dirs, want.Dirs)
		}
	}
	// And confirm PlatformBlueprint really does default to go, rather than
	// this test asserting a default that moved.
	if bp := PlatformBlueprint("not-a-platform"); !strings.Contains(bp, "go") {
		t.Errorf("PlatformBlueprint(%q) = %q — this test's premise is that it defaults to go", "not-a-platform", bp)
	}
}

// Owns is what will decide whether a planned path is this component's. Foreign
// is what will reject a sibling's. The gap between them is shared code, which
// the first component in a fresh tenant legitimately plans.
func TestOwnsAndForeignSeparateSiblingsFromSharedCode(t *testing.T) {
	l := LayoutOf(Agent("billing"), "go")
	for _, tc := range []struct {
		path            string
		owns, isForeign bool
	}{
		{"cmd/billing/main.go", true, false},
		{"internal/agents/billing/logic.go", true, false},
		// A sibling agent, which is the failure the revert observed.
		{"cmd/shipping/main.go", false, true},
		{"internal/agents/shipping/logic.go", false, true},
		// Shared libraries: not ours, not a sibling's.
		{"internal/protocol/types.go", false, false},
		{"internal/identity/keys.go", false, false},
		{"go.mod", false, false},
		// The framework package every agent imports.
		{"internal/agent/agent.go", false, false},
	} {
		if got := l.Owns(tc.path); got != tc.owns {
			t.Errorf("Owns(%q) = %v, want %v", tc.path, got, tc.owns)
		}
		if got := l.Foreign(tc.path); got != tc.isForeign {
			t.Errorf("Foreign(%q) = %v, want %v", tc.path, got, tc.isForeign)
		}
	}
}

// under() is written out rather than done with strings.HasPrefix for a reason.
func TestAPrefixIsNotAParent(t *testing.T) {
	l := LayoutOf(Agent("agents"), "go")
	// internal/agents/agents is ours; internal/agentsmith is nobody's.
	if l.Owns("internal/agentsmith/x.go") {
		t.Error("internal/agentsmith was read as being under internal/agents")
	}
	if !l.Owns("internal/agents/agents/x.go") {
		t.Error("an agent literally named `agents` does not own its own directory")
	}
}

// A gateway's directory is the CLI's convention, not a blueprint's statement,
// and layout.go says so. A rule that rejected a plan for departing from a
// convention would be the pipeline becoming the specification.
func TestAnUnspecifiedKindIsMarkedAsConvention(t *testing.T) {
	for _, p := range []string{"go", "node", "cloudflare", "rust"} {
		if LayoutOf(Gateway(), p).Specified() {
			t.Errorf("%s claims to specify where a gateway goes", p)
		}
		if !LayoutOf(Agent("billing"), p).Specified() {
			t.Errorf("%s does not specify where an agent goes, but its blueprint does", p)
		}
		if !LayoutOf(Orchestrator(), p).Specified() {
			t.Errorf("%s does not specify where the orchestrator goes", p)
		}
	}
	// And the claim behind it: no platform blueprint gives a gateway a row.
	if bp := findBlueprintDir(); bp != "" {
		for _, p := range []string{"go", "node", "cloudflare", "rust"} {
			b, err := os.ReadFile(filepath.Join(bp, "platforms", p+".md"))
			if err != nil {
				continue
			}
			if strings.Contains(string(b), "gateway/<") {
				t.Errorf("platforms/%s.md does state a gateway directory, so Specified() should say so", p)
			}
		}
	}
}

// The singleton pair is what isSelfDir has always derived and what every
// manifest on disk was written against, so it must not move.
func TestSingletonLayoutIsUnchangedFromWhatIsOnDisk(t *testing.T) {
	l := LayoutOf(Orchestrator(), "go")
	want := map[string]bool{"cmd/orchestrator": true, "internal/orchestrator": true}
	for _, d := range l.Dirs {
		delete(want, d)
	}
	if len(want) > 0 {
		t.Errorf("orchestrator layout is %v; it must still contain cmd/orchestrator and "+
			"internal/orchestrator, which is what isSelfDir derives and what existing manifests hold", l.Dirs)
	}
}

// findBlueprintDir locates the installed corpus, or "" when there is none —
// these tests must not fail on a machine that has not fetched it.
func findBlueprintDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	entries, err := os.ReadDir(filepath.Join(home, ".weblisk", "blueprints"))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(home, ".weblisk", "blueprints", e.Name())
		}
	}
	return ""
}
