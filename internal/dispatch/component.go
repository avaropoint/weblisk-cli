package dispatch

// component.go — what is being generated, and WHICH ONE of it.
//
// # The fault this prevents
//
// ComponentInit took a `target` string — "orchestrator", "agent", "domain",
// "gateway" — and keyed everything off it: the plan cache, the tenant-state
// read, the prior-records lookup, and the written manifest that decides which
// files a rebuild is allowed to delete.
//
// That is correct for the singletons and wrong for the rest. A tenant has ONE
// orchestrator and ONE gateway, and any number of agents and domains. Keyed by
// kind alone, two agents share all four:
//
//   - the plan cache hits on the second agent and reuses the FIRST one's plan,
//     including its output directory
//   - the written manifest names one owner, so building `agents/billing` after
//     `agents/shipping` sees shipping's files as its own — and DecideRebuild's
//     job is to delete files the current plan no longer lists
//
// The second is destructive, which is why the name is threaded rather than
// left for later.
//
// # Kind versus key
//
// Both are needed and they are not interchangeable:
//
//	Kind  chooses the blueprint to read and the assertions to grade against.
//	      Every agent is graded against architecture/agent.
//	Key   identifies this INSTANCE's state — its plan, its manifest, its
//	      records. Every agent has its own.
//
// Passing Key where Kind belongs asks for a blueprint named `agent:billing`.
// Passing Kind where Key belongs is the bug above.
//
// # Status
//
// Orchestrator and Gateway are the only kinds ComponentInit is reached with
// today. `agent create`, `domain create` and `gateway create` were switched to
// it and REVERTED — see the note above AgentCreate for what that switch broke.
// Agent and Domain therefore exist here ahead of their use, and the tests below
// pin the two properties the eventual switch depends on: that instances key
// their own state, and that a singleton's key is unchanged so no manifest
// already on disk is orphaned.
//
// The keying itself is live and is not speculative: ComponentInit stores state
// under Key() now, and Plan.Owner carries it into the written manifest.

import "strings"

// Component is one generated thing: a kind, and for the kinds that come in
// multiples, a name.
type Component struct {
	// Kind is orchestrator, agent, domain or gateway. It selects the blueprint
	// graph and the conformance assertions.
	Kind string
	// Name is the instance, empty for a singleton. `agents/billing` has Kind
	// "agent" and Name "billing".
	Name string
}

// Orchestrator, Gateway, Agent and Domain construct the four kinds, so a caller
// never spells one wrong and never gives a singleton a name.
func Orchestrator() Component      { return Component{Kind: "orchestrator"} }
func Gateway() Component           { return Component{Kind: "gateway"} }
func Agent(name string) Component  { return Component{Kind: "agent", Name: name} }
func Domain(name string) Component { return Component{Kind: "domain", Name: name} }

// Key identifies this instance's stored state.
//
// For a singleton it is the bare kind, so every manifest, plan and record
// written before names existed is still found — those files are keyed by a
// hash of this string, and changing it for the orchestrator would orphan every
// tenant already on disk.
func (c Component) Key() string {
	if c.Name == "" {
		return c.Kind
	}
	return c.Kind + ":" + c.Name
}

// Where this component's files go is NOT answered here. It was — a Dir()
// method returning `agents/<name>` — and that answer was wrong twice over.
//
// It was wrong for Go: platforms/go.md's mapping table states
// `agents/<name>` -> `cmd/<name>` + `internal/agents/<name>`, and its "Why one
// module, and not a copy per binary" section argues against the very layout
// Dir() described. And it was wrong in KIND, because it answered without
// asking which platform: cloudflare really does put a Worker at
// `agents/<name>` with its own wrangler.toml, and Go really does not.
//
// A component identifies itself. Where a platform puts it is layout.go's, and
// it is read from the platform blueprint rather than decided here.

// Label names this component the way an operator would say it.
func (c Component) Label() string {
	if c.Name == "" {
		return c.Kind
	}
	return c.Kind + " " + c.Name
}

// Named reports whether this kind comes in multiples.
func Named(kind string) bool {
	return kind == "agent" || kind == "domain"
}

// Valid reports what is wrong with this component, or "".
//
// A named kind with no name would write to `agents/` and own every agent's
// files; a singleton with a name would key state nothing else can find.
func (c Component) Valid() string {
	switch {
	case strings.TrimSpace(c.Kind) == "":
		return "no kind"
	case Named(c.Kind) && strings.TrimSpace(c.Name) == "":
		return c.Kind + " needs a name — there can be more than one"
	case !Named(c.Kind) && c.Name != "":
		return "a tenant has only one " + c.Kind + ", so it takes no name"
	}
	return ""
}
