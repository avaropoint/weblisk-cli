package dispatch

// Verification against the blueprints' own words.
//
// # Why the checker stopped driving the loop
//
// Layer 3 was a set of structural checks written in Go: parse the source, read
// the routes, compare the JSON tags, check the module graph. They worked, and they
// found a real conformance bug no compiler could see. They also produced ten
// false failures in a single session, every one of them a re-reading of an
// assertion that was subtly not what the assertion said:
//
//   - "HTTP handlers do not panic" became a grep for panic( over all source
//   - "OperationIntent requires `id`, …; operation includes `list` and `query`"
//     became a demand for fields named list and query
//   - "IF SQLite was chosen: …" became an unconditional requirement
//
// Each was found by running against real generated code, never by reasoning about
// the check. That is the tell: a check is a second opinion about what a
// specification means, and a second opinion is a thing that can be wrong on its
// own.
//
// So the authority moves back to the blueprint. The assertions are sent VERBATIM
// and the model reads them against its own output, which is the same act a human
// reviewer performs and requires no transcription of the requirement into Go.
//
// # What keeps this from being self-congratulation
//
// A model asked "did you satisfy this?" will tend to say yes. Three things push
// against it:
//
//  1. Every verdict must cite EVIDENCE — a file and the construct that satisfies
//     or violates the assertion. Naming a line is harder to fake than agreeing.
//  2. `unverifiable` is a first-class answer. An assertion about behaviour across
//     a restart cannot be settled by reading source, and saying so is correct.
//     Removing that option is what makes a reviewer guess.
//  3. The structural checks still run. They no longer drive repair, but they are
//     reported beside the model's verdicts, so a disagreement between the two is
//     visible rather than resolved silently in favour of either.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Verdict is the model's reading of one assertion against the generated source.
type Verdict struct {
	Index    int    `json:"index"`
	Holds    string `json:"holds"` // "yes" | "no" | "unverifiable"
	File     string `json:"file"`
	Evidence string `json:"evidence"`
}

// Violation is an assertion the model reports as unmet.
type Violation struct {
	Item     ChecklistItem
	File     string
	Evidence string
}

const verifySystemPrompt = `You verify an implementation against acceptance criteria.

You are given numbered assertions taken verbatim from the specification the
implementation was built from, and the complete source of that implementation.

For each assertion, decide whether the source satisfies it.

Output rules, which are absolute:
- Output ONLY a single JSON array. No prose, no explanation, no code fences.
- The first character of your response is [.

Shape:
[
  {"index": 1, "holds": "yes", "file": "identity.go", "evidence": "<the construct that satisfies it>"},
  {"index": 2, "holds": "no", "file": "orchestrator.go", "evidence": "<what is wrong, specifically>"},
  {"index": 3, "holds": "unverifiable", "file": "", "evidence": "<why reading source cannot settle it>"}
]

Rules on your verdicts:
- One entry per assertion. Every index appears exactly once.
- "yes" REQUIRES evidence naming the construct that satisfies the assertion. If
  you cannot name it, the answer is not "yes".
- "no" REQUIRES the file the fault is in and what specifically is wrong.
- "unverifiable" is the correct answer when reading source cannot settle the
  assertion — behaviour over time, across a restart, under concurrency, or under
  load. Use it rather than guessing. An assertion you cannot check is not one
  that passes.
- Judge the assertion as written. Do not substitute a stricter or looser reading.`

// SelfVerify asks the model to check its output against the assertions verbatim.
func SelfVerify(provider Provider, files []GeneratedFile, checklist []ChecklistItem) ([]Verdict, error) {
	if len(checklist) == 0 || len(files) == 0 {
		return nil, nil
	}
	var b strings.Builder
	b.WriteString("--- ASSERTIONS ---\n\n")
	for i, item := range checklist {
		fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, item.Source, item.Text)
	}
	b.WriteString("\n--- IMPLEMENTATION ---\n")
	for _, f := range files {
		fmt.Fprintf(&b, "\n=== %s ===\n%s\n", f.Path, f.Content)
	}

	raw, err := provider.Chat([]Message{
		{Role: "system", Content: verifySystemPrompt},
		{Role: "user", Content: b.String()},
	})
	if err != nil {
		return nil, err
	}
	return ParseVerdicts(raw, len(checklist))
}

var reJSONArray = regexp.MustCompile(`(?s)\[.*\]`)

// ParseVerdicts reads the verdict array, discarding anything malformed.
//
// A verdict for an assertion that does not exist, or a "yes" with no evidence, is
// dropped rather than trusted: the point of requiring evidence is lost if a
// verdict without it still counts.
func ParseVerdicts(raw string, n int) ([]Verdict, error) {
	m := reJSONArray.FindString(strings.TrimSpace(raw))
	if m == "" {
		return nil, fmt.Errorf("no JSON array in the verification response")
	}
	var all []Verdict
	if err := json.Unmarshal([]byte(m), &all); err != nil {
		return nil, fmt.Errorf("verification response is not valid JSON: %w", err)
	}
	seen := map[int]bool{}
	var out []Verdict
	for _, v := range all {
		if v.Index < 1 || v.Index > n || seen[v.Index] {
			continue
		}
		v.Holds = strings.ToLower(strings.TrimSpace(v.Holds))
		switch v.Holds {
		case "yes":
			if strings.TrimSpace(v.Evidence) == "" {
				// A "yes" without evidence is an opinion, not a verdict.
				v.Holds = "unverifiable"
				v.Evidence = "reported as satisfied without naming the construct that satisfies it"
			}
		case "no":
			if strings.TrimSpace(v.Evidence) == "" {
				// A "no" with no fault named cannot be repaired against.
				v.Holds = "unverifiable"
				v.Evidence = "reported as unmet without naming the fault"
			}
		case "unverifiable":
		default:
			continue
		}
		seen[v.Index] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no usable verdicts in the verification response")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, nil
}

// Violations returns the assertions reported unmet, paired with their text.
func Violations(verdicts []Verdict, checklist []ChecklistItem) []Violation {
	var out []Violation
	for _, v := range verdicts {
		if v.Holds != "no" || v.Index < 1 || v.Index > len(checklist) {
			continue
		}
		out = append(out, Violation{Item: checklist[v.Index-1], File: v.File, Evidence: v.Evidence})
	}
	return out
}

// VerdictSummary counts the model's verdicts, and reports how many assertions it
// declined to answer at all.
//
// Unanswered is not folded into unverifiable. A model that returns 40 verdicts for
// 129 assertions has not judged 89 of them, and that is a different fact from
// judging them unverifiable — one is a gap in the verification, the other is a
// finding about the assertions.
func VerdictSummary(verdicts []Verdict, total int) (yes, no, unverifiable, unanswered int) {
	for _, v := range verdicts {
		switch v.Holds {
		case "yes":
			yes++
		case "no":
			no++
		default:
			unverifiable++
		}
	}
	unanswered = total - len(verdicts)
	if unanswered < 0 {
		unanswered = 0
	}
	return
}
