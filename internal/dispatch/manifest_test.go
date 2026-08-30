package dispatch

import (
	"os"
	"strings"
	"testing"
)

// TestParsesTheRealGoManifest reads the actual blueprint rather than a fixture,
// so drift between the spec and this parser fails here rather than in a
// generation run.
func TestParsesTheRealGoManifest(t *testing.T) {
	src, err := os.ReadFile("../../../weblisk-blueprints/platforms/go.md")
	if err != nil {
		t.Skipf("blueprints not checked out beside the CLI: %v", err)
	}
	man, err := ExtractManifest(string(src))
	if err != nil {
		t.Fatalf("the shipped Go manifest does not parse: %v", err)
	}
	if man == nil {
		t.Fatal("no manifest found in platforms/go.md")
	}
	target, err := man.Target("orchestrator")
	if err != nil {
		t.Fatal(err)
	}
	if target.Root != "server" {
		t.Errorf("root = %q, want server", target.Root)
	}
	if !strings.Contains(target.Build, "go build") {
		t.Errorf("build = %q", target.Build)
	}
	if len(target.Files) < 5 {
		t.Errorf("got %d files, expected the full orchestrator set", len(target.Files))
	}

	// Rule 4: the union of must_serve must cover every orchestrator endpoint.
	served := map[string]bool{}
	for _, f := range target.Files {
		for _, e := range f.MustServe {
			served[e] = true
		}
	}
	for _, want := range []string{
		"POST /v1/register", "DELETE /v1/register", "GET /v1/services",
		"POST /v1/channel", "POST /v1/rotate-key", "GET /v1/health", "GET /v1/audit",
	} {
		if !served[want] {
			t.Errorf("manifest does not declare %q on any file", want)
		}
	}

	// identity.go must declare the crypto surface, or a generated hub can be
	// structurally complete and unable to sign anything.
	var identity *ManifestFile
	for i := range target.Files {
		if target.Files[i].Path == "identity.go" {
			identity = &target.Files[i]
		}
	}
	if identity == nil {
		t.Fatal("no identity.go in the manifest")
	}
	if len(identity.MustDefine) == 0 {
		t.Error("identity.go declares no required symbols")
	}
}

func TestAbsentManifestIsNotAnError(t *testing.T) {
	// A platform blueprint with no manifest is complete, not broken.
	man, err := ExtractManifest("# Some Platform\n\n## Project Structure\n\nprose only\n")
	if err != nil {
		t.Fatalf("a blueprint without a manifest errored: %v", err)
	}
	if man != nil {
		t.Error("a manifest was invented from a blueprint that has none")
	}
}

const goodManifest = `generate:
  orchestrator:
    root: server
    build: go build ./...
    files:
      - path: main.go
        purpose: Entry point
        must_define: [main]
      - path: orchestrator.go
        purpose: HTTP server
        must_serve:
          - POST /v1/register
          - GET /v1/health
    conformance: [L1]
`

func TestBothListFormsParse(t *testing.T) {
	man, err := ParseManifest(goodManifest)
	if err != nil {
		t.Fatal(err)
	}
	tg := man.Targets["orchestrator"]
	if len(tg.Files) != 2 {
		t.Fatalf("got %d files, want 2", len(tg.Files))
	}
	if len(tg.Files[0].MustDefine) != 1 || tg.Files[0].MustDefine[0] != "main" {
		t.Errorf("inline list mis-parsed: %v", tg.Files[0].MustDefine)
	}
	if len(tg.Files[1].MustServe) != 2 {
		t.Errorf("block list mis-parsed: %v", tg.Files[1].MustServe)
	}
	if tg.Conformance[0] != "L1" {
		t.Errorf("conformance = %v", tg.Conformance)
	}
}

// TestUnrecognisedConstructsAreRefused is the property that makes a restricted
// parser safe: a manifest that half-parses would generate a hub silently missing
// files nobody asked it to omit.
func TestUnrecognisedConstructsAreRefused(t *testing.T) {
	cases := map[string]string{
		"unknown key":       "generate:\n  orchestrator:\n    root: server\n    surprise: yes\n",
		"tab indentation":   "generate:\n\torchestrator:\n\t\troot: server\n",
		"file without path": "generate:\n  orchestrator:\n    files:\n      - purpose: nope\n",
		"purpose orphaned":  "generate:\n  orchestrator:\n    purpose: outside a file\n",
		"no generate block": "orchestrator:\n  root: server\n",
	}
	for name, src := range cases {
		if _, err := ParseManifest(src); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestValidationRulesAreEnforced(t *testing.T) {
	cases := map[string]string{
		"no files":       "generate:\n  orchestrator:\n    root: server\n    build: go build\n    conformance: [L1]\n",
		"no build":       "generate:\n  orchestrator:\n    root: server\n    files:\n      - path: a.go\n        purpose: x\n    conformance: [L1]\n",
		"no conformance": "generate:\n  orchestrator:\n    root: server\n    build: go build\n    files:\n      - path: a.go\n        purpose: x\n",
		"no purpose":     "generate:\n  orchestrator:\n    root: server\n    build: go build\n    files:\n      - path: a.go\n    conformance: [L1]\n",
		"duplicate path": "generate:\n  orchestrator:\n    root: server\n    build: go build\n    files:\n      - path: a.go\n        purpose: x\n      - path: a.go\n        purpose: y\n    conformance: [L1]\n",
		"escaping path":  "generate:\n  orchestrator:\n    root: server\n    build: go build\n    files:\n      - path: ../../etc/passwd\n        purpose: x\n    conformance: [L1]\n",
	}
	for name, src := range cases {
		if _, err := ParseManifest(src); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestMissingTargetNamesWhatExists(t *testing.T) {
	man, err := ParseManifest(goodManifest)
	if err != nil {
		t.Fatal(err)
	}
	_, err = man.Target("gateway")
	if err == nil {
		t.Fatal("a missing target was accepted")
	}
	if !strings.Contains(err.Error(), "orchestrator") {
		t.Errorf("error does not say what IS available: %v", err)
	}
}
