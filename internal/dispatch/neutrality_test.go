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
	"sort"
	"strings"
	"testing"
)

// blueprintsRoot locates the corpus these tests check.
//
// # Why this is resolved and not a constant
//
// It was an absolute path on one machine. CI checks out only this repository,
// so the path did not exist there, readBlueprints called t.Skipf, and EVERY
// corpus test skipped silently — the neutrality rule, the declared-name rules,
// the protected-endpoint derivation, all of it. They protected one laptop.
//
// Resolution order, most explicit first:
//
//	WL_BLUEPRINTS        — set by CI after checking the corpus out
//	../weblisk-blueprints — the sibling layout a developer actually has
//	./blueprints          — a vendored or symlinked copy
//
// WL_REQUIRE_BLUEPRINTS=1 turns a skip into a failure. CI sets it, so a corpus
// that fails to check out is reported rather than quietly reducing the suite.
func blueprintsRootDir() string {
	if p := strings.TrimSpace(os.Getenv("WL_BLUEPRINTS")); p != "" {
		return p
	}
	for _, candidate := range []string{
		filepath.Join("..", "..", "..", "weblisk-blueprints"),
		filepath.Join("..", "..", "blueprints"),
	} {
		if fi, err := os.Stat(filepath.Join(candidate, "schemas", "common.md")); err == nil && !fi.IsDir() {
			return candidate
		}
	}
	return ""
}

// requireBlueprints reports whether an absent corpus is a failure.
func requireBlueprints() bool { return os.Getenv("WL_REQUIRE_BLUEPRINTS") == "1" }

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
	root := blueprintsRootDir()
	if root == "" {
		if requireBlueprints() {
			t.Fatal("the blueprint corpus is not present and WL_REQUIRE_BLUEPRINTS=1 — " +
				"set WL_BLUEPRINTS or check out weblisk-blueprints beside this repository")
		}
		t.Skip("blueprint corpus not found; set WL_BLUEPRINTS to check it")
	}
	for _, d := range dirs {
		entries, err := os.ReadDir(filepath.Join(root, d))
		if err != nil {
			if requireBlueprints() {
				t.Fatalf("the blueprint corpus is incomplete: %v", err)
			}
			t.Skipf("blueprints not present: %v", err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
				continue
			}
			rel := d + "/" + e.Name()
			b, err := os.ReadFile(filepath.Join(root, rel))
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
	entries, err := os.ReadDir(filepath.Join(blueprintsRootDir(), "platforms"))
	if err != nil {
		t.Skip("blueprints not present")
	}
	seen := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
			continue
		}
		seen++
		b, err := os.ReadFile(filepath.Join(blueprintsRootDir(), "platforms", e.Name()))
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
	entries, err := os.ReadDir(filepath.Join(blueprintsRootDir(), "platforms"))
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
		b, _ := os.ReadFile(filepath.Join(blueprintsRootDir(), "platforms", e.Name()))
		prose := stripFences(string(b))
		for _, re := range []*regexp.Regexp{reParams, reSizes} {
			for _, m := range re.FindAllString(prose, -1) {
				t.Errorf("platforms/%s carries %q — a parameter belongs to the blueprint that requires it, and a copy here cannot follow it when it changes",
					e.Name(), strings.TrimSpace(m))
			}
		}
	}
}

// knownBindingGaps are the requires: entries that no dependency contract covers,
// as they stand today.
//
// Pinned rather than tolerated. Filling one correctly means reading both
// documents and stating what is genuinely consumed — real specification work,
// and guessing would put a wrong contract in front of every future generation.
// So the set is recorded, may not grow, and shrinks as each is closed.
//
// architecture/orchestrator is absent from this list because it was the one that
// cost a build: it declared architecture/storage and bound nothing from it, so
// 27 KB of storage specification reached the model unexplained and the generated
// orchestrator implemented all fourteen documented stores — ten of them owned by
// other components.
var knownBindingGaps = map[string]bool{
	"architecture/admin.md architecture/orchestrator":             true,
	"architecture/admin.md protocol/spec":                         true,
	"architecture/agent.md architecture/observability":            true,
	"architecture/agent.md patterns/messaging":                    true,
	"architecture/agent.md patterns/retry":                        true,
	"architecture/agent.md protocol/spec":                         true,
	"architecture/change-management.md architecture/orchestrator": true,
	"architecture/change-management.md patterns/messaging":        true,
	"architecture/change-management.md patterns/versioning":       true,
	"architecture/change-management.md protocol/spec":             true,
	"architecture/cli.md architecture/orchestrator":               true,
	"architecture/cli.md patterns/command":                        true,
	"architecture/cli.md protocol/spec":                           true,
	"architecture/data-security.md architecture/enforcement":      true,
	"architecture/data-security.md patterns/contract":             true,
	"architecture/data-security.md patterns/policy":               true,
	"architecture/data-security.md patterns/privacy":              true,
	"architecture/data-security.md patterns/scope":                true,
	"architecture/data-security.md protocol/spec":                 true,
	"architecture/domain.md architecture/orchestrator":            true,
	"architecture/domain.md patterns/messaging":                   true,
	"architecture/domain.md patterns/workflow":                    true,
	"architecture/gateway.md architecture/admin":                  true,
	"architecture/gateway.md architecture/observability":          true,
	"architecture/gateway.md patterns/api-ai":                     true,
	"architecture/gateway.md patterns/auth-session":               true,
	"architecture/gateway.md patterns/auth-token":                 true,
	"architecture/gateway.md patterns/rate-limiting":              true,
	"architecture/gateway.md patterns/user-management":            true,
	"architecture/generation.md protocol/spec":                    true,
	"architecture/lifecycle.md patterns/messaging":                true,
	"architecture/storage.md architecture/gateway":                true,
}

func TestFrontmatterRequiresAndBindingContractsAgree(t *testing.T) {
	// The two declarations are the same claim at two resolutions. frontmatter
	// `requires:` says which blueprints a component depends on; the Dependencies
	// block says what it consumes from each. Generation reads the second, so a
	// dependency present in the first and absent from the second arrives at the
	// model with no statement of what it is for.
	//
	// That is not a tidiness problem. architecture/orchestrator declared
	// architecture/storage, bound nothing from it, and the generated orchestrator
	// implemented store_lifecycle.go, store_gateway.go and store_execution.go —
	// state belonging to the Lifecycle Agent, the Gateway, and the Workflow and
	// Task agents.
	var found []string
	for rel, body := range readBlueprints(t, []string{"architecture"}) {
		bound := map[string]bool{}
		for _, b := range ExtractBindings(body) {
			bound[b.From] = true
		}
		if len(bound) == 0 {
			// No binding block at all is a different gap, and one this guard does
			// not yet insist on closing everywhere.
			continue
		}
		for _, req := range DeclaredRequires(body) {
			if bound[req] {
				continue
			}
			key := rel + " " + req
			if knownBindingGaps[key] {
				continue
			}
			found = append(found, key)
		}
	}
	sort.Strings(found)
	for _, f := range found {
		t.Errorf("new binding gap: %s\n"+
			"  frontmatter requires it and the dependency contract binds nothing from it.\n"+
			"  Generation reads the contract, so that blueprint reaches the model unexplained.", f)
	}
}

func TestTheKnownBindingGapsStillExist(t *testing.T) {
	// A pinned gap that has been closed must leave the list, or the list stops
	// describing the corpus and starts hiding it.
	present := map[string]bool{}
	for rel, body := range readBlueprints(t, []string{"architecture"}) {
		bound := map[string]bool{}
		for _, b := range ExtractBindings(body) {
			bound[b.From] = true
		}
		if len(bound) == 0 {
			continue
		}
		for _, req := range DeclaredRequires(body) {
			if !bound[req] {
				present[rel+" "+req] = true
			}
		}
	}
	for pinned := range knownBindingGaps {
		if !present[pinned] {
			t.Errorf("%q is pinned as a known gap and no longer exists — remove it from knownBindingGaps", pinned)
		}
	}
}

func TestPlatformBlueprintsNameNoSpecificComponent(t *testing.T) {
	// A platform blueprint says how a binary for this runtime is laid out and
	// built. Which agents and domain controllers exist is chosen by adopting
	// their blueprints — so an example agent in a platform document is a
	// component nobody asked for, presented as though it were part of the
	// platform.
	//
	// All four carried one: agents/seo, weblisk-agent-seo, seo-analyzer,
	// domains/seo, in structure listings, build commands and test fixtures.
	// Generation reads these documents whole, so a named component is an
	// invitation to build it.
	entries, err := os.ReadDir(filepath.Join(blueprintsRootDir(), "platforms"))
	if err != nil {
		t.Skip("blueprints not present")
	}
	// Names that would be a specific component rather than a placeholder.
	suspicious := regexp.MustCompile(`(?i)\b(seo|analyzer|crawler|scraper|summari[sz]er|translator|classifier)\b`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(blueprintsRootDir(), "platforms", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		// Prose and fenced examples alike: a build command in a fence is exactly
		// where these lived.
		for i, line := range strings.Split(string(b), "\n") {
			if m := suspicious.FindString(line); m != "" {
				t.Errorf("platforms/%s:%d names the component %q — a platform blueprint "+
					"describes the runtime, not which components a deployment has\n    %s",
					e.Name(), i+1, m, strings.TrimSpace(line))
			}
		}
	}
}

// A generation target must declare its HTTP surface where generation reads it.
//
// The schema template shows endpoints in the `## Interfaces` YAML block; the
// pipeline reads them from a `## Endpoints` TABLE and nowhere else. So a
// blueprint can state its whole surface, correctly, in the form a human reads
// and state nothing at all to the planner — and the plan is then never
// validated against a single endpoint.
//
// architecture/content.md was written that way first. It declared eight
// endpoints in YAML and required zero, which the pipeline reported as a target
// with no HTTP surface at all.
func TestEveryGenerationTargetDeclaresEndpointsWhereGenerationReadsThem(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol", "platforms", "patterns"})

	for _, target := range []string{"orchestrator", "agent", "content"} {
		file := targetBlueprint(target)
		if _, ok := bps[file]; !ok {
			t.Errorf("%s: %s is absent from this installation", target, file)
			continue
		}
		// The outcome, not the mechanism. protocol/spec supplies the surface for
		// the two components it names; every other component must supply its own
		// in an `## Endpoints` table, because nothing else is read.
		g := &BlueprintGraph{Map: bps, Order: []string{file, "protocol/spec.md"}}
		req := GatherRequirements(g, target)
		if len(req.Endpoints) == 0 {
			t.Errorf("%s: generation requires ZERO endpoints of it.\n"+
				"  An `## Endpoints` markdown table in %s is what the pipeline reads;\n"+
				"  the `## Interfaces` YAML block is not. With neither, the plan is\n"+
				"  validated against no HTTP surface and a missing endpoint is never\n"+
				"  detected.", target, file)
		}
	}

	// The component whose surface exists nowhere but its own blueprint. Asserted
	// separately because for it the table is the ONLY source, so losing the
	// table would silently drop all eight endpoints.
	if got := ExtractTableEndpoints(bps["architecture/content.md"]); len(got) < 8 {
		t.Errorf("architecture/content.md yields %d endpoints from its table, want its full surface", len(got))
	}
}
