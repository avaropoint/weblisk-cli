package dispatch

// No structural check may reach a verdict from an empty artifact.
//
// The class: a check searches an index, finds nothing wrong, and reports that
// as proof. "No quantum-vulnerable algorithm is imported" from an artifact
// whose imports were never parsed is not a pass — it is a failure to read.
// The panic check was fixed for this on 2026-09-09; this pins the sweep it
// asked for, and any check added later.

import (
	"strings"
	"testing"
)

// An assertion each check applies to, so every check is actually reached.
var probeAssertions = map[string]string{
	"enum values are declared":                               "ScopeLevel enum is constrained to `public`, `internal`, `private`",
	"declared JSON keys exist on the named type":             "ErrorResponse includes `error`, `code` and `detail` fields with exact JSON keys",
	"referenced endpoint is routed":                          "POST /v1/register enforces exclusive namespace ownership",
	"error codes are centrally registered":                   "Every error code is registered centrally",
	"registered codes carry the status the protocol assigns": "Each error code maps to the correct HTTP status",
	"dependency policy":                                      "No dependency beyond `github.com/cloudflare/circl`",
	"forbidden algorithms are absent":                        "No quantum-vulnerable signing algorithm is used",
	"every routed path is version-prefixed":                  "Protocol paths are all prefixed with `/v1`",
	"binaries are package main and shared code is not":       "Each binary is `package main` under cmd/",
	"HTTP handlers do not panic":                             "HTTP handlers do not panic",
	"declared sizes appear as literals":                      "Public keys are 1952 bytes and signatures 3309 bytes",
}

func TestNoCheckReachesAVerdictFromAnEmptyArtifact(t *testing.T) {
	empty := &CheckContext{}
	for _, sc := range structuralChecks {
		text, ok := probeAssertions[sc.name]
		if !ok {
			t.Errorf("check %q has no probe assertion — add one, or it is never measured here", sc.name)
			continue
		}
		a := parseAssertion(ChecklistItem{Source: "probe.md", Text: text}, empty)
		if !sc.applies(a) {
			// Structurally unreachable on an empty artifact — the check's own
			// applies() reads the parsed source, so nothing parsed means nothing
			// to answer. That is the safe shape, not a gap.
			continue
		}

		if sc.reads != nil {
			if missing := sc.reads(a, empty); missing != "" {
				continue // declines to run on nothing, which is the point
			}
		}
		// It chose to run. Then it must not claim the assertion holds.
		holds, detail, _ := sc.test(a, empty)
		if holds {
			t.Errorf("check %q reports an empty artifact as satisfying %q — "+
				"a verdict reached by examining nothing", sc.name, text)
			continue
		}
		if _, unresolved := splitInconclusive(detail); !unresolved {
			t.Errorf("check %q refutes %q from an empty artifact (%q) — "+
				"\"I cannot read this\" is being reported as \"this is wrong\"",
				sc.name, text, detail)
		}
	}
}

// And the guard must be a real one: a populated context still gets checked.
func TestTheGuardDoesNotSuppressRealChecks(t *testing.T) {
	c := &CheckContext{
		Files:   []GeneratedFile{{Path: "cmd/x/main.go", Content: "package main\n"}},
		Source:  "package main\n",
		Imports: map[string]string{"crypto/ed25519": "cmd/x/main.go"},
	}
	sc := findCheck(t, "forbidden algorithms are absent")
	a := parseAssertion(ChecklistItem{Source: "p.md",
		Text: "No quantum-vulnerable signing algorithm is used"}, c)
	if sc.reads != nil {
		if missing := sc.reads(a, c); missing != "" {
			t.Fatalf("the guard declined a context that carries imports: %s", missing)
		}
	}
	holds, detail, _ := sc.test(a, c)
	if holds || !strings.Contains(detail, "ed25519") {
		t.Errorf("an imported ed25519 was not reported: holds=%v detail=%q", holds, detail)
	}
}
