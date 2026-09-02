package dispatch

import (
	"strings"
	"testing"
)

// An assertion about a type the component binds nothing from is not its
// obligation.
//
// A clean orchestrator generation reported fifteen assertions unmet for types
// its own contract never claims — WorkflowPhase, TaskRequest, Finding,
// DeadLetterEntry, ScopeLevel, PolicyDecision, OperationIntent. That is the
// same list the pipeline correctly prints as "defined but unbound", used to
// mark the component non-conformant.
func TestAssertionsAboutUnboundTypesAreNotThisComponentsObligation(t *testing.T) {
	items := []ChecklistItem{
		{Text: "TaskRequest requires `id`, `from`, `action`, `payload`"},
		{Text: "No DeadLetterEntry field is optional"},
		{Text: "AgentManifest declares `publishes` and `subscriptions`"},
		{Text: "A ServiceDirectory signature covers the TaskRequest set"},
		{Text: "Every response body is valid JSON"},
	}
	bound := []string{"AgentManifest", "ServiceDirectory"}
	unbound := []string{"TaskRequest", "DeadLetterEntry", "WorkflowPhase"}

	kept, setAside := SplitByBoundTypes(items, bound, unbound)

	if len(setAside) != 2 {
		t.Errorf("set aside %d, want 2 (TaskRequest, DeadLetterEntry): %v", len(setAside), texts(setAside))
	}
	if len(kept) != 3 {
		t.Errorf("kept %d, want 3: %v", len(kept), texts(kept))
	}
	// The one naming both a bound and an unbound type is still an obligation.
	var keptBoth bool
	for _, it := range kept {
		if strings.Contains(it.Text, "ServiceDirectory signature") {
			keptBoth = true
		}
	}
	if !keptBoth {
		t.Error("an assertion relating a bound type to an unbound one was set aside; it is still about the bound one")
	}
	// An assertion naming no type at all addresses every implementation.
	var keptGeneric bool
	for _, it := range kept {
		if strings.Contains(it.Text, "valid JSON") {
			keptGeneric = true
		}
	}
	if !keptGeneric {
		t.Error("a type-free assertion was set aside")
	}
}

// Nothing is set aside when the component binds everything.
func TestNoUnboundTypesSetsNothingAside(t *testing.T) {
	items := []ChecklistItem{{Text: "TaskRequest requires `id`"}}
	kept, setAside := SplitByBoundTypes(items, []string{"TaskRequest"}, nil)
	if len(kept) != 1 || len(setAside) != 0 {
		t.Errorf("kept=%d setAside=%d, want 1/0", len(kept), len(setAside))
	}
}

// Word boundaries: Observation must not match inside ObservationStore.
func TestTypeMentionRespectsWordBoundaries(t *testing.T) {
	if mentionsType("The ObservationStore is append-only", "Observation") {
		t.Error("Observation matched inside ObservationStore")
	}
	if !mentionsType("An Observation carries findings", "Observation") {
		t.Error("Observation did not match on its own")
	}
	if !mentionsType("returns a TaskResult.", "TaskResult") {
		t.Error("a type followed by punctuation did not match")
	}
}

func texts(items []ChecklistItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Text)
	}
	return out
}

// The excluded summary must not print "22 for the " when the exclusion came
// from a binding rather than a group heading.
func TestExcludedSummaryNamesWhyNotJustHowMany(t *testing.T) {
	got := ExcludedSummary([]ChecklistItem{
		{Group: "Agent Protocol", Text: "a"},
		{Text: "TaskRequest requires id"},
		{Text: "DeadLetterEntry requires attempts"},
	})
	if strings.Contains(got, "for the ,") || strings.HasSuffix(got, "for the ") {
		t.Errorf("empty owner printed as a component: %q", got)
	}
	if !strings.Contains(got, "binds nothing from") {
		t.Errorf("summary does not say why they were set aside: %q", got)
	}
	if !strings.Contains(got, "1 for the agent") {
		t.Errorf("group-owned exclusions lost their owner: %q", got)
	}
}
