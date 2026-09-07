package dispatch

// validate_yaml_blocks.go — a machine-read block that does not parse.
//
// `weblisk validate` reported "Validation passed" on a blueprint whose
// `contracts:` block could not be parsed at all. Not because the check was
// lenient — because there was no check. Every rule downstream reads the parsed
// bindings, an unparseable block yields none, and no bindings breaks no rule.
// The blueprint declaring every behaviour a component must satisfy had, as far
// as the validator was concerned, declared nothing, and it said so by staying
// quiet.
//
// Found when a checklist rule was written beginning with a backtick. A backtick
// is a reserved indicator in YAML and cannot start a plain scalar, so one
// character silently removed an entire contract from the corpus.
//
// Three block kinds are machine-read: `requires:` (the dependency contract),
// `types:` (type definitions) and `contracts:` (behaviours and bindings). A
// failure in any of them is a FAULT — the corpus says something the pipeline
// cannot read, which is indistinguishable downstream from not having said it.
//
// Illustrative blocks are left alone. A malformed example in a policy sample is
// worth fixing and is not worth refusing a build over, and hardening those is
// the ratchet in yamlparse_test.go.

import (
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// reYAMLBlockFence captures a fenced yaml block's body. Its own pattern:
// validate_corpus.go's reYAMLFence matches only the opening line, because it
// answers "is there a yaml block here" and this needs the contents.
var reYAMLBlockFence = regexp.MustCompile("(?s)```yaml\\n(.*?)```")

// reMachineReadBlock names the blocks the pipeline consumes.
//
// Kept in step with yamlparse_test.go's copy deliberately rather than shared:
// that one is a guard over the corpus this repository ships, this one runs on
// whatever corpus a person points the CLI at, and they answer to different
// callers. The test below asserts they agree.
var reMachineReadBlock = regexp.MustCompile(`^\s*(requires|types|contracts)\s*:`)

func validateMachineReadBlocksParse(corpus map[string]string) []Finding {
	var out []Finding
	for _, name := range sortedKeys(corpus) {
		for _, m := range reYAMLBlockFence.FindAllStringSubmatch(corpus[name], -1) {
			block := m[1]
			if !reMachineReadBlock.MatchString(block) {
				continue
			}
			var v any
			err := yaml.Unmarshal([]byte(block), &v)
			if err == nil {
				continue
			}
			kind := strings.TrimSpace(strings.SplitN(strings.TrimSpace(block), ":", 2)[0])
			out = append(out, Finding{
				Blueprint: name,
				Rule:      "a block the pipeline reads must parse",
				Detail: "the `" + kind + ":` block does not parse as YAML, so everything it declares is " +
					"absent as far as generation is concerned — and absent declares nothing, which breaks no other rule: " +
					yamlErrorFirstLine(err.Error()),
				Authority: "architecture/generation.md",
				Severity:  SeverityFault,
			})
		}
	}
	return out
}

// yamlErrorFirstLine keeps the reason and drops yaml.v3's context lines, which
// quote the block back at a reader who is already looking at it.
func yamlErrorFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
