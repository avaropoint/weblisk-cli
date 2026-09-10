package dispatch

// layout.go — WHERE a component's files go.
//
// # The question ROADMAP item 3 was stuck on
//
// `agent create` was switched to the plan pipeline and reverted the same day.
// The revert named five places that assume plan.Root is "." and concluded that
// underneath them sat a design disagreement: the single-shot path put an agent
// at agents/<name> as a SELF-CONTAINED MODULE, and the plan pipeline refuses to
// plan a go.mod when the tenant already declares one. Item 3 offered two
// options — a component is a package set inside the tenant module, or a
// component is its own module — and said the decision had to come first.
//
// It is not a decision. The platform blueprints already answer it, and they do
// not all give the same answer:
//
//	platforms/go.md          one module rooted at the tenant. Its mapping table
//	                         reads `agents/<name>` -> cmd/<name> +
//	                         internal/agents/<name>, and its rationale section
//	                         ("Why one module, and not a copy per binary") is an
//	                         argument against exactly the layout the deleted
//	                         path used
//	platforms/node.md        one project. src/agents/<component>/ per agent,
//	                         importing ../../protocol
//	platforms/cloudflare.md  a Worker per agent: agents/<name>/ with its OWN
//	                         wrangler.toml and package.json
//	platforms/rust.md        a Cargo workspace: agents/<name>/ with its own
//	                         Cargo.toml, depending on the weblisk-core crate
//
// So "is a component its own module" has no platform-independent answer, which
// is why picking one broke. Go and node share the tenant's build; cloudflare and
// rust do not. WHERE the files go is a property of the PLATFORM BLUEPRINT, and
// the pipeline's job is to read it rather than to decide it.
//
// # What that buys, and why plan.Root stays "."
//
// Once the layout is a fact the pipeline is TOLD, nothing needs a second
// coordinate space. plan.Root remains "." for every component on every
// platform, and the component's directories appear as a PREFIX inside the
// plan's own file paths:
//
//	go/agent billing   cmd/billing/main.go, internal/agents/billing/*.go
//	cf/agent billing   agents/billing/wrangler.toml, agents/billing/src/*.js
//
// Every one of the five assumptions the revert listed then holds, unchanged:
// RunBuild runs at the tenant root and the model authors plan.Build knowing its
// paths start at that root; plan.Module is the tenant module and the import
// prefix is correct because the packages really are in it; manifests and
// ReadTenantState share one coordinate space; Protected() mixes nothing; and
// isSelfDir is given these directories rather than deriving internal/<kind>
// from a key that may read "agent:billing".
//
// The cloudflare case is what proves the shape rather than the exception to it:
// a Worker's package.json is planned INSIDE agents/<name>, and the plan rule
// that refuses a go.mod refuses it only at the root, where the tenant's own
// module is declared.

import (
	"fmt"
	"path"
	"strings"
)

// Layout is where one component's files live, and where its process starts.
type Layout struct {
	// Kind and Name identify the component, as Component does.
	Kind string
	Name string
	// Platform is the platform blueprint this layout was read from.
	Platform string
	// Dirs are the directories this component's own files live in, relative to
	// the tenant root and slash-separated. First is where a reader would look
	// first.
	//
	// This is the component's OWN code. It is not everything the component's
	// generation may write: the first component in a fresh tenant also plans
	// the shared libraries — internal/protocol, internal/identity, src/protocol
	// — and those belong to no one component's directory. Ownership of those is
	// settled by the manifests, which is what TenantState.Owned reads.
	Dirs []string
	// Families are the directories under which components of a kind live on
	// this platform — cmd/ on go, src/agents/ on node — one level above a
	// component's own directory.
	//
	// What they are for: a plan that names a path inside a family but not
	// inside THIS component's directory is writing into a sibling's home, and
	// that is the failure the revert of the first switch observed. A real
	// `weblisk agent create billing` planned cmd/agent/main.go and eighteen
	// more files at tenant-root paths, and nothing rejected it, because the only
	// check of the kind compared against cmd/<KIND> — which is the name a
	// sibling would have, not this one.
	//
	// Only families are treated this way. A path outside every family is a
	// SHARED library — internal/protocol, src/protocol, weblisk-core — and the
	// first component generated into a fresh tenant legitimately plans those.
	// Who owns them afterwards is the manifests' answer, not this type's.
	Families []string
	// Others are the directories belonging to components a tenant has at most
	// ONE of — the orchestrator, the gateway, the admin surface — minus this
	// component's own.
	//
	// Separate from Families because a singleton has no family. A family covers
	// a kind that comes in multiples: any path under cmd/ or internal/agents/
	// belongs to whichever instance is named there, so the family alone settles
	// it. architecture/orchestrator's library is internal/orchestrator EXACTLY,
	// and nothing above it is a family — internal/ also holds internal/protocol
	// and internal/identity, which are shared and which the first component
	// into a tenant legitimately plans.
	//
	// Measured, not predicted: with families alone, `Foreign` answered false for
	// internal/orchestrator/registry.go asked of an agent. The harm is not that
	// the agent writes the file — it is that the agent's manifest then RECORDS
	// it, so a later `weblisk server init` is refused its own directory by the
	// ownership rule.
	Others []string
	// Entry is the file the component's process starts at.
	Entry string
	// Contained reports whether the component carries its own build manifest —
	// a Worker's package.json, a crate's Cargo.toml — rather than sharing the
	// tenant's.
	//
	// Read by the rule that refuses to plan a build manifest the tenant already
	// declares: on go and node that refusal is right, and on cloudflare and
	// rust the manifest inside Dirs[0] is required.
	Contained bool
}

// LayoutOf reads a component's layout from the platform it is generated for.
//
// The platform arms mirror PlatformBlueprint exactly, including its default: a
// platform string nothing recognises resolves to platforms/go.md, so it must
// resolve to go's layout too. Two functions answering "which platform is this"
// differently is how a component gets generated against one blueprint and laid
// out per another.
func LayoutOf(c Component, platform string) Layout {
	l := Layout{Kind: c.Kind, Name: c.Name, Platform: platform, Dirs: dirsFor(c, platform)}
	// Every OTHER singleton's home. See Layout.Others for why a family does not
	// cover them.
	for _, k := range singletonKinds {
		if k == c.Kind {
			continue
		}
		l.Others = append(l.Others, dirsFor(Component{Kind: k}, platform)...)
	}
	// The instance's own word — an agent's name, or the kind for a singleton.
	self := c.Name
	if self == "" {
		self = c.Kind
	}
	switch platform {
	case "cloudflare":
		// platforms/cloudflare.md: each component is a Worker with its own
		// wrangler.toml and package.json.
		l.Contained = true
		l.Families = []string{"agents", "domains"}
		l.Entry = path.Join(l.Dirs[0], "src", "index.js")
	case "node":
		// platforms/node.md: one project, one src/. Shared code is imported from
		// src/protocol, so a component owns only its own subtree.
		l.Families = []string{path.Join("src", "agents"), path.Join("src", "domains")}
		if c.Kind == "orchestrator" {
			// The entry point is src/server.ts, which sits OUTSIDE
			// src/orchestrator/. Owns answers for Entry directly rather than
			// widening Dirs to "src" — see Owns.
			l.Entry = path.Join("src", "server.ts")
		} else {
			l.Entry = path.Join(l.Dirs[0], "index.ts")
		}
	case "rust":
		// platforms/rust.md: a Cargo workspace, each component a member with its
		// own Cargo.toml depending on the weblisk-core crate.
		l.Contained = true
		l.Families = []string{"agents", "domains"}
		l.Entry = path.Join(l.Dirs[0], "src", "main.rs")
	default:
		// platforms/go.md, and every unrecognised platform, because
		// PlatformBlueprint sends both here.
		l.Families = []string{"cmd", path.Join("internal", "agents"), path.Join("internal", "domains")}
		l.Entry = path.Join("cmd", self, "main.go")
	}
	return l
}

// singletonKinds are the component kinds a tenant has at most one of, and whose
// library directory therefore belongs to that one component.
//
// Listed rather than derived because "which kinds come in multiples" is the
// blueprint's answer and Component.Named already carries it — this is its
// complement, and the two must not disagree.
//
// NOT included, and each for a measured reason:
//
//	internal/agent, internal/domain   the FRAMEWORKS every agent and every
//	                                  domain controller imports —
//	                                  architecture/agent, not an instance of
//	                                  one. The first component into a tenant
//	                                  plans them.
//	internal/admin                    platforms/go.md gives admin a row, but
//	                                  the ORCHESTRATOR serves its surface:
//	                                  fourteen of the orchestrator's
//	                                  twenty-one required endpoints are
//	                                  /v1/admin/*, and architecture/admin.md
//	                                  is in its graph. Treating internal/admin
//	                                  as another component's home would reject
//	                                  a correct `weblisk server init` plan.
//	                                  cmd/admin is still protected — cmd is a
//	                                  family.
var singletonKinds = []string{"orchestrator", "gateway", "content"}

// dirsFor is where one component's own files live on one platform.
//
// Factored out of LayoutOf because a layout must be able to say where the
// OTHER components are, and answering that by calling LayoutOf would recur.
func dirsFor(c Component, platform string) []string {
	self := c.Name
	if self == "" {
		self = c.Kind
	}
	switch platform {
	case "cloudflare", "rust":
		// agents/<name>/, domains/<name>/, server/ for the orchestrator.
		switch c.Kind {
		case "agent":
			return []string{path.Join("agents", c.Name)}
		case "domain":
			return []string{path.Join("domains", c.Name)}
		case "orchestrator":
			return []string{"server"}
		}
		// gateway among them. Neither blueprint states a directory for one, so
		// this is the name the CLI has always written to rather than a reading
		// of the blueprint — see Specified.
		return []string{self}
	case "node":
		switch c.Kind {
		case "agent":
			return []string{path.Join("src", "agents", c.Name)}
		case "domain":
			return []string{path.Join("src", "domains", c.Name)}
		}
		return []string{path.Join("src", self)}
	}
	// go, and every unrecognised platform.
	//
	// The mapping table is explicit: a running thing is cmd/<name> plus
	// internal/<the blueprint's name>. An agent's blueprint is agents/<name>, so
	// its library is internal/agents/<name> — NOT internal/agent, which is
	// architecture/agent, the framework every agent imports.
	switch c.Kind {
	case "agent":
		return []string{path.Join("cmd", c.Name), path.Join("internal", "agents", c.Name)}
	case "domain":
		return []string{path.Join("cmd", c.Name), path.Join("internal", "domains", c.Name)}
	}
	// orchestrator, gateway, admin, content and anything else a singleton:
	// cmd/<kind> + internal/<kind>, which is the pair isSelfDir always derived
	// and every manifest already on disk was written against.
	return []string{path.Join("cmd", self), path.Join("internal", self)}
}

// Specified reports whether the platform blueprint states where this kind of
// component goes.
//
// False for a gateway, on every platform: architecture/gateway.md describes what
// a gateway serves and no platform blueprint gives it a row. The directory used
// for one is therefore the CLI's convention, and the rules that reject a plan
// for writing outside its layout do not fire on a convention — a rule enforcing
// a path nothing specified is the pipeline becoming the specification, which is
// the fault schemas/common calls out and this repo has now hit twice.
func (l Layout) Specified() bool {
	switch l.Kind {
	case "orchestrator", "agent", "domain":
		return true
	}
	return false
}

// Owns reports whether a tenant-root-relative path is this component's.
//
// The entry point counts wherever it sits. On node it is src/server.ts, one
// level above src/orchestrator/, and the alternative — widening Dirs to "src" —
// hands the orchestrator ownership of every agent's directory.
func (l Layout) Owns(rel string) bool {
	clean := path.Clean(rel)
	if l.Entry != "" && clean == l.Entry {
		return true
	}
	for _, d := range l.Dirs {
		if clean == d || under(clean, d) {
			return true
		}
	}
	return false
}

// Foreign reports whether a path belongs to a component that is not this one.
//
// Two separate questions, because they answer to different evidence.
//
// A NAMED component's home — internal/orchestrator, server/, src/gateway — is
// out of bounds for everyone else no matter what this component's own placement
// is specified as. Writing into one is wrong under any reading of any
// blueprint, so nothing gates it.
//
// A FAMILY — cmd/, internal/agents/ — is gated on Specified. This component's
// own directory is inside its family, so enforcing the family is also enforcing
// where THIS component goes, and for a gateway no platform blueprint says
// where that is. Rejecting a plan for placing a gateway outside a directory
// this CLI chose would be the pipeline becoming the specification. Locate
// reads the written manifest instead, so a gateway placed elsewhere is still
// found by `gateway start` and `weblisk validate`.
//
// Shared code is neither: `internal/protocol/types.go` is in no family and is
// nobody's home, and the first component into a fresh tenant plans it.
func (l Layout) Foreign(rel string) bool {
	if l.Owns(rel) {
		return false
	}
	clean := path.Clean(rel)
	for _, d := range l.Others {
		if clean == d || under(clean, d) {
			return true
		}
	}
	if !l.Specified() {
		return false
	}
	for _, fam := range l.Families {
		if under(clean, fam) {
			return true
		}
	}
	return false
}

// under reports whether clean is inside dir, by path segment.
//
// Written out rather than done with strings.HasPrefix, which answers yes for
// `internal/agentsmith` under `internal/agents`.
func under(clean, dir string) bool {
	return len(clean) > len(dir) && clean[:len(dir)] == dir && clean[len(dir)] == '/'
}

// Home is the directory a reader would be pointed at, and the one an operator is
// told about when a component is generated.
func (l Layout) Home() string {
	if len(l.Dirs) == 0 {
		return "."
	}
	return l.Dirs[0]
}

// Directories renders Dirs as prose, for a prompt.
func (l Layout) Directories() string {
	switch len(l.Dirs) {
	case 0:
		return "the tenant root"
	case 1:
		return l.Dirs[0] + "/"
	}
	out := ""
	for i, d := range l.Dirs {
		switch {
		case i == 0:
			out = d + "/"
		case i == len(l.Dirs)-1:
			out += " and " + d + "/"
		default:
			out += ", " + d + "/"
		}
	}
	return out
}

// FormatLayout states where this component's files go, for the plan prompt.
//
// Stated on EVERY generation, including the first into an empty directory.
// These sentences used to live inside FormatTenantState, which returns nothing
// when there is no tenant yet — so the one generation with no other component to
// collide with was also the only one told nothing about its own directories.
//
// The paths are the platform blueprint's, not a constant: `agent create billing`
// was told its entry point was cmd/agent/main.go, planned exactly that, and the
// ownership check then rejected the file the instruction had just demanded.
func (l Layout) FormatLayout() string {
	if len(l.Dirs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("--- WHERE YOUR FILES GO ---\n\n")
	fmt.Fprintf(&b, "Every path in your plan is relative to the tenant root.\n")
	fmt.Fprintf(&b, "Your component's own directories are %s.\n", l.Directories())
	if l.Entry != "" {
		fmt.Fprintf(&b, "Its entry point is %s, and your build command must build it.\n", l.Entry)
	}
	if len(l.Families) > 0 {
		fmt.Fprintf(&b, "Other components live beside you under %s. None of their\n",
			strings.Join(l.Families, "/, ")+"/")
		b.WriteString("directories are yours to write — replacing one replaces a running component.\n")
	}
	if l.Contained {
		fmt.Fprintf(&b, "This platform gives each component its own build manifest: plan yours\n"+
			"inside %s/, not at the tenant root.\n", l.Dirs[0])
	} else {
		b.WriteString("Shared code — the protocol types and helpers every component imports —\n" +
			"lives outside those directories, and you may plan it if it is not there yet.\n")
	}
	b.WriteString("\n")
	return b.String()
}
