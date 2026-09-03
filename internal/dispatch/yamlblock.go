package dispatch

// Parsing the YAML in a blueprint with a YAML parser.
//
// # Why this did not exist
//
// The decision was recorded in bindings.go and its premise was correct:
//
//	Parsed line-wise rather than as YAML: the block is fenced markdown inside a
//	document, the surrounding text is not YAML, and a full parse would fail on
//	the file rather than on the block.
//
// The file is indeed not YAML. The inference does not follow: the answer is to
// extract the fenced block and parse THAT, not to stop parsing. The reasoning
// stopped one step short, and fourteen regexes grew in the gap.
//
// # What it cost
//
// `behaviors:` was not in the list of section names a regex recognised, and an
// unrecognised key does not end the previous section — so a behaviours block
// following a types block was read as MORE TYPES, and `content-digest` and
// `read-without-mutation` arrived at the planner as types that must exist. A
// parser cannot make that mistake: a key it has no field for is ignored, and it
// never leaks into the value of another key.
//
// The same shape appeared three more times in the type-definition reader: a
// YAML list field, a heading with a parenthesised suffix, and a three-column
// table each produced FALSE accusations against correct blueprints.
//
// 683 of the corpus's 713 fenced YAML blocks parse cleanly. The 30 that do not
// are a blueprint defect this surfaces rather than tolerates.

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// reFencedYAML captures the body of a ```yaml fenced block.
var reFencedYAML = regexp.MustCompile("(?s)```yaml\\s*\\n(.*?)```")

// YAMLBlocks returns the fenced YAML blocks in a markdown section, in order.
func YAMLBlocks(markdown string) []string {
	var out []string
	for _, m := range reFencedYAML.FindAllStringSubmatch(markdown, -1) {
		out = append(out, m[1])
	}
	return out
}

// Section returns the body of a "## Heading" section, exclusive of the next one.
func Section(markdown, heading string) string {
	marker := "\n## " + heading
	i := strings.Index(markdown, marker)
	if i < 0 {
		if strings.HasPrefix(markdown, "## "+heading) {
			i = 0
		} else {
			return ""
		}
	}
	rest := markdown[i+1:]
	if j := strings.Index(rest[len("## "+heading):], "\n## "); j >= 0 {
		return rest[:j+len("## "+heading)]
	}
	return rest
}

// ParseSectionYAML parses every fenced YAML block in a section into out.
//
// Blocks are parsed independently and merged by the caller's type, because a
// section may carry several — a Dependencies section with one block per
// dependency is valid and common.
//
// A block that does not parse is REPORTED, with the blueprint's name and the
// parser's message. Silently skipping it is how a malformed contract reaches a
// model unread, which is the failure this file exists to end.
func ParseSectionYAML(blueprintPath, markdown, heading string, into func([]byte) error) []error {
	var errs []error
	for i, block := range YAMLBlocks(Section(markdown, heading)) {
		if err := into([]byte(block)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %s block %d: %w", blueprintPath, heading, i+1, err))
		}
	}
	return errs
}

// dependenciesDoc is the shape of a Dependencies block.
//
// Field names are the YAML keys verbatim, in the snake_case the format uses;
// the Go names beside them are PascalCase, as Go requires. The mapping is
// STATED in a tag rather than derived by case conversion, because a converter
// has to guess — and guessing turned ChannelID into channel_i_d.
type dependenciesDoc struct {
	Requires []struct {
		Blueprint string `yaml:"blueprint"`
		Version   string `yaml:"version"`
		Bindings  struct {
			Types []struct {
				Name       string   `yaml:"name"`
				FieldsUsed []string `yaml:"fields_used"`
			} `yaml:"types"`
			Behaviors []struct {
				Name       string   `yaml:"name"`
				FieldsUsed []string `yaml:"fields_used"`
			} `yaml:"behaviors"`
			Endpoints []struct {
				Path string `yaml:"path"`
			} `yaml:"endpoints"`
			Events []struct {
				Topic string `yaml:"topic"`
			} `yaml:"events"`
		} `yaml:"bindings"`
		OnChange map[string]string `yaml:"on_change"`
	} `yaml:"requires"`
}

// ParseBindings reads a blueprint's dependency contract with a YAML parser.
//
// Returns the bindings and any block that would not parse. A caller that wants
// the old permissive behaviour can ignore the errors; generation must not,
// because an unparsed contract is a contract nobody read.
func ParseBindings(blueprintPath, blueprint string) ([]Binding, []error) {
	var out []Binding
	errs := ParseSectionYAML(blueprintPath, blueprint, "Dependencies", func(b []byte) error {
		var doc dependenciesDoc
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return err
		}
		for _, r := range doc.Requires {
			for _, t := range r.Bindings.Types {
				out = append(out, Binding{
					From: r.Blueprint, Kind: "type", Type: t.Name, FieldsUsed: t.FieldsUsed,
				})
			}
			for _, bh := range r.Bindings.Behaviors {
				// A behaviour may name fields too — threat-model binds
				// boundary-enforcement as [message, storage, external, response].
				// Recorded as declared; field checking skips behaviours, so these
				// are never validated against a type and never accused.
				out = append(out, Binding{
					From: r.Blueprint, Kind: "behavior", Type: bh.Name, FieldsUsed: bh.FieldsUsed,
				})
			}
		}
		return nil
	})
	return out, errs
}
