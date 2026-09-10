package dispatch

// Reading a generated component back off the disk.
//
// `agent start` and `agent list` looked for agents/<name>/go.mod, which is the
// shape the deleted single-shot generator wrote. On go the pipeline writes
// cmd/<name>/main.go and no module of its own, so without these the switch
// would have produced agents that build and cannot be started or listed.

import (
	"strings"
	"testing"
)

// A Go agent lives where platforms/go.md puts it, not where the old generator
// put it, and Locate finds it there.
func TestAGoAgentIsFoundAtItsEntryPoint(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module acme\n\ngo 1.27\n")
	write(t, root, "cmd/billing/main.go", "package main\n\nfunc main() {}\n")

	l, found := Locate(root, Agent("billing"))
	if !found {
		t.Fatal("a generated Go agent could not be found — `agent start` would report it missing")
	}
	if l.Platform != "go" {
		t.Errorf("platform = %q, want go", l.Platform)
	}
	if l.Home() != "cmd/billing" {
		t.Errorf("home = %q, want cmd/billing", l.Home())
	}
}

// The platform is read back off the disk, and the markers do not overlap.
func TestEachPlatformIsRecognisedByItsOwnMarker(t *testing.T) {
	for _, tc := range []struct {
		platform string
		file     string
	}{
		{"go", "cmd/billing/main.go"},
		{"cloudflare", "agents/billing/wrangler.toml"},
		{"rust", "agents/billing/Cargo.toml"},
		{"node", "src/agents/billing/index.ts"},
	} {
		root := t.TempDir()
		write(t, root, tc.file, "x\n")
		l, found := Locate(root, Agent("billing"))
		if !found {
			t.Errorf("%s: %s did not identify a component", tc.platform, tc.file)
			continue
		}
		if l.Platform != tc.platform {
			t.Errorf("%s: read back as %q", tc.file, l.Platform)
		}
	}
}

// Nothing on disk is not "a go agent with a missing entry point".
func TestAnUngeneratedComponentIsNotLocated(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module acme\n\ngo 1.27\n")
	write(t, root, "cmd/billing/main.go", "package main\n\nfunc main() {}\n")
	if _, found := Locate(root, Agent("shipping")); found {
		t.Error("an agent that was never generated was located")
	}
	if err := StartComponent(root, Agent("shipping"), nil); err == nil ||
		!strings.Contains(err.Error(), "agent create shipping") {
		t.Errorf("starting an ungenerated agent does not say how to generate one: %v", err)
	}
}

// What a tenant has comes from the manifests, which name their owner exactly.
// A directory scan would have to guess which of cmd/'s entries is an agent.
func TestGeneratedComponentsComeFromTheManifests(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, Orchestrator().Key(), "cmd/orchestrator/main.go")
	writeManifest(t, root, Agent("billing").Key(), "cmd/billing/main.go")
	writeManifest(t, root, Agent("shipping").Key(), "cmd/shipping/main.go")
	writeManifest(t, root, Domain("seo").Key(), "cmd/seo/main.go")

	agents := GeneratedOfKind(root, "agent")
	if len(agents) != 2 || agents[0].Name != "billing" || agents[1].Name != "shipping" {
		t.Fatalf("agents = %+v, want billing and shipping in order", agents)
	}
	for _, c := range GeneratedComponents(root) {
		if c.Kind == "agent" && c.Name == "" {
			t.Error("an agent lost its name on the way through the manifest")
		}
	}
	if got := GeneratedOfKind(root, "domain"); len(got) != 1 || got[0].Name != "seo" {
		t.Errorf("domains = %+v, want seo", got)
	}
}

// Key and ParseKey are inverses, including for the singleton case.
func TestParseKeyIsKeysInverse(t *testing.T) {
	for _, c := range []Component{
		Orchestrator(), Gateway(), Agent("billing"), Domain("seo"), {Kind: "content"},
	} {
		if got := ParseKey(c.Key()); got != c {
			t.Errorf("ParseKey(%q) = %+v, want %+v", c.Key(), got, c)
		}
	}
}

// The node orchestrator's entry point sits outside its own directory, so a
// marker looked for INSIDE that directory misses one that was generated
// correctly.
func TestTheNodeOrchestratorIsFoundAtItsEntryPoint(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/server.ts", "export {}\n")
	write(t, root, "src/orchestrator/registry.ts", "export {}\n")

	l, found := Locate(root, Orchestrator())
	if !found {
		t.Fatal("a generated node orchestrator could not be found")
	}
	if l.Platform != "node" {
		t.Errorf("platform = %q, want node", l.Platform)
	}
}

// The layout names one extension because a prompt must name one. Finding the
// component afterwards should not depend on which was emitted.
func TestANodeComponentEmittedAsJavaScriptIsStillFound(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/agents/billing/index.js", "module.exports = {}\n")
	if _, found := Locate(root, Agent("billing")); !found {
		t.Error("a node agent emitted as .js was not found")
	}
}

// A component that is not where its platform says is still found.
//
// No platform blueprint gives a gateway a directory, so the layout used for one
// is this CLI's convention and ValidatePlan does not enforce it — see
// Layout.Specified. A gateway the model placed somewhere else would generate
// cleanly and then be invisible to `gateway start`, `agent list` and
// `weblisk validate`. The manifest records what actually happened.
func TestAComponentIsFoundWhereItsFilesActuallyAre(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module acme\n\ngo 1.27\n")
	write(t, root, "cmd/gw/main.go", "package main\n\nfunc main() {}\n")
	writeManifestOn(t, root, Gateway().Key(), "go", "cmd/gw/main.go")

	// The layout says cmd/gateway; nothing is there.
	if platformMarker(root, LayoutOf(Gateway(), "go"), "go") != "" {
		t.Fatal("the layout found something at cmd/gateway; this test is not measuring what it says")
	}
	l, found := Locate(root, Gateway())
	if !found {
		t.Fatal("a generated gateway was invisible because it is not where the convention says")
	}
	if l.Home() != "cmd/gw" {
		t.Errorf("home = %q, want cmd/gw — the record is what actually happened", l.Home())
	}
	if l.Entry != "cmd/gw/main.go" {
		t.Errorf("entry = %q, want cmd/gw/main.go", l.Entry)
	}
	// The platform's own decisions are kept; only the placement is corrected.
	if l.Contained {
		t.Error("a go component was reported as carrying its own build manifest")
	}
	if len(l.Families) == 0 {
		t.Error("the platform's families were lost")
	}
}

// The recorded platform is believed before any marker is probed.
func TestTheRecordedPlatformIsPreferredToProbing(t *testing.T) {
	root := t.TempDir()
	write(t, root, "cmd/billing/main.go", "package main\n\nfunc main() {}\n")
	writeManifestOn(t, root, Agent("billing").Key(), "go", "cmd/billing/main.go")
	l, found := Locate(root, Agent("billing"))
	if !found || l.Platform != "go" {
		t.Fatalf("found=%v platform=%q, want go", found, l.Platform)
	}
	// A manifest with no platform — written before it was recorded — still
	// resolves by probing, as it always did.
	root2 := t.TempDir()
	write(t, root2, "cmd/billing/main.go", "package main\n\nfunc main() {}\n")
	writeManifest(t, root2, Agent("billing").Key(), "cmd/billing/main.go")
	if _, found := Locate(root2, Agent("billing")); !found {
		t.Error("a manifest written before the platform was recorded stopped resolving")
	}
}

// A Go component keeps its library directory alongside the cmd/ directory its
// entry point named.
func TestARelocatedGoComponentKeepsItsLibraryDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, "cmd/gw/main.go", "package main\n\nfunc main() {}\n")
	write(t, root, "internal/gateway/routes.go", "package gateway\n")
	writeManifestOn(t, root, Gateway().Key(), "go", "cmd/gw/main.go", "internal/gateway/routes.go")
	l, _ := Locate(root, Gateway())
	if !containsStr(l.Dirs, "internal/gateway") {
		t.Errorf("dirs = %v, want internal/gateway kept", l.Dirs)
	}
}
