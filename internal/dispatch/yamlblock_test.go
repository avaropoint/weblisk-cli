package dispatch

import (
	"strings"
	"testing"
)

const wrappedBlueprint = "## Dependencies\n\n" + "```yaml\n" + `requires:
  - blueprint: architecture/hub
    version: ">=1.0.0 <2.0.0"
    bindings:
      types:
        - name: MetricsInfo
          fields_used: [listing_id, uptime_30d,
                        total_invocations_30d, error_rate_30d]
      behaviors:
        - name: boundary-enforcement
          fields_used: [message, storage, external, response]
` + "```\n\n## Next\n"

// A fields_used sequence wrapped across lines is valid YAML and must parse
// whole. The line-wise reader required the closing bracket on the opening line
// and returned an EMPTY list, so the fields of MetricsInfo, SearchQuery and
// DataContract reached generation as nothing at all — silently, because an
// empty list is indistinguishable from a type bound as a whole.
func TestAWrappedFieldsUsedSequenceParsesWhole(t *testing.T) {
	bs, errs := ParseBindings("t.md", wrappedBlueprint)
	if len(errs) > 0 {
		t.Fatalf("valid YAML did not parse: %v", errs)
	}
	var metrics *Binding
	for i := range bs {
		if bs[i].Type == "MetricsInfo" {
			metrics = &bs[i]
		}
	}
	if metrics == nil {
		t.Fatal("MetricsInfo binding not read at all")
	}
	if len(metrics.FieldsUsed) != 4 {
		t.Errorf("read %d fields from a wrapped sequence, want 4: %v", len(metrics.FieldsUsed), metrics.FieldsUsed)
	}
	if !strings.Contains(strings.Join(metrics.FieldsUsed, ","), "error_rate_30d") {
		t.Errorf("the field after the line break was lost: %v", metrics.FieldsUsed)
	}
}

// A behaviours block following a types block is its own section. The line-wise
// reader did not recognise `behaviors:`, and an unrecognised key does not end
// the previous section — so behaviour names arrived as types that must exist.
func TestABehavioursBlockIsNotReadAsMoreTypes(t *testing.T) {
	bs, _ := ParseBindings("t.md", wrappedBlueprint)
	for _, b := range bs {
		if b.Type == "boundary-enforcement" && b.Kind != "behavior" {
			t.Errorf("boundary-enforcement was read as kind %q; it is a behaviour", b.Kind)
		}
	}
	if got := BoundTypes(bs); len(got) != 1 || got[0] != "MetricsInfo" {
		t.Errorf("BoundTypes = %v, want only MetricsInfo — a behaviour is inherited, not defined", got)
	}
}

// A bare * is YAML's alias indicator. Binding a whole type must be quoted.
func TestBindingAWholeTypeMustBeQuoted(t *testing.T) {
	bad := "## Dependencies\n\n```yaml\nrequires:\n  - blueprint: protocol/types\n    bindings:\n      types:\n        - name: AgentManifest\n          fields_used: [*]\n```\n"
	if _, errs := ParseBindings("t.md", bad); len(errs) == 0 {
		t.Error("a bare * parsed; it is a YAML alias and must be reported, not silently skipped")
	}
	good := strings.Replace(bad, "[*]", `["*"]`, 1)
	bs, errs := ParseBindings("t.md", good)
	if len(errs) > 0 {
		t.Fatalf(`["*"] did not parse: %v`, errs)
	}
	if len(bs) != 1 || len(bs[0].FieldsUsed) != 1 || bs[0].FieldsUsed[0] != "*" {
		t.Errorf(`["*"] read as %v`, bs)
	}
}

// Every fenced YAML block in the corpus must parse. One that does not is a
// contract reaching generation unread.
func TestEveryYAMLBlockInTheCorpusParses(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol", "patterns", "agents", "platforms", "schemas", "standards"})
	for path, body := range bps {
		if _, errs := ParseBindings(path, body); len(errs) > 0 {
			for _, e := range errs {
				t.Errorf("%v", e)
			}
		}
	}
}
