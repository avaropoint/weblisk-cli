package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A component is never shown its own previous output as existing code.
//
// After one content build, internal/content held 340 exported names. Because
// self-owned files are excluded from Owned, the package rendered with no owner
// — which reads as hand-written, the most protected category — and the prompt
// told the next content build not to re-declare its own types.
func TestAComponentIsNotShownItsOwnPreviousOutput(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module hubgen\n\ngo 1.27\n")
	write("internal/protocol/types.go", "package protocol\n\ntype ErrorResponse struct{}\n")
	write("internal/content/custody.go", "package content\n\ntype ContentRepository struct{}\n")
	write("cmd/content/main.go", "package main\n\nfunc main() {}\n")

	// The orchestrator's manifest claims the protocol package.
	m, _ := json.Marshal(writtenManifest{Target: "orchestrator", Root: ".",
		Files: []string{"internal/protocol/types.go"}})
	write(filepath.Join(cacheDirName, "written-"+"aaaaaaaaaaaa"+".json"), string(m))
	os.Rename(filepath.Join(root, cacheDirName, "written-aaaaaaaaaaaa.json"),
		manifestName(root, "orchestrator"))

	st := ReadTenantState(root, "content", LayoutOf(Component{Kind: "content"}, "go"))

	if st.Module != "hubgen" {
		t.Errorf("module = %q, want hubgen", st.Module)
	}
	surface := st.FormatTenantPackages()
	if !strings.Contains(surface, "internal/protocol") || !strings.Contains(surface, "ErrorResponse") {
		t.Error("another component's importable surface is missing — files will guess its symbols")
	}
	for _, mine := range []string{"internal/content", "cmd/content", "ContentRepository"} {
		if strings.Contains(surface, mine) {
			t.Errorf("the content build is shown its own %q as pre-existing code", mine)
		}
	}
}

// The plan is re-derived when the tenant's STRUCTURE changes, and never merely
// because generated code gained a symbol.
//
// This is the incremental-build property, and it did not hold: Shape carried
// each package's export COUNT, which changes on almost every build, so the plan
// cache missed every run after the first. The model then re-planned, invented
// different method names for the same contract, and every file that declared
// the old names was regenerated as non-compliant — a loop that fed itself.
func TestShapeIgnoresWhatAPackageExports(t *testing.T) {
	before := &TenantState{
		Module: "example.com/tenant",
		Packages: []TenantPackage{
			{Dir: "internal/orchestrator", Name: "orchestrator", Owner: "orchestrator",
				Exports: []string{"Get", "Put", "List"}},
		},
	}
	after := &TenantState{
		Module: "example.com/tenant",
		Packages: []TenantPackage{
			{Dir: "internal/orchestrator", Name: "orchestrator", Owner: "orchestrator",
				Exports: []string{"Get", "Put", "List", "Touch", "Directory", "Counts"}},
		},
	}
	if before.Shape() != after.Shape() {
		t.Fatalf("a package gaining exports changed the tenant's shape, so the plan will be re-derived:\n before: %q\n after:  %q",
			before.Shape(), after.Shape())
	}
}

// The property that makes a rebuild incremental: building a tenant must not
// change the plan key.
//
// This is the whole test, and it failed twice for two different reasons. First
// the export count — generating files changes what packages export. Then the
// module path — GENERATING CREATES go.mod, so the first run saw no module and
// the second saw one. Measured on a real tenant: "" then "tenant-v7", and the
// second run of a freshly built tenant re-planned every time.
//
// The question to ask of any key input is not "is it structural?" but "can a
// build produce it?"
func TestBuildingATenantDoesNotChangeItsShape(t *testing.T) {
	before := &TenantState{} // nothing generated yet: no module, no packages
	after := &TenantState{
		Module:   "tenant-v7", // written by the build
		Packages: []TenantPackage{
			// Every package belongs to the component being built, so all of them
			// are excluded from Packages — as they are on a real tenant.
		},
		SelfNames: []string{"Server", "New", "Config", "LoadConfig"}, // 275 in practice
	}
	if before.Shape() != after.Shape() {
		t.Fatalf("building the tenant changed its shape, so the second run re-plans:\n before: %q\n after:  %q",
			before.Shape(), after.Shape())
	}
}

// A module RENAME must not re-plan either — it changes every import, which is
// file content, and cacheKey hashes the whole file prompt. The plan that was
// correct before the rename is still correct after it.
func TestAModuleRenameDoesNotReDeriveThePlan(t *testing.T) {
	a := &TenantState{Module: "tenant-v7",
		Packages: []TenantPackage{{Dir: "internal/content", Name: "content", Owner: "content"}}}
	b := &TenantState{Module: "something-else",
		Packages: []TenantPackage{{Dir: "internal/content", Name: "content", Owner: "content"}}}
	if a.Shape() != b.Shape() {
		t.Errorf("a module rename re-derives the plan; it should only re-generate files:\n %q\n %q",
			a.Shape(), b.Shape())
	}
}

// But a real structural change MUST still re-plan, or this has been turned off.
func TestShapeNoticesRealStructuralChange(t *testing.T) {
	base := &TenantState{
		Module:   "example.com/tenant",
		Packages: []TenantPackage{{Dir: "internal/orchestrator", Name: "orchestrator", Owner: "orchestrator"}},
	}
	for name, changed := range map[string]*TenantState{
		"a new package": {
			Module: "example.com/tenant",
			Packages: []TenantPackage{
				{Dir: "internal/orchestrator", Name: "orchestrator", Owner: "orchestrator"},
				{Dir: "internal/content", Name: "content", Owner: "content"},
			},
		},
		"ownership moved": {
			Module:   "example.com/tenant",
			Packages: []TenantPackage{{Dir: "internal/orchestrator", Name: "orchestrator", Owner: "content"}},
		},
	} {
		if base.Shape() == changed.Shape() {
			t.Errorf("%s did not change the tenant's shape", name)
		}
	}
}

// A component must be shown the names it declared last time, and must NOT be
// shown its own files as somebody else's package to import.
//
// Both halves matter and they pull in opposite directions. Presenting the
// component's own output as an existing package told a regeneration its types
// already existed somewhere it must not re-declare them. Removing it entirely
// left the planner nothing to be consistent with, so a re-derived plan renamed
// symbols at random and every file that used the old names was rebuilt.
func TestAComponentSeesItsOwnNamesButNotItsOwnPackages(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/tenant\n\ngo 1.27\n")
	write("internal/orchestrator/registry.go", `package orchestrator

type Registry struct{}

func NewRegistry() *Registry { return nil }
`)
	write("internal/protocol/types.go", `package protocol

type Agent struct{}
`)

	st := ReadTenantState(root, "orchestrator", LayoutOf(Orchestrator(), "go"))

	if !containsStr(st.SelfNames, "NewRegistry") || !containsStr(st.SelfNames, "Registry") {
		t.Fatalf("the component was not shown its own previous names: %v", st.SelfNames)
	}
	for _, p := range st.Packages {
		if p.Dir == "internal/orchestrator" {
			t.Fatal("the component's own package was offered to it as one to import")
		}
	}

	prompt := st.FormatTenantState()
	if !strings.Contains(prompt, "KEEP a name where the") {
		t.Fatal("the planner is not told to keep names it was not asked to change")
	}
	if !strings.Contains(prompt, "NewRegistry") {
		t.Fatal("the previous names never reach the prompt")
	}

	// And the naming baseline must stay out of the cache key, or it
	// reintroduces the churn loop it exists to stop.
	if strings.Contains(st.Shape(), "NewRegistry") {
		t.Fatal("generated symbol names leaked into the plan cache key")
	}
}

func containsStr(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}
