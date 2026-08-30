package dispatch

// Generation manifests.
//
// A platform blueprint MAY carry a `### Generation Manifest` — a machine-readable
// form of its Project Structure, declaring which files an implementation is made
// of, what each must define, and which endpoints each must serve. See
// schemas/platform.md.
//
// It is not the definition of a hub. protocol/spec.md is the contract and
// architecture/testing.md is the proof; a manifest makes ONE route repeatable —
// generating a base hub the same way each time — by taking the file set out of
// the model's hands and putting it in the blueprint's.
//
// # Why parsing is restricted rather than general
//
// This CLI has two dependencies and its own generation prompt requires the
// standard library, so a YAML package would be a conspicuous addition for one
// block of our own content in a known shape. The parser below understands
// exactly that shape and REFUSES anything else. That is the important property:
// an unrecognised construct is an error naming the line, never a silently
// dropped field — a manifest that half-parses would generate a hub missing
// files nobody asked it to omit.

import (
	"fmt"
	"regexp"
	"strings"
)

// ManifestFile is one file an implementation must contain.
type ManifestFile struct {
	Path       string
	Purpose    string
	MustDefine []string
	MustServe  []string
}

// ManifestTarget is one buildable thing — an orchestrator, an agent, a gateway.
type ManifestTarget struct {
	Root        string
	Build       string
	Files       []ManifestFile
	Conformance []string
}

// GenerationManifest is the `generate:` block of a platform blueprint.
type GenerationManifest struct {
	Targets map[string]*ManifestTarget
}

var reManifestBlock = regexp.MustCompile("(?s)### Generation Manifest.*?```yaml\\n(.*?)```")

// ExtractManifest pulls the manifest out of a platform blueprint's markdown.
// Returns nil with no error when the blueprint carries none — a platform
// blueprint without a manifest is complete, not broken.
func ExtractManifest(blueprint string) (*GenerationManifest, error) {
	m := reManifestBlock.FindStringSubmatch(blueprint)
	if m == nil {
		return nil, nil
	}
	return ParseManifest(m[1])
}

// indentOf returns the leading-space count, and refuses tabs.
//
// Tabs and spaces mixed in an indentation-significant format is how a file reads
// correctly and parses wrongly. Refusing outright is cheaper than guessing.
func indentOf(line string) (int, error) {
	n := 0
	for _, r := range line {
		switch r {
		case ' ':
			n++
		case '\t':
			return 0, fmt.Errorf("tab in indentation; manifests use spaces")
		default:
			return n, nil
		}
	}
	return n, nil
}

// splitKeyValue splits "key: value", returning the value unquoted.
func splitKeyValue(s string) (string, string, bool) {
	i := strings.Index(s, ":")
	if i < 0 {
		return "", "", false
	}
	k := strings.TrimSpace(s[:i])
	v := strings.TrimSpace(s[i+1:])
	return k, unquote(v), true
}

func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// parseInlineList reads "[a, b, c]" or an empty "[]".
func parseInlineList(v string) ([]string, bool) {
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return nil, false
	}
	inner := strings.TrimSpace(v[1 : len(v)-1])
	if inner == "" {
		return []string{}, true
	}
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := unquote(strings.TrimSpace(p)); t != "" {
			out = append(out, t)
		}
	}
	return out, true
}

// ParseManifest reads the restricted subset described in schemas/platform.md.
func ParseManifest(src string) (*GenerationManifest, error) {
	man := &GenerationManifest{Targets: map[string]*ManifestTarget{}}

	var target *ManifestTarget
	var file *ManifestFile
	// pendingList is the key currently accumulating "- item" lines, and
	// fileIndent is the indentation of the "- path:" line that opened the
	// current file entry.
	//
	// Both are needed. Without the indent, a block list still open when the next
	// file begins swallows "- path: helpers.go" as a list entry — the file
	// vanishes from the manifest, generation silently produces one fewer file
	// than the blueprint declares, and the omission surfaces much later as a
	// build error about a missing symbol.
	var pendingList string
	fileIndent := -1
	seenGenerate := false

	lines := strings.Split(src, "\n")
	for n, raw := range lines {
		lineNo := n + 1
		if t := strings.TrimSpace(raw); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		indent, err := indentOf(raw)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		body := strings.TrimSpace(raw)

		// A list item belonging to must_define / must_serve — only when indented
		// DEEPER than the file entry that opened this block.
		if strings.HasPrefix(body, "- ") && pendingList != "" && file != nil && indent > fileIndent {
			item := unquote(strings.TrimSpace(body[2:]))
			switch pendingList {
			case "must_define":
				file.MustDefine = append(file.MustDefine, item)
			case "must_serve":
				file.MustServe = append(file.MustServe, item)
			}
			continue
		}

		// A new file entry: "- path: main.go"
		if strings.HasPrefix(body, "- ") {
			rest := strings.TrimSpace(body[2:])
			k, v, ok := splitKeyValue(rest)
			if !ok || k != "path" {
				return nil, fmt.Errorf("line %d: a file entry must begin with %q, got %q", lineNo, "- path:", rest)
			}
			if target == nil {
				return nil, fmt.Errorf("line %d: file entry outside a target", lineNo)
			}
			target.Files = append(target.Files, ManifestFile{Path: v})
			file = &target.Files[len(target.Files)-1]
			pendingList = ""
			fileIndent = indent
			continue
		}

		k, v, ok := splitKeyValue(body)
		if !ok {
			return nil, fmt.Errorf("line %d: expected %q, got %q", lineNo, "key: value", body)
		}

		switch k {
		case "generate":
			seenGenerate = true
			target, file, pendingList = nil, nil, ""
			continue
		case "root", "build":
			if target == nil {
				return nil, fmt.Errorf("line %d: %q outside a target", lineNo, k)
			}
			if k == "root" {
				target.Root = v
			} else {
				target.Build = v
			}
			file, pendingList = nil, ""
			continue
		case "files":
			if target == nil {
				return nil, fmt.Errorf("line %d: files outside a target", lineNo)
			}
			file, pendingList = nil, ""
			continue
		case "conformance":
			if target == nil {
				return nil, fmt.Errorf("line %d: conformance outside a target", lineNo)
			}
			list, isList := parseInlineList(v)
			if !isList {
				return nil, fmt.Errorf("line %d: conformance must be an inline list, got %q", lineNo, v)
			}
			target.Conformance = list
			file, pendingList = nil, ""
			continue
		case "purpose":
			if file == nil {
				return nil, fmt.Errorf("line %d: purpose outside a file entry", lineNo)
			}
			file.Purpose = v
			pendingList = ""
			continue
		case "must_define", "must_serve":
			if file == nil {
				return nil, fmt.Errorf("line %d: %q outside a file entry", lineNo, k)
			}
			if list, isList := parseInlineList(v); isList {
				if k == "must_define" {
					file.MustDefine = list
				} else {
					file.MustServe = list
				}
				pendingList = ""
			} else if v == "" {
				pendingList = k
			} else {
				return nil, fmt.Errorf("line %d: %q must be a list", lineNo, k)
			}
			continue
		}

		// A target name — the only remaining legal key, and only directly
		// under `generate:`.
		if seenGenerate && indent > 0 && v == "" {
			t := &ManifestTarget{}
			man.Targets[k] = t
			target, file, pendingList = t, nil, ""
			continue
		}
		return nil, fmt.Errorf("line %d: unrecognised key %q", lineNo, k)
	}

	if !seenGenerate {
		return nil, fmt.Errorf("no generate: block found")
	}
	if len(man.Targets) == 0 {
		return nil, fmt.Errorf("generate: declares no targets")
	}
	return man, man.validate()
}

// validate applies the rules in schemas/platform.md that can be checked without
// the protocol blueprint in hand.
func (m *GenerationManifest) validate() error {
	for name, t := range m.Targets {
		if len(t.Files) == 0 {
			return fmt.Errorf("target %q declares no files", name)
		}
		if strings.TrimSpace(t.Build) == "" {
			return fmt.Errorf("target %q has no build command", name)
		}
		if len(t.Conformance) == 0 {
			return fmt.Errorf("target %q names no conformance level", name)
		}
		seen := map[string]bool{}
		for _, f := range t.Files {
			switch {
			case f.Path == "":
				return fmt.Errorf("target %q has a file with no path", name)
			case f.Purpose == "":
				return fmt.Errorf("target %q: %q has no purpose", name, f.Path)
			case seen[f.Path]:
				return fmt.Errorf("target %q lists %q twice", name, f.Path)
			}
			seen[f.Path] = true
			// Containment is enforced again at write time; refusing here means a
			// bad manifest fails before a single model call is spent.
			if _, err := safeGeneratedPath("/manifest-check", f.Path); err != nil {
				return fmt.Errorf("target %q: %w", name, err)
			}
		}
	}
	return nil
}

// Target returns a named target, or an error listing what is available.
func (m *GenerationManifest) Target(name string) (*ManifestTarget, error) {
	if t, ok := m.Targets[name]; ok {
		return t, nil
	}
	names := make([]string, 0, len(m.Targets))
	for n := range m.Targets {
		names = append(names, n)
	}
	return nil, fmt.Errorf("no %q target in this manifest; it declares: %s", name, strings.Join(names, ", "))
}
