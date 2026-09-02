package dispatch

// Deciding whether a file must be rebuilt.
//
// architecture/generation, "Deciding whether a file must be rebuilt": a rebuild
// is a decision made per file and the default answer is KEEP. Six conditions,
// first match winning. This file implements that table and nothing else — every
// rule here is declared there, and a rule that is not declared there does not
// belong here.
//
// What this replaces: one digest over the whole prompt, which can answer "did
// anything change" and cannot answer "did anything I depend on change". A
// four-line amendment to one blueprint regenerated all thirty-five files of a
// component, and that was described as incremental.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RebuildVerdict is why a file will or will not be regenerated.
type RebuildVerdict string

const (
	// RebuildAbsent — no file on disk.
	RebuildAbsent RebuildVerdict = "absent"
	// RebuildEdited — the file differs from what generation wrote. REFUSED.
	RebuildEdited RebuildVerdict = "edited"
	// RebuildNonCompliant — the file no longer meets its plan entry.
	RebuildNonCompliant RebuildVerdict = "non-compliant"
	// RebuildReached — a blueprint changed and the change reaches this file.
	RebuildReached RebuildVerdict = "reached-by-change"
	// RebuildUnreached — a blueprint changed and it does not reach this file.
	RebuildUnreached RebuildVerdict = "assessed-unchanged"
	// RebuildUnchanged — nothing changed.
	RebuildUnchanged RebuildVerdict = "unchanged"
)

// Generates reports whether this verdict means the file is produced again.
//
// RebuildEdited generates NOTHING: it is a refusal, not an instruction. Reading
// it as "rebuild" would overwrite the edit this verdict exists to protect.
func (v RebuildVerdict) Generates() bool {
	switch v {
	case RebuildAbsent, RebuildNonCompliant, RebuildReached:
		return true
	}
	return false
}

// FileRecord is what one generated file was built from, at the granularity a
// change can be assessed against.
type FileRecord struct {
	Path     string            `json:"path"`
	Digest   string            `json:"digest"`
	Declares []string          `json:"declares,omitempty"`
	Serves   []string          `json:"serves,omitempty"`
	Purpose  string            `json:"purpose,omitempty"`
	// Blueprints maps blueprint path → the digest of the copy this file was
	// generated from. Per blueprint, not one digest over the corpus, so a change
	// can be attributed to the blueprint it happened in.
	Blueprints map[string]string `json:"blueprints,omitempty"`
}

// Decision is the outcome for one planned file.
type Decision struct {
	Path    string
	RebuildVerdict RebuildVerdict
	// Detail names what decided it — the blueprint that changed, the symbol that
	// went missing. Present so a report says why, not merely what.
	Detail string
}

// DecideRebuild applies the declared table to every file in a plan.
//
// prior is the previous run's records by path; blueprints is the corpus this run
// resolved. Both may be nil on a first run, which yields RebuildAbsent for
// everything and is correct.
func DecideRebuild(root string, plan *Plan, prior map[string]FileRecord,
	blueprints map[string]string) []Decision {

	// A nil map means the previous run recorded no per-file provenance at all —
	// a manifest written before records existed. That is a different situation
	// from a recorded run missing one path, and the two must not produce the
	// same message: one means "you changed this", the other means "I cannot tell
	// whether anyone did". Both refuse, because the rule exists to make sure
	// somebody is asked.
	legacy := prior == nil

	out := make([]Decision, 0, len(plan.Files))
	for _, f := range plan.Order() {
		out = append(out, decideOne(root, plan, f, prior[f.Path], blueprints, legacy))
	}
	return out
}

func decideOne(root string, plan *Plan, f PlannedFile, rec FileRecord,
	blueprints map[string]string, legacy bool) Decision {

	d := Decision{Path: f.Path}
	full := filepath.Join(root, plan.Root, f.Path)

	body, err := os.ReadFile(full)
	if err != nil {
		d.RebuildVerdict = RebuildAbsent
		d.Detail = "not present"
		return d
	}
	onDisk := digestString(string(body))

	// No record: generation never wrote this, or the record was lost. Treated as
	// edited rather than absent — the file exists and generation cannot show it
	// authored it, so overwriting it would destroy work of unknown origin.
	if rec.Digest == "" {
		d.RebuildVerdict = RebuildEdited
		if legacy {
			d.Detail = "the previous run recorded no per-file provenance, so an edit cannot be ruled out"
		} else {
			d.Detail = "present with no record of generation having written it"
		}
		return d
	}
	if onDisk != rec.Digest {
		d.RebuildVerdict = RebuildEdited
		d.Detail = "changed since generation wrote it"
		return d
	}

	// Compliance, before any question about inputs. A file is not kept because
	// its inputs look unchanged; it is kept because it still meets its
	// obligations.
	if missing := unmetObligations(string(body), f); missing != "" {
		d.RebuildVerdict = RebuildNonCompliant
		d.Detail = missing
		return d
	}

	// Which blueprints this file was generated from have changed.
	changed := changedBlueprints(rec, blueprints)
	if len(changed) == 0 {
		d.RebuildVerdict = RebuildUnchanged
		return d
	}
	// The cascade is not implemented, so any change to a blueprint this file was
	// generated from is treated as reaching it. architecture/generation permits
	// this and forbids calling it incremental: it never keeps a file it should
	// have rebuilt, and it rebuilds many it should have kept.
	d.RebuildVerdict = RebuildReached
	d.Detail = "changed: " + strings.Join(changed, ", ")
	return d
}

// unmetObligations reports the first obligation the file no longer meets.
func unmetObligations(body string, f PlannedFile) string {
	if len(f.Declares) > 0 {
		have := map[string]bool{}
		for _, d := range ExtractDeclarations(f.Path, body) {
			have[d.Name] = true
		}
		var missing []string
		for _, want := range f.Declares {
			if !have[want] {
				missing = append(missing, want)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return "no longer declares " + strings.Join(missing, ", ")
		}
	}
	var unserved []string
	for _, ep := range f.Serves {
		// A plan entry serves "POST /v1/register"; the file carries the path.
		parts := strings.Fields(ep)
		path := parts[len(parts)-1]
		if !strings.Contains(body, path) {
			unserved = append(unserved, ep)
		}
	}
	if len(unserved) > 0 {
		sort.Strings(unserved)
		return "no longer serves " + strings.Join(unserved, ", ")
	}
	return ""
}

// changedBlueprints names the blueprints this file was generated from whose
// content now differs.
func changedBlueprints(rec FileRecord, blueprints map[string]string) []string {
	var out []string
	for path, was := range rec.Blueprints {
		now, present := blueprints[path]
		if !present {
			out = append(out, path+" (no longer present)")
			continue
		}
		if digestString(now) != was {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

func digestString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// BuildFileRecords produces the records to persist for a completed run.
func BuildFileRecords(plan *Plan, files []GeneratedFile, blueprints map[string]string) map[string]FileRecord {
	byPath := map[string]PlannedFile{}
	for _, pf := range plan.Files {
		byPath[pf.Path] = pf
	}
	bpDigests := map[string]string{}
	for path, body := range blueprints {
		bpDigests[path] = digestString(body)
	}
	out := map[string]FileRecord{}
	for _, f := range files {
		pf := byPath[f.Path]
		out[f.Path] = FileRecord{
			Path:       f.Path,
			Digest:     digestString(f.Content),
			Declares:   pf.Declares,
			Serves:     pf.Serves,
			Purpose:    pf.Purpose,
			Blueprints: bpDigests,
		}
	}
	return out
}

// ReportDecisions renders the decisions for an operator, grouped by verdict.
func ReportDecisions(ds []Decision) string {
	byVerdict := map[RebuildVerdict][]Decision{}
	for _, d := range ds {
		byVerdict[d.RebuildVerdict] = append(byVerdict[d.RebuildVerdict], d)
	}
	var b strings.Builder
	order := []RebuildVerdict{RebuildEdited, RebuildNonCompliant, RebuildReached, RebuildAbsent, RebuildUnreached, RebuildUnchanged}
	labels := map[RebuildVerdict]string{
		RebuildEdited:       "edited since generation wrote them — REFUSED, nothing written",
		RebuildNonCompliant: "no longer meet their plan entry — regenerating",
		RebuildReached:      "reached by a blueprint change — regenerating",
		RebuildAbsent:       "absent — generating",
		RebuildUnreached:    "assessed and kept",
		RebuildUnchanged:    "unchanged — kept",
	}
	for _, v := range order {
		group := byVerdict[v]
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(&b, "  %d %s\n", len(group), labels[v])
		if v == RebuildEdited || v == RebuildNonCompliant {
			for _, d := range group {
				fmt.Fprintf(&b, "    %s — %s\n", d.Path, d.Detail)
			}
		}
	}
	return b.String()
}

// marshalRecords is the persisted form.
func marshalRecords(recs map[string]FileRecord) ([]byte, error) {
	keys := make([]string, 0, len(recs))
	for k := range recs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make([]FileRecord, 0, len(keys))
	for _, k := range keys {
		ordered = append(ordered, recs[k])
	}
	return json.Marshal(ordered)
}
