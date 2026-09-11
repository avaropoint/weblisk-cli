package dispatch

// Finding a fault while it is still cheap to fix.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file that does not parse is rejected on its own attempt, not at the build.
//
// notSourceIn asks whether the response looks like Go — a package clause is
// enough. A file can pass that and be unparseable, and nothing noticed until
// the build, which runs once after every file.
func TestAnUnparseableFileIsRejectedImmediately(t *testing.T) {
	f := PlannedFile{Path: "internal/agents/x/store.go", Purpose: "store"}
	broken := "package x\n\nfunc Open( {\n\treturn nil\n}\n"
	// It passes the shallow check, which is the point.
	if why := notSourceIn(f.Path, broken); why != "" {
		t.Fatalf("the shallow check already caught it (%s); this test proves nothing", why)
	}
	v := contractViolation(broken, f, nil)
	if v == "" {
		t.Fatal("an unparseable file was accepted, and would reach the build an hour later")
	}
	if !strings.Contains(v, "does not parse") {
		t.Errorf("the complaint does not name the problem: %q", v)
	}
	// Good source still passes.
	if v := contractViolation("package x\n\nfunc Open() error { return nil }\n", f, nil); v != "" {
		t.Errorf("valid Go was rejected: %s", v)
	}
}

// Only languages this binary can parse. A TypeScript syntax error is real and
// not Go's parser's to guess at.
func TestOnlyGoIsParsed(t *testing.T) {
	for _, p := range []string{"src/agents/x/index.ts", "agents/x/wrangler.toml", "x.json"} {
		if why := syntaxFault(p, "this is not Go {{{"); why != "" {
			t.Errorf("%s was parsed as Go: %s", p, why)
		}
	}
}

// A package is checked when its last planned file arrives, and not before.
func TestAPackageIsCheckedOnlyWhenComplete(t *testing.T) {
	plan := &Plan{Files: []PlannedFile{
		{Path: "internal/protocol/types.go"},
		{Path: "internal/protocol/errors.go"},
		{Path: "cmd/x/main.go"},
		{Path: "agents/x/wrangler.toml"}, // not Go; never gates anything
	}}
	g := newPackageGate(plan)
	if pkg := g.done("internal/protocol/types.go"); pkg != "" {
		t.Errorf("a half-generated package was offered for checking: %s", pkg)
	}
	if pkg := g.done("internal/protocol/errors.go"); pkg != "internal/protocol" {
		t.Errorf("a completed package was not offered: got %q", pkg)
	}
	if pkg := g.done("cmd/x/main.go"); pkg != "cmd/x" {
		t.Errorf("a single-file package was not offered: got %q", pkg)
	}
	if pkg := g.done("agents/x/wrangler.toml"); pkg != "" {
		t.Errorf("a non-Go file gated a package: %s", pkg)
	}
	// A file generated twice must not re-offer a package it already completed.
	if pkg := g.done("cmd/x/main.go"); pkg != "" {
		t.Errorf("a package was offered twice: %s", pkg)
	}
}

// The type check compiles real source and reports a real compiler diagnostic,
// named by the path the file was GENERATED as rather than the temp file the
// overlay put it in.
func TestTypeCheckingReportsTheCompilersComplaint(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module acme\n\ngo 1.27\n")
	// The tenant on disk is left EMPTY for this package: the whole point is
	// that nothing is written until generation finishes.
	if err := os.MkdirAll(filepath.Join(root, "internal", "protocol"), 0o755); err != nil {
		t.Fatal(err)
	}

	good := []GeneratedFile{{Path: "internal/protocol/types.go",
		Content: "package protocol\n\ntype ErrorResponse struct{ Error string }\n"}}
	if why := typeCheckPackage(root, "internal/protocol", good); why != "" {
		t.Errorf("sound source was reported as broken: %s", why)
	}

	bad := []GeneratedFile{{Path: "internal/protocol/types.go",
		Content: "package protocol\n\nfunc New() *Missing { return nil }\n"}}
	why := typeCheckPackage(root, "internal/protocol", bad)
	if why == "" {
		t.Fatal("a reference to an undefined type compiled")
	}
	if !strings.Contains(why, "Missing") {
		t.Errorf("the diagnostic does not name the fault: %s", why)
	}
	if !strings.Contains(why, "internal/protocol/types.go") {
		t.Errorf("the diagnostic names a temp file the model never saw, not the "+
			"generated path: %s", why)
	}
	// And the tenant is untouched — the overlay is why this is safe to run
	// before anything is written.
	if _, err := os.Stat(filepath.Join(root, "internal", "protocol", "types.go")); err == nil {
		t.Error("type checking wrote a file into the tenant")
	}
}

// A mechanism that cannot run is not the artifact being wrong.
func TestABrokenToolchainIsNotReportedAsABrokenPackage(t *testing.T) {
	// No go.mod: `go build` cannot resolve the package at all.
	root := t.TempDir()
	files := []GeneratedFile{{Path: "internal/protocol/types.go", Content: "package protocol\n"}}
	if why := typeCheckPackage(root, "internal/protocol", files); why != "" {
		t.Errorf("a toolchain refusal was reported as a broken package, which blames "+
			"the artifact for the tool: %s", why)
	}
	// Nothing to check produces nothing to say.
	if why := typeCheckPackage(root, "internal/protocol", nil); why != "" {
		t.Errorf("an empty file set produced a complaint: %s", why)
	}
}
