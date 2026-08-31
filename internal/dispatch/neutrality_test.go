package dispatch

// The blueprint corpus is checked against its own neutrality rule.
//
// schemas/common.md states it: a specification blueprint states requirements in
// terms of algorithms, formats and behaviour, and a platform blueprint translates
// them. Both directions were violated, each violation broke a build, and writing
// the rule down does not stop the next one.
//
// A documented rule nothing enforces is a rule that has already started drifting.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const blueprintsRoot = "/Users/lwilson/Projects/Avaropoint/weblisk-blueprints"

// specificationDirs hold blueprints that state requirements.
var specificationDirs = []string{"protocol", "architecture", "patterns", "agents"}

// platformArtifacts are references only a platform blueprint may make.
var platformArtifacts = []*regexp.Regexp{
	regexp.MustCompile(`\bgolang\.org/x/\w+`),
	regexp.MustCompile(`\bgithub\.com/cloudflare/circl\b`),
	regexp.MustCompile(`\bmodernc\.org/\w+`),
	regexp.MustCompile(`\bbetter-sqlite3\b`),
	regexp.MustCompile(`\brusqlite\b`),
	regexp.MustCompile(`\bDurable Objects?\b`),
}

// facilityBlueprints are the narrow exception: their subject IS a platform
// facility, so they may name it, provided they state where it is unavailable.
var facilityBlueprints = map[string]bool{
	"patterns/command.md": true,
	"patterns/interop.md": true,
	"architecture/cli.md": true,
}

func readBlueprints(t *testing.T, dirs []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, d := range dirs {
		entries, err := os.ReadDir(filepath.Join(blueprintsRoot, d))
		if err != nil {
			t.Skipf("blueprints not present: %v", err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
				continue
			}
			rel := d + "/" + e.Name()
			b, err := os.ReadFile(filepath.Join(blueprintsRoot, rel))
			if err != nil {
				continue
			}
			out[rel] = string(b)
		}
	}
	return out
}

// stripFences removes fenced code blocks, which carry illustrative examples —
// a Dockerfile in patterns/deployment is an example, not a requirement.
func stripFences(s string) string {
	var b strings.Builder
	inFence := false
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func TestSpecificationBlueprintsNameNoPlatformArtifact(t *testing.T) {
	for rel, body := range readBlueprints(t, specificationDirs) {
		if facilityBlueprints[rel] {
			continue
		}
		prose := stripFences(body)
		for _, re := range platformArtifacts {
			// A blueprint may quote a violation it is recording; the schema rule
			// and the identity notation section both do.
			for _, m := range re.FindAllString(prose, -1) {
				if strings.Contains(prose, "platform blueprint") &&
					strings.Contains(prose, m) &&
					strings.Contains(strings.ToLower(prose), "once carried") {
					continue
				}
				t.Errorf("%s names the platform artifact %q — that belongs in platforms/", rel, m)
			}
		}
	}
}

func TestEveryPlatformBlueprintCarriesAPrimitiveMapping(t *testing.T) {
	// Only go.md answered where the signature algorithm and key-derivation
	// function come from. rust.md, node.md and cloudflare.md answered neither, so
	// a hub generated for any of them had no stated way to satisfy
	// protocol/identity at all — and nothing said so.
	entries, err := os.ReadDir(filepath.Join(blueprintsRoot, "platforms"))
	if err != nil {
		t.Skip("blueprints not present")
	}
	seen := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
			continue
		}
		seen++
		b, err := os.ReadFile(filepath.Join(blueprintsRoot, "platforms", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		body := string(b)
		if !strings.Contains(body, "## Primitive Mapping") {
			t.Errorf("platforms/%s has no Primitive Mapping section — nothing says where its primitives come from", e.Name())
			continue
		}
		// Every primitive the protocol requires must have a row, filled or
		// explicitly UNFILLED. An omitted slot is indistinguishable from a
		// satisfied one.
		for _, primitive := range []string{"signature algorithm", "key-derivation function", "symmetric encryption", "random source"} {
			if !strings.Contains(body, primitive) {
				t.Errorf("platforms/%s does not map %q", e.Name(), primitive)
			}
		}
	}
	if seen < 4 {
		t.Errorf("only %d platform blueprints read; expected at least 4", seen)
	}
}

func TestPlatformBlueprintsDoNotRestateRequirements(t *testing.T) {
	// go.md once carried the key-derivation algorithm with its RFC number and its
	// parameters, making a second normative copy of a protocol/identity section
	// one directory away. Naming the module is translation; repeating the
	// standard is duplication.
	entries, err := os.ReadDir(filepath.Join(blueprintsRoot, "platforms"))
	if err != nil {
		t.Skip("blueprints not present")
	}
	// The first version of this guard flagged every RFC and FIPS citation, and
	// immediately failed on five that are correct: a package table row reading
	// "@noble/post-quantum — ML-DSA-65 cryptography (FIPS 204)" is identifying
	// WHICH primitive the package provides, and the standard's name is the least
	// ambiguous way to do that. Forbidding it makes the mapping vaguer, not
	// cleaner.
	//
	// The drift risk is the algorithm's PARAMETERS. A platform blueprint carrying
	// "time=3, memory=65536, parallelism=4" has copied a tunable set that
	// protocol/identity may change, and the copy will not change with it.
	reParams := regexp.MustCompile(`\b(time|memory|parallelism|iterations|saltlen|keylen)\s*=\s*\d+`)
	// Key and signature sizes are the same class: stated in protocol/types, and a
	// platform copy silently disagrees with it after any algorithm change.
	reSizes := regexp.MustCompile(`\b(1952|3309|4032)\s*bytes?\b`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(blueprintsRoot, "platforms", e.Name()))
		prose := stripFences(string(b))
		for _, re := range []*regexp.Regexp{reParams, reSizes} {
			for _, m := range re.FindAllString(prose, -1) {
				t.Errorf("platforms/%s carries %q — a parameter belongs to the blueprint that requires it, and a copy here cannot follow it when it changes",
					e.Name(), strings.TrimSpace(m))
			}
		}
	}
}
