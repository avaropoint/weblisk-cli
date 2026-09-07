package dispatch

import (
	"strings"
	"testing"
)

// A contract block that does not parse must be a FAULT, not silence.
//
// The corpus reported "Validation passed" on exactly this. Every rule
// downstream reads parsed bindings; an unparseable block yields none; and no
// bindings breaks no rule. Absence and correctness were indistinguishable.
func TestUnparseableMachineReadBlockIsAFault(t *testing.T) {
	for _, kind := range []string{"contracts", "requires", "types"} {
		t.Run(kind, func(t *testing.T) {
			// A backtick cannot start a YAML plain scalar. This is the exact
			// character that removed a contract from the corpus.
			corpus := map[string]string{
				"architecture/x.md": "# X\n\n```yaml\n" + kind + ":\n  behaviors:\n    - `POST /v1/x` MUST exist\n```\n",
			}
			findings := validateMachineReadBlocksParse(corpus)
			if len(findings) != 1 {
				t.Fatalf("got %d findings, want 1", len(findings))
			}
			if findings[0].Severity != SeverityFault {
				t.Errorf("severity is %q, want fault — an unreadable declaration is not a warning", findings[0].Severity)
			}
			if !strings.Contains(findings[0].Detail, kind) {
				t.Errorf("the detail does not name which block: %s", findings[0].Detail)
			}
		})
	}
}

// An illustrative block is left alone. A malformed policy sample is worth
// fixing and is not worth refusing a build over; hardening those is the ratchet
// in yamlparse_test.go, and treating them as faults here would make this check
// something people turn off.
func TestIllustrativeBlocksAreNotFaults(t *testing.T) {
	corpus := map[string]string{
		"patterns/x.md": "# X\n\n```yaml\n1. Do a thing\n2. Then: another\n```\n",
	}
	if f := validateMachineReadBlocksParse(corpus); len(f) != 0 {
		t.Fatalf("an illustrative block produced %d finding(s): %v", len(f), f)
	}
}

// A block that parses produces nothing, including the shapes real contract
// blocks use — backticks mid-scalar, em dashes, colons inside quoted values.
func TestWellFormedBlocksProduceNothing(t *testing.T) {
	corpus := map[string]string{
		"architecture/x.md": "# X\n\n```yaml\ncontracts:\n  behaviors:\n    - name: a-rule\n" +
			"      rules:\n        - The route `POST /v1/x` MUST exist — because it must\n" +
			"        - It MUST require the `admin` role\n```\n",
	}
	if f := validateMachineReadBlocksParse(corpus); len(f) != 0 {
		t.Fatalf("a well-formed contract block produced %d finding(s): %v", len(f), f)
	}
}

// The check must be reachable from the command a person actually runs. A rule
// wired into nothing is the fault this repository has shipped before.
func TestValidateCorpusRunsTheBlockCheck(t *testing.T) {
	corpus := map[string]string{
		"architecture/x.md": "# X\n\n```yaml\ncontracts:\n  behaviors:\n    - `bad` scalar\n```\n",
	}
	var found bool
	for _, f := range ValidateCorpus(corpus) {
		if strings.Contains(f.Rule, "must parse") {
			found = true
		}
	}
	if !found {
		t.Fatal("ValidateCorpus does not run the block-parse check — it is wired into nothing")
	}
}

// The two copies of the machine-read pattern must agree. They are separate
// because one guards this repository's corpus and one runs on whatever corpus a
// person points the CLI at, but a block kind added to one and not the other is
// a hole that looks closed.
func TestTheTwoMachineReadPatternsAgree(t *testing.T) {
	for _, kind := range []string{"requires", "types", "contracts"} {
		block := kind + ":\n  x: 1\n"
		if !reMachineReadBlock.MatchString(block) {
			t.Errorf("the validator does not treat %q as machine-read", kind)
		}
		if !reMachineRead.MatchString(block) {
			t.Errorf("the guard does not treat %q as machine-read", kind)
		}
	}
	// And neither may claim an illustrative block.
	if reMachineReadBlock.MatchString("1. a step\n") || reMachineRead.MatchString("1. a step\n") {
		t.Error("a narrative block is being treated as machine-read")
	}
}
