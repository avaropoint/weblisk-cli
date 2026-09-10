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
	Target string `json:"target"`
	Root   string `json:"root"`
	// Platform is the platform blueprint these files were generated against.
	//
	// Recorded so a component can be found again without guessing. A manifest
	// written before this existed simply has none, and Locate falls back to
	// probing each platform's layout as it always did.
	Platform string   `json:"platform,omitempty"`
	Files    []string `json:"files"`
	// Records is what each file was built FROM, at the granularity a change can
	// be assessed against — see rebuild.go. Files above stays as the flat list
	// reconcile needs; records answer a different question and a manifest
	// written before they existed simply has none.
	Records []FileRecord `json:"records,omitempty"`
	// Provenance is what produced these files: which blueprint sources, at
	// which revision, and which model.
	//
	// architecture/cli requires it — "the provider that generated a hub, and
	// the blueprint version it generated from, are part of that hub's
	// provenance and MUST be recorded". It was PRINTED and not recorded, so a
	// generated hub carried no statement of what governed it, and the one
	// question this whole product exists to answer — what does this artifact
	// come from — had to be reconstructed from a terminal scrollback.
	Provenance *Provenance `json:"provenance,omitempty"`
}

// Provenance records what a component was generated from.
type Provenance struct {
	// GeneratedAt is when, in UTC. A provenance record with no time on it
	// invites being read as current.
	GeneratedAt string `json:"generated_at"`
	// Sources are the blueprint sources read, each with its revision. A
	// revision ending "-dirty" means the corpus had uncommitted changes and
	// the artifact came from a tree that has no name.
	Sources []ProvenanceSource `json:"sources"`
	// Model is what the provider reported using. Empty when the provider did
	// not say, which is recorded as empty rather than guessed at.
	Model string `json:"model,omitempty"`
	// Provider is the backend kind — claude-code, ollama, anthropic.
	Provider string `json:"provider,omitempty"`
}

// ProvenanceSource is one blueprint source and the revision it was at.
type ProvenanceSource struct {
	Kind     string `json:"kind"` // project | custom | core
	Dir      string `json:"dir"`
	Revision string `json:"revision,omitempty"`
	Used     int    `json:"used"`
}

// planOwner is the instance a plan belongs to, for keying its manifest.
//
// Falls back to Target so a plan cached before Owner existed — and every
// manifest already written by one — is still found. For the singletons the two
// are equal anyway.
func planOwner(p *Plan) string {
	if p.Owner != "" {
		return p.Owner
	}
	return p.Target
}

// PriorRecords returns the previous run's per-file records for a component.
//
// Absent or unreadable yields an empty map, which DecideRebuild reads as "no
// record" — and a file present with no record is refused, not overwritten.
func PriorRecords(root, target string) map[string]FileRecord {
	b, err := os.ReadFile(manifestName(root, target))
	if err != nil {
		return nil // no previous run at all
	}
	var m writtenManifest
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	if len(m.Records) == 0 {
		return nil // a manifest from before records existed
	}
	out := make(map[string]FileRecord, len(m.Records))
	for _, r := range m.Records {
		out[r.Path] = r
	}
	return out
}

// RecordWritten saves the paths generation produced for a target.
//
// Failure is not fatal: losing the record costs a future resume its ability to
// clean up, and refusing to finish a successful generation over it would cost
// more.
func RecordWritten(root string, plan *Plan, files []GeneratedFile) {
	RecordWrittenWith(root, plan, files, nil)
}

// RecordWrittenWith also persists what each file was generated from.
func RecordWrittenWith(root string, plan *Plan, files []GeneratedFile, blueprints map[string]string) {
	seen := map[string]bool{}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		clean := filepath.ToSlash(filepath.Clean(f.Path))
		if !seen[clean] {
			seen[clean] = true
			paths = append(paths, clean)
		}
	}
	// Ownership is not forgotten because a plan stopped naming a file.
	//
	// The manifest is the ONLY evidence that a file on disk is generation's to
	// remove — reconcile leaves anything else alone, correctly. Replacing the
	// list wholesale meant a file written by one plan and dropped by the next
	// vanished from the record while still sitting on disk, so it became
	// indistinguishable from a hand-written file and could never be cleaned up.
	//
	// Reconcile runs BEFORE generation and deletes what this plan drops, so in
	// the ordinary case this union adds nothing. It matters when a run is
	// interrupted between writing files and recording them: without it, that
	// run's output is orphaned permanently.
	//
	// Only paths that still EXIST are carried forward, so the manifest cannot
	// grow without bound as a tenant is restructured.
	dir := filepath.Join(root, plan.Root)
	if b, err := os.ReadFile(manifestName(root, planOwner(plan))); err == nil {
		var prior writtenManifest
		if json.Unmarshal(b, &prior) == nil {
			for _, p := range prior.Files {
				clean := filepath.ToSlash(filepath.Clean(p))
				if seen[clean] {
					continue
				}
				if _, statErr := os.Stat(filepath.Join(dir, clean)); statErr != nil {
					continue // gone; nothing left to own
				}
				seen[clean] = true
				paths = append(paths, clean)
			}
		}
	}
	sort.Strings(paths)
	var recs []FileRecord
	if blueprints != nil {
		byPath := BuildFileRecords(filepath.Join(root, plan.Root), plan, files, blueprints)
		for _, p := range paths {
			if r, ok := byPath[p]; ok {
				recs = append(recs, r)
			}
		}
	}
	b, err := json.Marshal(writtenManifest{
		Target: planOwner(plan), Root: plan.Root, Platform: plan.Platform,
		Files: paths, Records: recs, Provenance: currentProvenance,
	})
	if err != nil {
		return
	}
	name := manifestName(root, planOwner(plan))
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
	if b, err := os.ReadFile(manifestName(root, planOwner(plan))); err == nil {
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

	// Walked recursively, because a plan's paths are relative and nested.
	//
	// This used to call os.ReadDir on plan.Root and skip every entry that was a
	// directory. plan.Root is "." — the tenant IS the module root — so it saw
	// go.mod, cmd/ and internal/, skipped the two directories, and examined
	// nothing. Reconciliation has therefore never removed a stale file since
	// platforms/go specified a module with cmd/ and internal/ packages.
	//
	// It announced itself as a build failure: one plan named the audit file
	// auditlog.go and the next named it audit.go, so both existed and the
	// package declared Auditor twice. Eleven redeclaration errors, none of them
	// a fault in any generated file.
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return rec, nil
		}
		return rec, err
	}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		if info.IsDir() {
			// Never descend into what generation does not own. bin/ is build
			// output, blueprints/ is the corpus, and .weblisk holds the keys.
			switch base := filepath.Base(path); {
			case path == dir:
				return nil
			case strings.HasPrefix(base, "."), base == "bin", base == "blueprints",
				base == "node_modules", base == "vendor", base == "target":
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		if strings.HasPrefix(filepath.Base(rel), ".") {
			return nil
		}
		if planned[rel] {
			return nil
		}
		if !previous[rel] {
			// Not ours to remove. A file this target cannot prove it wrote is
			// reported and left alone — that property is what makes the walk
			// safe to widen, and it must not be relaxed.
			rec.Foreign = append(rec.Foreign, rel)
			return nil
		}
		if protected[rel] {
			rec.Retained = append(rec.Retained, rel)
			return nil
		}
		// Written by a previous generation of THIS target, absent from this plan.
		if err := os.Remove(path); err != nil {
			return err
		}
		rec.Stale = append(rec.Stale, rel)
		return nil
	})
	if err != nil {
		return rec, err
	}
	sort.Strings(rec.Stale)
	sort.Strings(rec.Foreign)
	sort.Strings(rec.Retained)
	return rec, nil
}

// PriorPaths is the set of files the manifest records this target as owning.
//
// Exported so the ownership rule can be asserted directly: reconcile removes
// only what appears here, so a test that checks deletion behaviour without
// checking what is claimed is testing half the mechanism.
func PriorPaths(root, target string) []string {
	b, err := os.ReadFile(manifestName(root, target))
	if err != nil {
		return nil
	}
	var m writtenManifest
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m.Files
}

// currentProvenance is what this run read and what generated it.
//
// Package-level because it is a property of the RUN, not of a file: every
// manifest written by one invocation records the same sources and the same
// model, and threading it through six call sites would invite one of them
// recording something different.
var currentProvenance *Provenance

// SetProvenance records what this run is generating from.
func SetProvenance(p *Provenance) { currentProvenance = p }
