package dispatch

import (
	"strings"
	"testing"
)

// Two agents must not share stored state. Keyed by kind alone — which is what
// ComponentInit did — the second agent's build reuses the first's plan
// (including its output directory) and, worse, reads the first's written
// manifest as its own. DecideRebuild's job is to remove files the current plan
// no longer lists, so building `agents/billing` after `agents/shipping` could
// delete shipping's files.
func TestEachInstanceKeysItsOwnState(t *testing.T) {
	billing, shipping := Agent("billing"), Agent("shipping")

	if billing.Key() == shipping.Key() {
		t.Fatalf("both agents key state as %q — one rebuild can delete the other's files", billing.Key())
	}
	// The manifest is the file that decides deletions; it is named from a hash
	// of the key, so distinct keys must produce distinct manifests.
	if manifestName("/t", billing.Key()) == manifestName("/t", shipping.Key()) {
		t.Error("two agents share one written manifest")
	}
	// And their directories are distinct — asked of the platform, which is the
	// only thing that knows.
	if LayoutOf(billing, "go").Home() == LayoutOf(shipping, "go").Home() {
		t.Errorf("both agents generate into %q", LayoutOf(billing, "go").Home())
	}
}

// A singleton's key must stay the bare kind. Manifests, plans and records on
// disk are keyed by a hash of this string, so changing it for the orchestrator
// would orphan every tenant already generated.
func TestSingletonKeysAreUnchanged(t *testing.T) {
	for _, c := range []Component{Orchestrator(), Gateway()} {
		if c.Key() != c.Kind {
			t.Errorf("%s keys state as %q, want the bare kind — "+
				"every manifest already on disk is found by that name", c.Kind, c.Key())
		}
	}
}

// A named kind with no name would write into `agents/` and own every agent's
// files. A singleton with a name would key state nothing else can find.
func TestAComponentThatCannotBeBuiltSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		c       Component
		wantBad bool
	}{
		{"agent without a name", Component{Kind: "agent"}, true},
		{"domain without a name", Component{Kind: "domain"}, true},
		{"orchestrator with a name", Component{Kind: "orchestrator", Name: "x"}, true},
		{"gateway with a name", Component{Kind: "gateway", Name: "x"}, true},
		{"no kind at all", Component{}, true},
		{"a named agent", Agent("billing"), false},
		{"the orchestrator", Orchestrator(), false},
		{"the gateway", Gateway(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := tc.c.Valid()
			if tc.wantBad && bad == "" {
				t.Errorf("%+v was accepted", tc.c)
			}
			if !tc.wantBad && bad != "" {
				t.Errorf("%+v was refused: %s", tc.c, bad)
			}
		})
	}
}

// Kind selects the blueprint; the key must never be used for that. Asking for
// a blueprint named `agent:billing` finds nothing.
func TestTheBlueprintIsChosenByKindNotByInstance(t *testing.T) {
	c := Agent("billing")
	if got := targetBlueprint(c.Kind); got == "" {
		t.Fatalf("no blueprint for kind %q", c.Kind)
	}
	if targetBlueprint(c.Key()) == targetBlueprint(c.Kind) && c.Key() != c.Kind {
		t.Error("targetBlueprint answers the same for a key and a kind, so passing the wrong one " +
			"would not be caught here — the distinction is only enforced by the call sites")
	}
	// Every agent is graded against the same assertions, whatever it is called.
	if GenerationRoots(Agent("a").Kind, "go")[2] != GenerationRoots(Agent("b").Kind, "go")[2] {
		t.Error("two agents resolve different blueprint roots")
	}
}

// The advice printed when a build stops must name a command that exists.
//
// It said "Re-run with --resume" for every component. Only `server init` and
// `tenant create` accept that flag; `agent create`, `domain create` and
// `gateway create` do not, and pointing three commands at a flag that does not
// exist is worse than saying nothing — the operator is told the banked files
// need a switch to reach, when re-running is what reaches them.
func TestResumeAdviceNamesACommandThatExists(t *testing.T) {
	for _, tc := range []struct {
		c    Component
		want string
	}{
		{Orchestrator(), "weblisk server init --resume"},
		{Agent("alerting"), "weblisk agent create alerting"},
		{Domain("seo"), "weblisk domain create seo"},
		{Gateway(), "weblisk gateway create"},
		{Component{Kind: "content"}, "weblisk component content init"},
	} {
		if got := ResumeCommand(tc.c); got != tc.want {
			t.Errorf("ResumeCommand(%s) = %q, want %q", tc.c.Key(), got, tc.want)
		}
	}
	// And only the command that has a guard to lift mentions the flag.
	for _, c := range []Component{Agent("a"), Domain("d"), Gateway()} {
		if strings.Contains(ResumeCommand(c), "--resume") {
			t.Errorf("%s is told to pass a flag its command does not accept: %q",
				c.Key(), ResumeCommand(c))
		}
	}
}
