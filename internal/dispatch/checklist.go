package dispatch

// Verification Checklists as a gate.
//
// Every blueprint schema requires a "## Verification Checklist" — minimum five
// testable assertions, ten for agents. They are the blueprint's own statement of
// what a correct implementation looks like.
//
// Nothing read them. The four blueprints a Go orchestrator generation consumes
// carry 84 assertions between them, and the pipeline passed them to the model as
// undifferentiated prose and moved on. Checked by hand against the first
// generated hub, seven of eight platform assertions passed and one — "429 with
// Retry-After when at capacity", stated explicitly in platforms/go.md — failed
// silently and would have shipped.
//
// That is the distinction: the checklists were present as CONTEXT and absent as
// a CONTRACT. Compliance was left to whether the model happened to notice.
//
// architecture/generation.md Layer 3 requires that every mechanically checkable
// assertion be evaluated and the remainder reported as UNCHECKED — never as
// passed. An assertion nobody verified is not a satisfied one.

import (
	"regexp"
	"sort"
	"strings"
)

// ChecklistItem is one assertion from a blueprint.
type ChecklistItem struct {
	Source string // blueprint it came from
	Text   string
	// Group is the `###` subheading the assertion sits under, empty when
	// ungrouped. It is how a blueprint says which component an assertion is
	// addressed to — see ScopeChecklist.
	Group string
}

var reChecklistItem = regexp.MustCompile(`(?m)^-\s*\[\s*\]\s*(.+?)\s*$`)

var reChecklistGroup = regexp.MustCompile(`(?m)^###\s+(.+?)\s*$`)

// ExtractChecklist reads the Verification Checklist of one blueprint, recording
// the `###` group each assertion sits under.
//
// The group is not decoration. A protocol blueprint describes both ends of a
// conversation, so protocol/spec.md's checklist holds assertions no single
// implementation can satisfy — an orchestrator does not serve POST /v1/describe.
// The heading is the blueprint's own statement of which component an assertion is
// addressed to, which is why it is read rather than inferred from wording.
func ExtractChecklist(source, blueprint string) []ChecklistItem {
	i := strings.Index(blueprint, "## Verification Checklist")
	if i < 0 {
		return nil
	}
	rest := blueprint[i+len("## Verification Checklist"):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}

	// Walk lines so a group heading applies to the assertions that follow it.
	group := ""
	var out []ChecklistItem
	for _, line := range strings.Split(rest, "\n") {
		if m := reChecklistGroup.FindStringSubmatch(line); m != nil {
			group = strings.TrimSpace(m[1])
			continue
		}
		if m := reChecklistItem.FindStringSubmatch(line); m != nil {
			if t := strings.TrimSpace(m[1]); t != "" {
				out = append(out, ChecklistItem{Source: source, Text: t, Group: group})
			}
		}
	}
	return out
}

// componentGroups are the component names a checklist group may be addressed to.
//
// A closed list, from schemas/common.md. Anything else — "Event Publishing",
// "Cross-Cutting" — addresses every implementation.
var componentGroups = []string{"agent", "orchestrator", "domain", "gateway", "content"}

// ScopeChecklist splits assertions into this target's obligations and another
// component's.
//
// Excluded assertions are RETURNED, not dropped. An assertion left out silently
// is indistinguishable from one that was never written, and quietly narrowing a
// specification is how a pipeline comes to report success against a contract
// nobody agreed to.
func ScopeChecklist(items []ChecklistItem, target string) (mine, others []ChecklistItem) {
	target = strings.ToLower(strings.TrimSpace(target))
	for _, it := range items {
		owner := groupComponent(it.Group)
		if owner == "" || owner == target {
			mine = append(mine, it)
			continue
		}
		others = append(others, it)
	}
	return mine, others
}

// groupComponent returns the component a group heading is addressed to, or "".
func groupComponent(group string) string {
	lower := strings.ToLower(strings.TrimSpace(group))
	for _, c := range componentGroups {
		if lower == c || strings.HasPrefix(lower, c+" ") {
			return c
		}
	}
	return ""
}

// ExcludedSummary describes assertions another component owns, for reporting.
func ExcludedSummary(others []ChecklistItem) string {
	if len(others) == 0 {
		return ""
	}
	byOwner := map[string]int{}
	var order []string
	for _, it := range others {
		owner := groupComponent(it.Group)
		if _, seen := byOwner[owner]; !seen {
			order = append(order, owner)
		}
		byOwner[owner]++
	}
	sort.Strings(order)
	parts := make([]string, 0, len(order))
	for _, owner := range order {
		parts = append(parts, itoa(byOwner[owner])+" for the "+owner)
	}
	return strings.Join(parts, ", ")
}

// FormatChecklist renders assertions as acceptance criteria for a prompt.
func FormatChecklist(items []ChecklistItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("The implementation MUST satisfy every one of these, taken from " +
		"the Verification Checklists of the blueprints being implemented:\n")
	bySource := map[string][]string{}
	var order []string
	for _, it := range items {
		if _, seen := bySource[it.Source]; !seen {
			order = append(order, it.Source)
		}
		bySource[it.Source] = append(bySource[it.Source], it.Text)
	}
	for _, src := range order {
		b.WriteString("\nFrom " + src + ":\n")
		for _, t := range bySource[src] {
			b.WriteString("  - " + t + "\n")
		}
	}
	return b.String()
}

// ChecklistResult is the outcome of evaluating one assertion.
type ChecklistResult struct {
	Item    ChecklistItem
	Outcome Outcome
	Detail  string
	// Check names the mechanism that reached the outcome, so a reader can tell
	// what was actually established.
	Check string
	// Files are the generated files the failure is attributable to, for repair.
	Files []string
}

// Checked reports whether anything mechanical settled the assertion.
//
// `necessary` is deliberately NOT checked: a necessary condition holding is not
// the assertion holding, and the whole point of the four outcomes is that those
// two are never added together.
func (r ChecklistResult) Checked() bool {
	return r.Outcome == OutcomeVerified || r.Outcome == OutcomeFailed
}

// Passed reports whether the assertion is established.
func (r ChecklistResult) Passed() bool { return r.Outcome == OutcomeVerified }

// mechanicalCheck maps a recognisable phrase to a test over the generated
// source. Deliberately small and literal: a check that guesses at an assertion's
// meaning would report confident nonsense, and this whole file exists because a
// confident wrong answer is worse than an honest gap.
type mechanicalCheck struct {
	// match is looked for in the lower-cased assertion text.
	match string
	// test receives the concatenated source of every generated file.
	test func(src string) (bool, string)
}

func contains(sub string) func(string) (bool, string) {
	return func(src string) (bool, string) {
		if strings.Contains(src, sub) {
			return true, ""
		}
		return false, sub + " does not appear in the generated source"
	}
}

// mechanicalChecks are text checks retained for assertions whose whole content
// IS the presence of a construct. Every one is one-way for the same reason: a
// string appearing in the source may be appearing in a comment.
var mechanicalChecks = []mechanicalCheck{
	{"io.limitreader", contains("io.LimitReader")},
	{"sync.rwmutex", contains("sync.RWMutex")},
	{"retry-after", contains("Retry-After")},
	{"wl_dev", contains("WL_DEV")},
	{"user_version", contains("user_version")},
	{"wal journal", contains("WAL")},
	{"sync.waitgroup", contains("sync.WaitGroup")},
	{"create table if not exists", contains("CREATE TABLE IF NOT EXISTS")},
	{"do not panic", func(src string) (bool, string) {
		if strings.Contains(src, "panic(") {
			return false, "panic( appears in the generated source"
		}
		return true, ""
	}},
	{"package main", func(src string) (bool, string) {
		if strings.Contains(src, "package main") {
			return true, ""
		}
		return false, "no file declares package main"
	}},
}

// EvaluateChecklist evaluates every assertion it can and reports the rest as
// unchecked.
//
// Structural checks are tried first — they read the parsed source and answer
// exactly. The text table is the fallback, for assertions whose entire content
// is the presence of a construct.
func EvaluateChecklist(items []ChecklistItem, files []GeneratedFile) []ChecklistResult {
	return EvaluateChecklistAgainst(items, files, nil)
}

// EvaluateChecklistAgainst evaluates the checklist with the blueprints available,
// so a check can compare the artifact against the specification's own tables.
func EvaluateChecklistAgainst(items []ChecklistItem, files []GeneratedFile, spec map[string]string) []ChecklistResult {
	ctx := BuildCheckContextWith(files, spec)

	results := make([]ChecklistResult, 0, len(items))
	for _, item := range items {
		results = append(results, evaluateOne(item, ctx))
	}
	return results
}

func evaluateOne(item ChecklistItem, ctx *CheckContext) ChecklistResult {
	r := ChecklistResult{Item: item, Outcome: OutcomeUnchecked}

	// A conditional assertion binds only when its premise holds. Evaluated
	// unconditionally, "IF SQLite was chosen: WAL journal mode, `user_version`
	// pragma…" fails every implementation that took the JSONL default — a failure
	// for making the choice the blueprint recommends.
	//
	// An unrecognised premise leaves the assertion unchecked with the premise
	// quoted, never excused: guessing a premise false is how a checking layer
	// starts silently forgiving requirements.
	text := item.Text
	if premise, obligation, isConditional := splitConditional(item.Text); isConditional {
		holds, settled := evaluatePremise(premise, ctx)
		switch {
		case !settled:
			r.Check = "conditional premise not settled"
			r.Detail = "conditional on: " + premise
			return r
		case !holds:
			r.Outcome = OutcomeNotApplicable
			r.Check = "conditional premise refuted"
			r.Detail = premise + " — not the case in this implementation"
			return r
		}
		// The premise holds; the obligation is what must be satisfied.
		text = obligation
	}

	scoped := ChecklistItem{Source: item.Source, Text: text, Group: item.Group}
	a := parseAssertion(scoped, ctx)

	for _, sc := range structuralChecks {
		if !sc.applies(a) {
			continue
		}
		ok, detail, blame := sc.test(a, ctx)
		r.Check = sc.name
		switch {
		case !ok:
			r.Outcome, r.Detail = OutcomeFailed, detail
			r.Files = blame
			if len(r.Files) == 0 {
				r.Files = attributeFailure(a, ctx)
			}
		case sc.oneWay:
			r.Outcome = OutcomeNecessary
		default:
			r.Outcome = OutcomeVerified
		}
		return r
	}

	lower := strings.ToLower(text)
	for _, mc := range mechanicalChecks {
		if !strings.Contains(lower, mc.match) {
			continue
		}
		ok, detail := mc.test(ctx.Source)
		r.Check = "text: " + mc.match
		if ok {
			// One-way: a construct appearing in the source may be appearing in a
			// comment, so its presence does not establish the assertion.
			r.Outcome = OutcomeNecessary
		} else {
			r.Outcome, r.Detail = OutcomeFailed, detail
			r.Files = attributeFailure(a, ctx)
		}
		return r
	}
	return r
}

// attributeFailure names the files a failing assertion is about, so a repair
// round asks the file that owns the fault rather than every file.
//
// Empty when nothing can be attributed: repairing the wrong file is worse than
// reporting a failure without a target, and the caller can fall back to the
// whole set.
func attributeFailure(a assertion, ctx *CheckContext) []string {
	var out []string
	add := func(f string) {
		if f != "" && !containsString(out, f) {
			out = append(out, f)
		}
	}
	for _, t := range a.Types {
		add(ctx.OwnerOf[t])
	}
	for _, r := range a.Routes {
		add(ctx.Routes[r])
		if i := strings.Index(r, " "); i >= 0 {
			add(ctx.Routes[r[i+1:]])
		}
	}
	sort.Strings(out)
	return out
}

// ChecklistSummary counts outcomes.
//
// Four counts, never three. `necessary` is not folded into `passed` — that fold
// is exactly the confident wrong answer this whole mechanism exists to avoid —
// and it is not folded into `unchecked` either, because a necessary condition
// holding is a real result worth reporting.
func ChecklistSummary(results []ChecklistResult) (verified, failed, necessary, unchecked int) {
	v, f, n, na, u := ChecklistCounts(results)
	_ = na
	return v, f, n, u
}

// ChecklistCounts counts every outcome, including not-applicable.
//
// `not-applicable` is separate from `unchecked` because they call for different
// things: an unchecked assertion needs a human to look at it, and an inapplicable
// one needs nobody. Folding them inflates "needs review by hand" with work that
// does not exist.
func ChecklistCounts(results []ChecklistResult) (verified, failed, necessary, notApplicable, unchecked int) {
	for _, r := range results {
		switch r.Outcome {
		case OutcomeVerified:
			verified++
		case OutcomeFailed:
			failed++
		case OutcomeNecessary:
			necessary++
		case OutcomeNotApplicable:
			notApplicable++
		default:
			unchecked++
		}
	}
	return
}

// FailedChecklist returns the assertions a check refuted, which are the only
// ones that drive a repair round.
func FailedChecklist(results []ChecklistResult) []ChecklistResult {
	var out []ChecklistResult
	for _, r := range results {
		if r.Outcome == OutcomeFailed {
			out = append(out, r)
		}
	}
	return out
}
