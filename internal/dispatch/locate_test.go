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
