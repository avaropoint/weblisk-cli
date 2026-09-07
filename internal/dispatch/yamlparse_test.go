package dispatch

import (
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var reFenceForTest = regexp.MustCompile("(?s)```yaml\\n(.*?)```")
var reMachineRead = regexp.MustCompile(`^\s*(requires|types|contracts)\s*:`)

// Every block generation READS must parse. This is the hard line.
//
// A `requires:` block is a dependency contract and a `types:` block is a type
// definition; both are consumed by the pipeline, so one that does not parse is
// a contract nobody read. Measured at 247 blocks, and it must stay at zero
// failures.
//
// `contracts:` was added 2026-09-06 and is the most consequential of the three.
// A contract block declares every behaviour and binding a component must
// satisfy, so one that does not parse means the whole declaration is silently
// absent — and `weblisk validate` reported "Validation passed" on exactly that,
// because an unparseable block yields no bindings and no bindings breaks no
// rule. Caught when a rule beginning with a backtick — which cannot start a
// YAML plain scalar — broke architecture/admin's block, and only the ratchet
// below noticed. The corpus had zero such blocks at the time this line was
// added, so this is holding a property, not fixing a backlog.
//
// Found this way: `{ type: string[] }` inside a flow mapping. `[` opens a flow
// sequence, so fourteen values in architecture/change-management had never
// parsed — in the blueprint whose whole subject is assessing a change against
// declared bindings.
func TestEveryMachineReadYAMLBlockParses(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol", "patterns", "agents", "platforms", "schemas", "standards"})
	checked := 0
	for path, body := range bps {
		for _, m := range reFenceForTest.FindAllStringSubmatch(body, -1) {
			block := m[1]
			if !reMachineRead.MatchString(block) {
				continue
			}
			checked++
			var v any
			if err := yaml.Unmarshal([]byte(block), &v); err != nil {
				t.Errorf("%s: a block the pipeline reads does not parse: %v\n%s",
					path, err, firstNLines(block, 6))
			}
		}
	}
	if checked < 200 {
		t.Errorf("only %d machine-read blocks found — the matcher is not seeing the corpus", checked)
	}
}

// The illustrative blocks are a RATCHET, not a rule.
//
// A malformed example is a real defect — a model reading this corpus imitates
// it — but twenty of them are authoring errors in policy samples and test
// narratives, and a hard rule would be exempted rather than met. The count may
// fall and may never rise.
const illustrativeYAMLFailureCeiling = 23

func TestMalformedIllustrativeBlocksDoNotIncrease(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol", "patterns", "agents", "platforms", "schemas", "standards"})
	var failing []string
	for path, body := range bps {
		for _, m := range reFenceForTest.FindAllStringSubmatch(body, -1) {
			block := m[1]
			if reMachineRead.MatchString(block) {
				continue
			}
			var v any
			if err := yaml.Unmarshal([]byte(block), &v); err != nil {
				failing = append(failing, path)
			}
		}
	}
	if len(failing) > illustrativeYAMLFailureCeiling {
		t.Errorf("malformed illustrative yaml rose to %d (ceiling %d): %v",
			len(failing), illustrativeYAMLFailureCeiling, failing)
	}
	if len(failing) < illustrativeYAMLFailureCeiling {
		t.Errorf("malformed illustrative yaml fell to %d — lower the ceiling from %d to hold the gain",
			len(failing), illustrativeYAMLFailureCeiling)
	}
}

func firstNLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return "  " + strings.Join(lines, "\n  ")
}
