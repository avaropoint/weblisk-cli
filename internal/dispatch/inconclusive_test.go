package dispatch

import (
	"strings"
	"testing"
)

// "Cannot read the source" and "the source contradicts this" are different
// answers and must be reported as such.
//
// One real run printed 25 lines of "structural checks disagree with the
// specification" whose every detail began "cannot tell whether a handler is
// registered … could not be resolved to a path". The detail was honest and the
// verdict was not: a generated route table is a loop over constants this tool
// cannot resolve, which is a fact about the tool. A checker that reports what
// it does not know as a fault is one people learn to ignore, and 25 at once
// teaches that in a single sitting.
func TestAnUnreadableSourceIsInconclusiveNotRefuted(t *testing.T) {
	marked := markInconclusive("cannot tell whether a handler is registered for GET /v1/health")
	stripped, unresolved := splitInconclusive(marked)
	if !unresolved {
		t.Fatal("a marked detail was not recognised as inconclusive")
	}
	if strings.Contains(stripped, "\x00") {
		t.Errorf("the marker survived into the detail a person reads: %q", stripped)
	}
	if !strings.HasPrefix(stripped, "cannot tell") {
		t.Errorf("the detail was mangled: %q", stripped)
	}

	// An ordinary failure detail must NOT be read as inconclusive, or a real
	// refutation would be downgraded to "we could not tell".
	plain, unresolved2 := splitInconclusive("no handler is registered for: GET /v1/health")
	if unresolved2 {
		t.Error("an ordinary refutation was treated as inconclusive")
	}
	if plain != "no handler is registered for: GET /v1/health" {
		t.Errorf("an unmarked detail was altered: %q", plain)
	}
}

// The counts keep them apart, and inconclusive is NOT folded into unchecked —
// unchecked means no check exists, which is a different thing to review.
func TestInconclusiveIsCountedApart(t *testing.T) {
	results := []ChecklistResult{
		{Outcome: OutcomeVerified},
		{Outcome: OutcomeFailed},
		{Outcome: OutcomeInconclusive},
		{Outcome: OutcomeInconclusive},
		{Outcome: OutcomeUnchecked},
		{Outcome: OutcomeNotApplicable},
		{Outcome: OutcomeNecessary},
	}
	verified, failed, necessary, notApplicable, unchecked := ChecklistCounts(results)
	if verified != 1 || failed != 1 || necessary != 1 || notApplicable != 1 {
		t.Errorf("counts moved: v=%d f=%d n=%d na=%d", verified, failed, necessary, notApplicable)
	}
	if unchecked != 1 {
		t.Errorf("unchecked = %d, want 1 — inconclusive must not be folded in", unchecked)
	}
	if got := ChecklistInconclusive(results); got != 2 {
		t.Errorf("inconclusive = %d, want 2", got)
	}
}

// And it must not drive repair. Repair rewrites files to satisfy an assertion;
// asking a model to fix something this tool merely could not read would edit
// working code to chase a gap in the checker.
func TestInconclusiveDoesNotDriveRepair(t *testing.T) {
	results := []ChecklistResult{
		{Outcome: OutcomeInconclusive, Detail: "cannot tell"},
		{Outcome: OutcomeFailed, Detail: "genuinely wrong"},
	}
	failed := FailedChecklist(results)
	if len(failed) != 1 {
		t.Fatalf("FailedChecklist returned %d, want only the genuine failure", len(failed))
	}
	if failed[0].Detail != "genuinely wrong" {
		t.Errorf("repair was handed the inconclusive one: %q", failed[0].Detail)
	}
}

// An inconclusive result is not conclusive, and is not a pass.
func TestInconclusiveIsNeitherCheckedNorPassing(t *testing.T) {
	r := ChecklistResult{Outcome: OutcomeInconclusive}
	if r.Checked() {
		t.Error("an unresolvable check reported itself as checked, so it would count toward settled work")
	}
	if r.Passed() {
		t.Error("an unresolvable check reported itself as passing")
	}
}

// The marker must never reach printed output. It is a control character, and a
// build log containing NULs is a build log a terminal mangles.
func TestTheMarkerNeverReachesOutput(t *testing.T) {
	if !strings.Contains(inconclusivePrefix, "\x00") {
		t.Skip("the marker is no longer a control sequence; this guard is about that choice")
	}
	// Every path that sets OutcomeInconclusive must have stripped it.
	stripped, _ := splitInconclusive(markInconclusive("detail"))
	if strings.ContainsAny(stripped, "\x00") {
		t.Error("the marker leaked into a detail")
	}
}

// The WIRING, not the helpers.
//
// Everything above tests markInconclusive, splitInconclusive and the counts. It
// all passed while two mutations survived: removing markInconclusive from the
// route check, and removing the inconclusive branch from evaluateItem. Both are
// the original fault, restored, and neither touched a helper.
//
// So this drives the real path — a corpus with a route registered from a
// variable this tool cannot follow, and an assertion about that route — and
// asserts the OUTCOME.
func TestAnUnresolvableRouteTableYieldsInconclusiveThroughEvaluation(t *testing.T) {
	// A route table registered in a loop: exactly the shape a generated
	// orchestrator uses, and exactly what evalString cannot follow.
	files := []GeneratedFile{{
		Path: "internal/orchestrator/routes.go",
		Lang: "go",
		Content: `package orchestrator

import "net/http"

type route struct {
	pattern string
	handler http.HandlerFunc
}

func register(mux *http.ServeMux, routes []route) {
	for _, r := range routes {
		mux.HandleFunc(r.pattern, r.handler)
	}
}
`,
	}}

	items := []ChecklistItem{{
		Source: "protocol/spec.md",
		Text:   "`GET /v1/health` returns 200 without auth",
	}}

	results := EvaluateChecklistAgainst(items, files, map[string]string{})
	if len(results) != 1 {
		t.Fatalf("got %d result(s), want 1", len(results))
	}
	r := results[0]

	if r.Outcome == OutcomeFailed {
		t.Fatalf("an unresolvable route table REFUTED the assertion: %s\n"+
			"  This is the original fault: the tool could not read the route table, "+
			"and reported the component as contradicting its specification.", r.Detail)
	}
	if r.Outcome != OutcomeInconclusive {
		t.Fatalf("outcome = %q, want inconclusive (detail: %s)", r.Outcome, r.Detail)
	}
	if !strings.Contains(r.Detail, "cannot tell") {
		t.Errorf("the detail does not say it could not tell: %q", r.Detail)
	}
	if strings.Contains(r.Detail, "\x00") {
		t.Errorf("the marker reached the detail a person reads: %q", r.Detail)
	}

	// And it must not be handed to repair, which would rewrite working code to
	// chase a gap in this tool.
	if len(FailedChecklist(results)) != 0 {
		t.Error("an inconclusive result was handed to repair")
	}
}

// A route that is genuinely absent, with a route table this tool CAN read, must
// still be refuted — or the fix above would have turned every real finding into
// a shrug.
func TestAGenuinelyAbsentRouteIsStillRefuted(t *testing.T) {
	files := []GeneratedFile{{
		Path: "internal/orchestrator/routes.go",
		Lang: "go",
		Content: `package orchestrator

import "net/http"

func register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/services", handleServices)
}

func handleServices(w http.ResponseWriter, r *http.Request) {}
`,
	}}
	items := []ChecklistItem{{
		Source: "protocol/spec.md",
		Text:   "`GET /v1/health` returns 200 without auth",
	}}

	results := EvaluateChecklistAgainst(items, files, map[string]string{})
	if len(results) != 1 {
		t.Fatalf("got %d result(s)", len(results))
	}
	if results[0].Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q, want failed — every route here is legible, so an absent "+
			"one is a real refutation and must not be softened to inconclusive (detail: %s)",
			results[0].Outcome, results[0].Detail)
	}
	if len(FailedChecklist(results)) != 1 {
		t.Error("a genuine refutation was not handed to repair")
	}
}
