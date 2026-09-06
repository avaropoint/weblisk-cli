package dispatch

import (
	"sort"
	"strings"
	"testing"
)

func TestAuditBindings(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol", "patterns", "agents", "platforms"})
	declares := map[string]map[string]bool{}
	owner := map[string][]string{}
	for name, body := range bps {
		key := strings.TrimSuffix(name, ".md")
		set := map[string]bool{}
		for _, n := range declaredNames(body) {
			set[n] = true
			owner[n] = append(owner[n], key)
		}
		declares[key] = set
	}
	type miss struct{ from, name, where string }
	var elsewhere, nowhere []miss
	seen := map[string]bool{}
	for _, name := range sortedKeys(bps) {
		bindings, _ := ParseBindings(name, bps[name])
		for _, b := range bindings {
			if declares[b.From] == nil || declares[b.From][b.Type] {
				continue
			}
			k := b.From + "::" + b.Type
			if seen[k] {
				continue
			}
			seen[k] = true
			if o := owner[b.Type]; len(o) > 0 {
				sort.Strings(o)
				elsewhere = append(elsewhere, miss{b.From, b.Type, o[0]})
			} else {
				nowhere = append(nowhere, miss{b.From, b.Type, ""})
			}
		}
	}
	t.Logf("declared ELSEWHERE (repoint the binding): %d", len(elsewhere))
	for _, m := range elsewhere {
		t.Logf("   %-24s %s -> %s", m.name, m.from, m.where)
	}
	t.Logf("declared NOWHERE: %d", len(nowhere))
	for _, m := range nowhere {
		t.Logf("   %-24s bound from %s", m.name, m.from)
	}
}
