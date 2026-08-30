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
	"strings"
)

// ChecklistItem is one assertion from a blueprint.
type ChecklistItem struct {
	Source string // blueprint it came from
	Text   string
}

var reChecklistItem = regexp.MustCompile(`(?m)^-\s*\[\s*\]\s*(.+?)\s*$`)

// ExtractChecklist reads the Verification Checklist of one blueprint.
func ExtractChecklist(source, blueprint string) []ChecklistItem {
	i := strings.Index(blueprint, "## Verification Checklist")
	if i < 0 {
		return nil
	}
	rest := blueprint[i+len("## Verification Checklist"):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	var out []ChecklistItem
	for _, m := range reChecklistItem.FindAllStringSubmatch(rest, -1) {
		if t := strings.TrimSpace(m[1]); t != "" {
			out = append(out, ChecklistItem{Source: source, Text: t})
		}
	}
	return out
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
	Checked bool // false means no mechanical check exists — NOT a pass
	Passed  bool
	Detail  string
}

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

// EvaluateChecklist runs every mechanical check it has against the generated
// source, and reports the rest as unchecked.
func EvaluateChecklist(items []ChecklistItem, files []GeneratedFile) []ChecklistResult {
	var all strings.Builder
	for _, f := range files {
		all.WriteString(f.Content)
		all.WriteString("\n")
	}
	src := all.String()

	results := make([]ChecklistResult, 0, len(items))
	for _, item := range items {
		lower := strings.ToLower(item.Text)
		r := ChecklistResult{Item: item}
		for _, mc := range mechanicalChecks {
			if strings.Contains(lower, mc.match) {
				ok, detail := mc.test(src)
				r.Checked, r.Passed, r.Detail = true, ok, detail
				break
			}
		}
		results = append(results, r)
	}
	return results
}

// ChecklistSummary counts outcomes. `unchecked` is reported separately and
// deliberately: folding it into passed is the failure this file prevents.
func ChecklistSummary(results []ChecklistResult) (passed, failed, unchecked int) {
	for _, r := range results {
		switch {
		case !r.Checked:
			unchecked++
		case r.Passed:
			passed++
		default:
			failed++
		}
	}
	return
}
