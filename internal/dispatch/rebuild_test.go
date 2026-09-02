package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One test per row of architecture/generation's rebuild table. The rows are the
// specification; these assert the implementation matches it, including the two
// rows that are easy to get backwards.

func rebuildFixture(t *testing.T) (root string, plan *Plan, bps map[string]string) {
	t.Helper()
	root = t.TempDir()
	plan = &Plan{Target: "content", Root: ".", Files: []PlannedFile{
		{Path: "svc.go", Purpose: "the service", Declares: []string{"Service"},
			Serves: []string{"GET /v1/content"}},
	}}
	bps = map[string]string{"architecture/content.md": "SPEC v1"}
	return root, plan, bps
}

const compliantFile = "package content\n\ntype Service struct{}\n\n// route: /v1/content\n"

func writeAndRecord(t *testing.T, root string, plan *Plan, bps map[string]string, body string) map[string]FileRecord {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "svc.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return BuildFileRecords(plan, []GeneratedFile{{Path: "svc.go", Content: body}}, bps)
}

func onlyDecision(t *testing.T, ds []Decision) Decision {
	t.Helper()
	if len(ds) != 1 {
		t.Fatalf("want one decision, got %d", len(ds))
	}
	return ds[0]
}

func TestRebuildRowAbsent(t *testing.T) {
	root, plan, bps := rebuildFixture(t)
	d := onlyDecision(t, DecideRebuild(root, plan, nil, bps))
	if d.RebuildVerdict != RebuildAbsent || !d.RebuildVerdict.Generates() {
		t.Errorf("absent file: got %q generates=%v", d.RebuildVerdict, d.RebuildVerdict.Generates())
	}
}

// The row that protects hand work. It must NOT generate.
func TestRebuildRowEditedRefusesAndDoesNotGenerate(t *testing.T) {
	root, plan, bps := rebuildFixture(t)
	prior := writeAndRecord(t, root, plan, bps, compliantFile)
	// Someone patches it, exactly as ten files of a real tenant were patched.
	if err := os.WriteFile(filepath.Join(root, "svc.go"),
		[]byte(compliantFile+"\n// hand fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := onlyDecision(t, DecideRebuild(root, plan, prior, bps))
	if d.RebuildVerdict != RebuildEdited {
		t.Fatalf("got %q, want edited", d.RebuildVerdict)
	}
	if d.RebuildVerdict.Generates() {
		t.Error("an edited file was scheduled for regeneration — the edit would be destroyed")
	}
}

// A file present with no record is treated as edited, not as absent.
func TestRebuildUnrecordedFileIsNotOverwritten(t *testing.T) {
	root, plan, bps := rebuildFixture(t)
	if err := os.WriteFile(filepath.Join(root, "svc.go"), []byte(compliantFile), 0o644); err != nil {
		t.Fatal(err)
	}
	d := onlyDecision(t, DecideRebuild(root, plan, map[string]FileRecord{}, bps))
	if d.RebuildVerdict != RebuildEdited {
		t.Errorf("got %q, want edited — generation cannot show it authored this file", d.RebuildVerdict)
	}
}

// Compliance is checked before inputs: unchanged inputs do not excuse a file
// that stopped meeting its plan entry.
func TestRebuildRowNonCompliantEvenWhenNothingChanged(t *testing.T) {
	root, plan, bps := rebuildFixture(t)
	body := "package content\n\n// Service was removed\n// route: /v1/content\n"
	prior := writeAndRecord(t, root, plan, bps, body)

	d := onlyDecision(t, DecideRebuild(root, plan, prior, bps))
	if d.RebuildVerdict != RebuildNonCompliant {
		t.Fatalf("got %q, want non-compliant", d.RebuildVerdict)
	}
	if !d.RebuildVerdict.Generates() {
		t.Error("a non-compliant file must be regenerated")
	}
	if d.Detail == "" || !strings.Contains(d.Detail, "Service") {
		t.Errorf("detail must name what is missing, got %q", d.Detail)
	}
}

func TestRebuildRowNonCompliantWhenAnEndpointIsGone(t *testing.T) {
	root, plan, bps := rebuildFixture(t)
	body := "package content\n\ntype Service struct{}\n"
	prior := writeAndRecord(t, root, plan, bps, body)
	d := onlyDecision(t, DecideRebuild(root, plan, prior, bps))
	if d.RebuildVerdict != RebuildNonCompliant || !strings.Contains(d.Detail, "/v1/content") {
		t.Errorf("got %q detail=%q, want non-compliant naming the endpoint", d.RebuildVerdict, d.Detail)
	}
}

func TestRebuildRowReachedByABlueprintChange(t *testing.T) {
	root, plan, bps := rebuildFixture(t)
	prior := writeAndRecord(t, root, plan, bps, compliantFile)

	changed := map[string]string{"architecture/content.md": "SPEC v2 — amended"}
	d := onlyDecision(t, DecideRebuild(root, plan, prior, changed))
	if d.RebuildVerdict != RebuildReached || !d.RebuildVerdict.Generates() {
		t.Fatalf("got %q, want reached-by-change", d.RebuildVerdict)
	}
	if !strings.Contains(d.Detail, "architecture/content.md") {
		t.Errorf("detail must name the blueprint that changed, got %q", d.Detail)
	}
}

// The row that makes a rebuild incremental: nothing changed, so nothing is done.
func TestRebuildRowUnchangedIsKept(t *testing.T) {
	root, plan, bps := rebuildFixture(t)
	prior := writeAndRecord(t, root, plan, bps, compliantFile)
	d := onlyDecision(t, DecideRebuild(root, plan, prior, bps))
	if d.RebuildVerdict != RebuildUnchanged {
		t.Fatalf("got %q, want unchanged", d.RebuildVerdict)
	}
	if d.RebuildVerdict.Generates() {
		t.Error("an unchanged, compliant file was scheduled for regeneration")
	}
}

// A blueprint disappearing from the installation is a change that reaches every
// file built from it — silence there would keep files against a spec nobody has.
func TestRebuildBlueprintNoLongerPresentReaches(t *testing.T) {
	root, plan, bps := rebuildFixture(t)
	prior := writeAndRecord(t, root, plan, bps, compliantFile)
	d := onlyDecision(t, DecideRebuild(root, plan, prior, map[string]string{}))
	if d.RebuildVerdict != RebuildReached || !strings.Contains(d.Detail, "no longer present") {
		t.Errorf("got %q detail=%q", d.RebuildVerdict, d.Detail)
	}
}

// A legacy manifest and a recorded run missing a path both refuse, and must not
// say the same thing: one means "you changed this", the other means "I cannot
// tell whether anyone did".
func TestRebuildLegacyManifestSaysItCannotTell(t *testing.T) {
	root, plan, bps := rebuildFixture(t)
	if err := os.WriteFile(filepath.Join(root, "svc.go"), []byte(compliantFile), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy := onlyDecision(t, DecideRebuild(root, plan, nil, bps))
	known := onlyDecision(t, DecideRebuild(root, plan, map[string]FileRecord{}, bps))

	if legacy.RebuildVerdict != RebuildEdited || known.RebuildVerdict != RebuildEdited {
		t.Fatalf("both must refuse: legacy=%q known=%q", legacy.RebuildVerdict, known.RebuildVerdict)
	}
	if legacy.Detail == known.Detail {
		t.Errorf("the two causes report identically: %q", legacy.Detail)
	}
	if !strings.Contains(legacy.Detail, "cannot be ruled out") {
		t.Errorf("legacy detail should say it cannot tell, got %q", legacy.Detail)
	}
}

// A first build has no files, so nothing is refused — the guard must not block
// a tenant that has never been generated.
func TestRebuildFirstBuildRefusesNothing(t *testing.T) {
	root, plan, bps := rebuildFixture(t)
	for _, d := range DecideRebuild(root, plan, nil, bps) {
		if d.RebuildVerdict != RebuildAbsent {
			t.Errorf("%s: got %q, want absent on a first build", d.Path, d.RebuildVerdict)
		}
	}
}

// A repair is generation's own work, not an edit.
//
// The manifest was recorded straight after generation and build-and-repair then
// rewrote files, so every repaired file differed from its record and the next
// run refused seven of them as hand-edited. Only a change generation did not
// make is an edit, so the record must describe what is on disk when the run
// ends.
func TestARepairedFileIsNotReportedAsEdited(t *testing.T) {
	root, plan, bps := rebuildFixture(t)

	// Generated, recorded, then repaired — and re-recorded, as the pipeline now
	// does after BuildAndRepair returns.
	_ = writeAndRecord(t, root, plan, bps, compliantFile)
	repaired := compliantFile + "\n// fixed by the repair loop\n"
	prior := writeAndRecord(t, root, plan, bps, repaired)

	d := onlyDecision(t, DecideRebuild(root, plan, prior, bps))
	if d.RebuildVerdict == RebuildEdited {
		t.Fatalf("a repaired file was refused as hand-edited: %s", d.Detail)
	}
	if d.RebuildVerdict != RebuildUnchanged {
		t.Errorf("got %q, want unchanged", d.RebuildVerdict)
	}
}
