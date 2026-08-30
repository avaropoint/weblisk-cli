package dispatch

// Validating a generation manifest against schemas/platform.md.
//
// The schema states seven rules. Until this file existed, nothing checked any of
// them — they were prose in a document, which is the same shape of fault as a
// permission flag that is stored and never read. A rule nobody enforces reads as
// a guarantee and is not one.
//
// Rule 4 is the one that needs the protocol blueprint in hand: the union of
// must_serve across a target's files must cover every endpoint the protocol
// defines for that target. Without it a manifest can look complete, generate
// cleanly, build, and produce a hub missing an endpoint — which surfaces as a
// conformance failure with no obvious cause.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ManifestIssue is one validation failure.
type ManifestIssue struct {
	Rule   int
	Target string
	Detail string
}

func (i ManifestIssue) String() string {
	if i.Target != "" {
		return fmt.Sprintf("rule %d [%s]: %s", i.Rule, i.Target, i.Detail)
	}
	return fmt.Sprintf("rule %d: %s", i.Rule, i.Detail)
}

// reOrchestratorEndpoint finds orchestrator endpoint headings in protocol/spec.md,
// e.g. "### POST /v1/register".
var reOrchestratorEndpoint = regexp.MustCompile(`(?m)^###\s+((?:GET|POST|PUT|DELETE|PATCH)\s+/v1/[^\s]+)\s*$`)

// OrchestratorEndpoints reads the endpoints the protocol defines for an
// orchestrator, from the "## Orchestrator Endpoints" section of protocol/spec.md.
//
// Derived from the spec rather than hard-coded here: a list maintained in two
// places drifts, and the copy in the checker is the one nobody notices is stale.
func OrchestratorEndpoints(protocolSpec string) []string {
	start := strings.Index(protocolSpec, "## Orchestrator Endpoints")
	if start < 0 {
		return nil
	}
	rest := protocolSpec[start+len("## Orchestrator Endpoints"):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range reOrchestratorEndpoint.FindAllStringSubmatch(rest, -1) {
		ep := strings.Join(strings.Fields(m[1]), " ")
		if !seen[ep] {
			seen[ep] = true
			out = append(out, ep)
		}
	}
	sort.Strings(out)
	return out
}

// ValidateManifest checks a manifest against schemas/platform.md.
//
// protocolSpec may be empty, in which case rule 4 is skipped and reported as
// unchecked rather than passed — an unrun check is not a pass.
func ValidateManifest(m *GenerationManifest, protocolSpec string) (issues []ManifestIssue, checkedRule4 bool) {
	if m == nil {
		return nil, false
	}
	names := make([]string, 0, len(m.Targets))
	for n := range m.Targets {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		t := m.Targets[name]
		seen := map[string]bool{}
		for _, f := range t.Files {
			// Rule 1 — relative, contained.
			if _, err := safeGeneratedPath("/manifest-check", f.Path); err != nil {
				issues = append(issues, ManifestIssue{1, name, fmt.Sprintf("%q is not a safe relative path", f.Path)})
			}
			// Rule 2 — purpose present.
			if strings.TrimSpace(f.Purpose) == "" {
				issues = append(issues, ManifestIssue{2, name, fmt.Sprintf("%q has no purpose", f.Path)})
			}
			if seen[f.Path] {
				issues = append(issues, ManifestIssue{1, name, fmt.Sprintf("%q is listed twice", f.Path)})
			}
			seen[f.Path] = true
		}
		// Rule 5 — a build command that can fail.
		if strings.TrimSpace(t.Build) == "" {
			issues = append(issues, ManifestIssue{5, name, "no build command"})
		}
		// Rule 6 — at least L1.
		hasL1 := false
		for _, c := range t.Conformance {
			if strings.EqualFold(c, "L1") {
				hasL1 = true
			}
		}
		if !hasL1 {
			issues = append(issues, ManifestIssue{6, name, fmt.Sprintf("conformance %v does not include L1", t.Conformance)})
		}
	}

	if strings.TrimSpace(protocolSpec) == "" {
		return issues, false
	}
	defined := OrchestratorEndpoints(protocolSpec)
	if len(defined) == 0 {
		return issues, false
	}
	definedSet := map[string]bool{}
	for _, e := range defined {
		definedSet[e] = true
	}

	if t, ok := m.Targets["orchestrator"]; ok {
		served := map[string]bool{}
		for _, f := range t.Files {
			for _, e := range f.MustServe {
				ep := strings.Join(strings.Fields(e), " ")
				served[ep] = true
				// Rule 3 — a manifest may not invent an endpoint.
				if !definedSet[ep] {
					issues = append(issues, ManifestIssue{3, "orchestrator",
						fmt.Sprintf("%q declares %s, which protocol/spec.md does not define", f.Path, ep)})
				}
			}
		}
		// Rule 4 — full coverage.
		var missing []string
		for _, e := range defined {
			if !served[e] {
				missing = append(missing, e)
			}
		}
		if len(missing) > 0 {
			issues = append(issues, ManifestIssue{4, "orchestrator",
				"no file declares: " + strings.Join(missing, ", ")})
		}
	}
	return issues, true
}
