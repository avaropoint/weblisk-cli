package dispatch

// What a tenant already contains, read before anything is planned into it.
//
// # The failure this exists to prevent
//
// A tenant root is the Go module root, so every component a tenant grows is
// planned into the same module. The planner was told the blueprints and the
// platform and nothing else — so asked for a content service, it planned
// `module weblisk-tenant` over a module called `hubgen`, re-implemented
// internal/protocol, internal/identity and internal/storage, and put its entry
// point in cmd/orchestrator/main.go: the working orchestrator's.
//
// Twelve of thirty-two planned files duplicated packages the tenant already
// had, and one of them would have replaced the component that was running.
//
// The plan was not wrong about the blueprints. It was answering "what does a
// content service consist of" when the question is "what must this tenant grow
// in order to have one".

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TenantState is what a tenant already contains, from the tenant itself.
type TenantState struct {
	Module   string            // module path from go.mod, "" if none
	Owned    map[string]string // path → the component whose manifest claims it
	Packages []TenantPackage   // existing packages and what they export
	// SelfNames are the symbols the component being built declared last time.
	//
	// Kept apart from Packages on purpose. Packages is "code somebody else
	// owns — import it, do not re-declare it". These are this component's own
	// previous output, which this run may freely replace; they are shown as a
	// NAMING baseline, so a plan that is re-derived does not rename symbols
	// nothing asked it to rename.
	//
	// Two builds of the same blueprints produced Get/Put/List and then
	// GetAgent/PutAgent/ListAgents. No file was wrong and every file was
	// stale: ten compliant files were regenerated because the plan had been
	// made again. Excluding self was right — being shown its own output as
	// untouchable stopped a component changing anything — but excluding the
	// NAMES with it left the model nothing to be consistent with.
	//
	// Never part of Shape(): that is a cache key, and keying it on generated
	// symbol names is the feedback loop this whole area exists to remove.
	SelfNames []string
}

// TenantPackage is one existing package and the surface another component may
// import instead of re-declaring.
//
// A component's OWN packages are never listed. Its previous output is what this
// run replaces, so presenting it as existing code tells a regeneration that its
// own types already exist somewhere it must not re-declare them — and because
// self-owned files are excluded from Owned, the package appears unowned, which
// reads as hand-written and is the most protected category of all.
type TenantPackage struct {
	Dir     string   // import path suffix, e.g. "internal/protocol"
	Name    string   // package clause
	Owner   string   // component whose manifest claims its files, "" if unclaimed
	Exports []string // exported top-level declarations
}

// ReadTenantState inspects a tenant, excluding the component about to be built.
//
// Excluding self matters: a rebuild of the orchestrator must not be told the
// orchestrator's own files are somebody else's to leave alone.
//
// selfKey identifies the instance whose manifest is skipped; self is where that
// instance's files LIVE. The two were one string, and for an agent the string
// read "agent:billing" — from which the directory `internal/agent:billing` was
// derived, matching nothing, so a re-run of `agent create billing` was offered
// its own previous package as somebody's to import and never told the names it
// had just declared.
func ReadTenantState(root, selfKey string, self Layout) *TenantState {
	st := &TenantState{Owned: map[string]string{}}

	if b, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "module ") {
				st.Module = strings.TrimSpace(strings.TrimPrefix(line, "module "))
				break
			}
		}
	}

	// Who owns what, from the per-component manifests.
	entries, _ := os.ReadDir(filepath.Join(root, cacheDirName))
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "written-") {
			continue
		}
		owner, files := readManifestOwner(filepath.Join(root, cacheDirName, e.Name()))
		if owner == "" || owner == selfKey {
			continue
		}
		for _, f := range files {
			st.Owned[filepath.Clean(f)] = owner
		}
	}

	st.Packages, st.SelfNames = readTenantPackages(root, st.Owned, selfOwned(root, selfKey), self)
	return st
}

// selfOwned is the set of paths the component being built wrote last time.
func selfOwned(root, selfKey string) map[string]bool {
	out := map[string]bool{}
	if selfKey == "" {
		return out
	}
	_, files := readManifestOwner(manifestName(root, selfKey))
	for _, f := range files {
		out[filepath.Clean(f)] = true
	}
	return out
}

// readTenantPackages lists the Go packages present and what each exports.
//
// A directory is the component's own when the LAYOUT says so — checked by path
// as well as by manifest, because a first build has no manifest and a run
// interrupted before RecordWritten has a stale one.
func readTenantPackages(root string, owned map[string]string, mine map[string]bool, self Layout) ([]TenantPackage, []string) {
	byDir := map[string]*TenantPackage{}
	var selfNames []string
	fset := token.NewFileSet()

	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if base == ".weblisk" || base == ".git" || base == "bin" || base == "blueprints" ||
				base == "node_modules" || base == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil // unparseable is not this function's problem to report
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		if mine[filepath.Clean(rel)] || self.Owns(dir) {
			// This component's own output, which this run replaces. Not a
			// package to import — but its names are worth keeping.
			selfNames = append(selfNames, exportedDecls(f)...)
			return nil
		}
		p := byDir[dir]
		if p == nil {
			p = &TenantPackage{Dir: dir, Name: f.Name.Name, Owner: owned[filepath.Clean(rel)]}
			byDir[dir] = p
		}
		if p.Owner == "" {
			p.Owner = owned[filepath.Clean(rel)]
		}
		p.Exports = append(p.Exports, exportedDecls(f)...)
		return nil
	})

	out := make([]TenantPackage, 0, len(byDir))
	for _, p := range byDir {
		sort.Strings(p.Exports)
		p.Exports = dedupeStrings(p.Exports)
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	sort.Strings(selfNames)
	return out, dedupeStrings(selfNames)
}

// exportedDecls returns the exported top-level names a file declares.
func exportedDecls(f *ast.File) []string {
	var out []string
	for _, d := range f.Decls {
		switch n := d.(type) {
		case *ast.FuncDecl:
			if n.Recv != nil || !n.Name.IsExported() {
				continue // methods are reached through their type
			}
			out = append(out, n.Name.Name)
		case *ast.GenDecl:
			for _, spec := range n.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						out = append(out, s.Name.Name)
					}
				case *ast.ValueSpec:
					for _, id := range s.Names {
						if id.IsExported() {
							out = append(out, id.Name)
						}
					}
				}
			}
		}
	}
	return out
}

func readManifestOwner(path string) (string, []string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	var m writtenManifest
	if json.Unmarshal(b, &m) != nil {
		return "", nil
	}
	return m.Target, m.Files
}

func dedupeStrings(in []string) []string {
	out := in[:0]
	var prev string
	for i, s := range in {
		if i > 0 && s == prev {
			continue
		}
		out = append(out, s)
		prev = s
	}
	return out
}

// FormatTenantState renders what the tenant already has, for the plan prompt.
//
// What the tenant HAS. Where this component GOES is FormatLayout's, and the two
// were one block: the directories were stated inside this one, so a component
// generated into an empty directory — where this returns "" — was told nothing
// about where its own files belonged.
func (st *TenantState) FormatTenantState() string {
	if st == nil || (st.Module == "" && len(st.Packages) == 0) {
		return ""
	}
	var b strings.Builder
	b.WriteString("--- THIS TENANT ALREADY EXISTS ---\n\n")
	b.WriteString("You are adding ONE component to a tenant that is already built and running.\n")
	b.WriteString("You are not creating a project. Plan only what this tenant does not yet have.\n\n")
	if st.Module != "" {
		fmt.Fprintf(&b, "Module path: %s\n", st.Module)
		b.WriteString("  Import the tenant's own packages by this path. Do NOT plan go.mod —\n")
		b.WriteString("  it exists, and renaming the module breaks every package in the tenant.\n\n")
	}
	if len(st.Packages) > 0 {
		b.WriteString("Packages this tenant already provides. IMPORT these; do not re-declare\n")
		b.WriteString("what they already export, and do not plan a file in their directories:\n\n")
		for _, p := range st.Packages {
			owner := p.Owner
			if owner == "" {
				owner = "hand-written"
			}
			fmt.Fprintf(&b, "  %s (package %s, owned by %s)\n", p.Dir, p.Name, owner)
			if len(p.Exports) > 0 {
				fmt.Fprintf(&b, "      %s\n", wrapList(p.Exports, 72, "      "))
			}
		}
		b.WriteString("\n")
	}
	if len(st.SelfNames) > 0 {
		// Framed as a naming baseline, NOT as code to leave alone. The
		// distinction is the whole point: this component's previous output is
		// what this run replaces, and being told it is untouchable stops a
		// regeneration changing anything. Being told nothing about it makes a
		// re-derived plan rename things at random, which is what actually
		// happened — Get/Put/List became GetAgent/PutAgent/ListAgents and ten
		// correct files were rebuilt because of it.
		b.WriteString("Names YOUR component declared last time. This code is yours and this run\n")
		b.WriteString("replaces it — you may restructure it freely. But KEEP a name where the\n")
		b.WriteString("requirement behind it has not changed. Renaming something nothing asked\n")
		b.WriteString("you to rename makes every file that used it stale:\n\n")
		fmt.Fprintf(&b, "      %s\n\n", wrapList(st.SelfNames, 72, "      "))
	}
	if len(st.Owned) > 0 {
		b.WriteString("Files owned by another component. A plan naming any of these is rejected:\n")
		var paths []string
		for p := range st.Owned {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			fmt.Fprintf(&b, "  %s (%s)\n", p, st.Owned[p])
		}
		b.WriteString("\n")
	}
	return b.String()
}

func wrapList(items []string, width int, indent string) string {
	var lines []string
	cur := ""
	for _, it := range items {
		if cur != "" && len(cur)+len(it)+2 > width {
			lines = append(lines, cur)
			cur = ""
		}
		if cur != "" {
			cur += ", "
		}
		cur += it
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n"+indent)
}

// Shape is what a tenant IS, as opposed to everything it currently exports.
//
// Used as the plan's cache key input. A plan decides which files exist and where
// they go, so it must be re-made when the tenant's structure changes — a package
// appearing, a component taking ownership of a directory, the module being
// renamed. It must NOT be re-made because an unrelated component gained one
// exported constant, which is what keying on the full export text would do:
// every component in the tenant re-plans whenever any other one is regenerated.
//
// Exports still reach the FILE prompts, where the symbol names actually matter.
//
// # Why the export COUNT is not here either
//
// It was, and it made the plan cache miss on every run after the first. The
// count changes whenever a generated file gains a helper, so:
//
//	build → files written → export counts change → shape changes →
//	plan cache misses → the model re-plans → it invents different names
//	(Get/Put/List became GetAgent/PutAgent/ListAgents) → every file that
//	declared the old names is non-compliant → regenerate → counts change again
//
// A self-sustaining churn loop, and the reason a rebuild that should have cost
// nothing cost twenty files. One orchestrator run regenerated ten files whose
// only fault was that the plan had been re-derived since they were written.
//
// The count is not structure. A package gaining an export is the same package.
//
// # Nor is the module path, for the same reason
//
// It was here, and it had exactly the property the export count had: THE BUILD
// CREATES go.mod. So the first run saw no module, the second saw one, the key
// changed, and the plan was re-derived on the second run of every tenant that
// had ever been built — measured directly, "" then "tenant-v7".
//
// Removing the export count and leaving this was half a fix. The test to apply
// to any candidate key input is not "is it structural?" but "can a build
// produce it?" — and if it can, it is a feedback loop whatever else it is.
//
// The module path is not a plan input in any case. A plan decides which files
// exist, what each declares, what each serves and in what order. The module
// path decides IMPORT STATEMENTS, which is file content — and cacheKey hashes
// the whole rendered file prompt, so a module rename already invalidates every
// file precisely, without touching the plan that was correct before it.
//
// What remains is genuinely structural: the package directories, their names,
// and who owns them. Those change only when something has really moved.
func (st *TenantState) Shape() string {
	if st == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range st.Packages {
		fmt.Fprintf(&b, "%s %s %s\n", p.Dir, p.Name, p.Owner)
	}
	owned := make([]string, 0, len(st.Owned))
	for k, v := range st.Owned {
		owned = append(owned, k+"="+v)
	}
	sort.Strings(owned)
	b.WriteString(strings.Join(owned, "\n"))
	return b.String()
}

// FormatTenantPackages renders the importable surface for a file prompt.
//
// Separate from FormatTenantState: the plan needs to know what NOT to re-plan,
// a file needs to know what to IMPORT. A generated file that has the module path
// but not the symbol names guesses them, and a guessed identifier is a build
// error that costs a repair round trip to discover.
func (st *TenantState) FormatTenantPackages() string {
	if st == nil || len(st.Packages) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("--- PACKAGES THIS TENANT ALREADY PROVIDES ---\n\n")
	b.WriteString("Import these rather than re-declaring what they export. These names\n")
	b.WriteString("exist with these exact spellings; do not invent variants of them.\n\n")
	for _, p := range st.Packages {
		if len(p.Exports) == 0 {
			continue
		}
		imp := p.Dir
		if st.Module != "" {
			imp = st.Module + "/" + p.Dir
		}
		fmt.Fprintf(&b, "  %s  (package %s)\n      %s\n", imp, p.Name, wrapList(p.Exports, 72, "      "))
	}
	return b.String()
}

// Protected are paths this run INSTRUCTED the planner to leave out, which must
// therefore never be treated as stale.
//
// Reconcile's premise is that a plan is a complete statement of what a target
// consists of, so anything the last run wrote and this one omits was dropped
// deliberately. Tenant awareness broke that premise: the prompt now tells the
// planner "do NOT plan go.mod — it exists", the planner correctly omits it, and
// reconcile then read the omission as a deletion and removed go.mod from a
// working tenant.
//
// Both rules were right. Nothing carried the fact that an omission had been
// requested, so the second rule could not tell a dropped file from an excluded
// one.
func (st *TenantState) Protected() map[string]bool {
	out := map[string]bool{}
	if st == nil {
		return out
	}
	if st.Module != "" {
		// The exact instruction given in FormatTenantState. These two must not
		// drift: a path excluded there and not protected here is deleted.
		out["go.mod"] = true
	}
	for path := range st.Owned {
		out[path] = true
	}
	return out
}
