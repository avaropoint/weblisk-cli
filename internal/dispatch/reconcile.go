package dispatch

// Reconciling a target directory with the plan that is about to be written into it.
//
// # The fault
//
// `server init --resume` wrote a new plan's files into a directory still holding
// the previous plan's. The two plans differed by one file — one run split the
// registry into routing.go, the next into registry.go — and the result was every
// symbol in that file declared twice:
//
//	./routing.go:35:6: Registry redeclared in this block
//	./routing.go:172:20: method Registry.ValidateSubscriptions already declared
//
// Nine correct files and one leftover produced a build that could not be repaired,
// because nothing was wrong with any file. The directory was wrong.
//
// A plan is not additive. It is a complete statement of what the target consists
// of, so writing it means the target consists of that and nothing else generation
// put there.
//
// # Why a manifest rather than "delete what is not in the plan"
//
// Deleting everything absent from the plan would delete a file somebody added by
// hand — a test, a README, a local experiment — which is a far worse mistake than
// the one being fixed. So generation records what it wrote, and reconciliation
// removes only from that record. Anything else in the directory is reported and
// left alone: it is not generation's to remove, and it is not generation's to
// hide either.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// manifestName is the record of what generation wrote for one COMPONENT.
//
// Keyed by target, not by the plan's root directory. A tenant root is the Go
// module root, so every component of a tenant plans into "." — and keying by
// that meant the orchestrator and the content service shared one manifest. The
// second build then read the first's 49 files as "written by a previous run and
// absent from this plan" and deleted go.mod out from under a working tenant.
func manifestName(root, target string) string {
	if target == "" {
		target = "orchestrator" // manifests written before targets were recorded
	}
	h := sha256.Sum256([]byte(filepath.Clean(target)))
	return filepath.Join(root, cacheDirName, "written-"+hex.EncodeToString(h[:6])+".json")
}

type writtenManifest struct {
	// Target is the component that wrote these files. Recorded, not derived from
	// the filename, so another component reading this manifest can say WHOSE
	// files these are rather than merely that they are somebody's.
	Target string   `json:"target"`
	Root   string   `json:"root"`
	Files  []string `json:"files"`
}

// RecordWritten saves the paths generation produced for a target.
//
// Failure is not fatal: losing the record costs a future resume its ability to
// clean up, and refusing to finish a successful generation over it would cost
// more.
func RecordWritten(root string, plan *Plan, files []GeneratedFile) {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	sort.Strings(paths)
	b, err := json.Marshal(writtenManifest{Target: plan.Target, Root: plan.Root, Files: paths})
	if err != nil {
		return
	}
	name := manifestName(root, plan.Target)
	_ = os.MkdirAll(filepath.Dir(name), 0o755)
	_ = os.WriteFile(name, b, 0o644)
}

// Reconciliation describes what a resume found in the target directory.
type Reconciliation struct {
	// Stale are files generation wrote previously that this plan does not
	// include. They are removed.
	Stale []string
	// Foreign are files in the target that generation never wrote. They are left
	// alone, and reported.
	Foreign []string
	// Retained are files a previous run wrote that this plan omits BECAUSE it was
	// told to. Kept, and reported separately from Foreign so a reader can tell
	// "generation never owned this" from "generation owns it and was asked to
	// leave it alone".
	Retained []string
}

// ReconcileTarget removes files a previous generation wrote that the current plan
// does not include, and reports anything it did not write.
func ReconcileTarget(root string, plan *Plan, st *TenantState) (Reconciliation, error) {
	var rec Reconciliation
	dir := filepath.Join(root, plan.Root)

	planned := map[string]bool{}
	for _, f := range plan.Files {
		planned[filepath.Clean(f.Path)] = true
	}

	previous := map[string]bool{}
	if b, err := os.ReadFile(manifestName(root, plan.Target)); err == nil {
		var m writtenManifest
		if json.Unmarshal(b, &m) == nil {
			for _, p := range m.Files {
				previous[filepath.Clean(p)] = true
			}
		}
	}

	// Paths this run told the planner to omit. An omission that was requested is
	// not a deletion, and deleting go.mod out of a working tenant is how that
	// distinction announced itself.
	protected := st.Protected()

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return rec, nil
		}
		return rec, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		clean := filepath.Clean(name)
		if planned[clean] {
			continue
		}
		if !previous[clean] {
			rec.Foreign = append(rec.Foreign, name)
			continue
		}
		if protected[clean] {
			rec.Retained = append(rec.Retained, name)
			continue
		}
		// Written by a previous generation, absent from this plan.
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return rec, err
		}
		rec.Stale = append(rec.Stale, name)
	}
	sort.Strings(rec.Stale)
	sort.Strings(rec.Foreign)
	sort.Strings(rec.Retained)
	return rec, nil
}
