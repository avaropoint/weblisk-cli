package dispatch

import (
	"strings"
	"testing"
)

// knownFieldBindingFaults are bindings that name a field their type does not
// define, recorded so the corpus can be ratcheted rather than frozen.
//
// Every entry is a real fault: generation is told to implement a field the
// protocol has no name for, and the model complies. They are pinned rather than
// fixed in bulk because the fix is a design decision per type — TaskRequest
// carries `from` and no `target_agent`, so twenty blueprints either mean `to`,
// or the type is missing a field, and guessing at scale would encode the wrong
// answer twenty times.
//
// Three classes were fixed rather than pinned, because each had already caused
// a failure in generated code:
//
//	ErrorResponse    message         → error            (25 blueprints)
//	HealthStatus     component       → name             (7 blueprints)
//	HealthStatus     uptime_seconds  → uptime
//	HealthStatus     details         → checks           (6 blueprints)
//	ServiceDirectory agents          → services         (5 blueprints)
// The corpus-wide pin list is gone.
//
// It held a hundred entries produced by a checker that was wrong three times —
// it could not read a YAML list field, a heading with a suffix, or a
// three-column table, and each blind spot manufactured accusations against
// correct blueprints. Reported counts went 89 → 111 → 100 as the regexes
// changed, which measured the tooling and not the blueprints.
//
// A list of guesses that looks like knowledge is worse than no list: it becomes
// a work queue, and work gets done against it. What remains asserted below is
// only what was PROVEN — by reading an unambiguous type table, and in the
// HealthStatus case by a failing L1-01 on generated code.
//
// The durable fix is not a better checker. It is one machine-readable form for
// a type definition, parsed by a real parser. See the plan in
// architecture/generation.

// A pinned fault that has been fixed must leave the list, or the list stops
// describing the corpus and starts hiding it.

// The three fixed classes must stay fixed.
func TestTheFieldNamesThatBrokeGeneratedCodeAreCorrect(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol", "patterns", "agents", "platforms", "schemas"})
	types := TypeFields(bps)

	for _, c := range []struct{ typ, wrong, right string }{
		{"ErrorResponse", "message", "error"},
		{"HealthStatus", "component", "name"},
		{"HealthStatus", "uptime_seconds", "uptime"},
		{"ServiceDirectory", "agents", "services"},
	} {
		if !types[c.typ][c.right] {
			t.Errorf("%s should define %q and does not", c.typ, c.right)
		}
		if types[c.typ][c.wrong] {
			t.Errorf("%s defines %q — the wrong name was added to the type instead of "+
				"correcting the bindings", c.typ, c.wrong)
		}
	}
	for _, f := range CheckFieldBindings(bps, nil) {
		for _, bad := range []string{"message", "uptime_seconds"} {
			if (f.Type == "ErrorResponse" || f.Type == "HealthStatus") &&
				strings.Contains(strings.Join(f.Fields, ","), bad) {
				t.Errorf("%s still binds %q from %s", f.Blueprint, bad, f.Type)
			}
		}
	}
}

// A three-column table declares no JSON key, so the field name is converted.
// Acronyms are one word: ChannelID is channel_id, not channel_i_d.
func TestSnakeCaseHandlesAcronyms(t *testing.T) {
	for in, want := range map[string]string{
		"LastSeen":  "last_seen",
		"ChannelID": "channel_id",
		"TargetURL": "target_url",
		"AgentID":   "agent_id",
		"Manifest":  "manifest",
		"URLPrefix": "url_prefix",
	} {
		if got := snakeCase(in); got != want {
			t.Errorf("snakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}

// The checker must not accuse a consumer of a type whose definition it simply
// cannot read. Three shapes manufactured false accusations before being handled:
// a YAML list field, a heading with a suffix, and a three-column table.
func TestTheExtractorReadsEveryShapeTheCorpusUses(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol", "patterns", "agents", "platforms", "schemas"})
	types := TypeFields(bps)
	for _, c := range []struct{ typ, field, shape string }{
		{"ScopeDeclaration", "context", "YAML list: - name: context"},
		{"ContentIdentity", "digest", "YAML map: digest:"},
		{"AgentEntry", "last_seen", "3-column table under a suffixed heading"},
		{"ChannelGrant", "channel_token", "5-column table with a JSON key"},
	} {
		if !types[c.typ][c.field] {
			t.Errorf("%s.%s not read — %s", c.typ, c.field, c.shape)
		}
	}
}
