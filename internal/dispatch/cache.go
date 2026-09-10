package dispatch

// Not regenerating what has not changed.
//
// # The waste this removes
//
// Every run regenerated every file from nothing. A run that failed at file eight
// discarded seven correct files and started over; fixing a bug in the checking
// layer meant regenerating twelve files to test a change that affected one. Four
// consecutive runs produced substantially the same identity.go at roughly two
// minutes each.
//
// That is the opposite of what a specification is for. The point of writing the
// contract down is that work becomes reusable — if protocol/identity.md has not
// changed and the plan still asks identity.go to do the same thing, the file that
// satisfied it yesterday satisfies it today.
//
// # The key is the inputs, not the output
//
// A cached file is valid exactly when everything that produced it is unchanged:
// the plan entry, the blueprints that file was actually sent, the system prompt,
// and the platform blueprint. Hash those and you have derived staleness — the
// same mechanism patterns/content-identity specifies for documents, applied to
// generation.
//
// This is why relevance filtering matters twice over: a file sent only the
// blueprints it needs is invalidated only by changes to THOSE. Editing
// architecture/orchestrator.md leaves go.mod alone.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// cacheDirName is where generated files are remembered, inside the instance's
// own state directory rather than beside the output.
const cacheDirName = ".weblisk/generation"

// GenerationCache remembers files by the inputs that produced them.
type GenerationCache struct {
	dir     string
	Hits    int
	Misses  int
	Enabled bool
}

// NewGenerationCache opens the cache under a project root.
//
// A cache that cannot be created disables itself rather than failing the run:
// losing reuse is an inconvenience, and refusing to generate over it is not.
func NewGenerationCache(root string) *GenerationCache {
	dir := filepath.Join(root, cacheDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &GenerationCache{Enabled: false}
	}
	return &GenerationCache{dir: dir, Enabled: true}
}

// cacheKey hashes the prompt that would produce the file.
//
// # Why the prompt itself, and not a list of ingredients
//
// This used to hash a hand-listed set: the plan entry, the blueprints sent, the
// platform blueprint, the system prompt. That list was a second statement of
// what determines a file, and it drifted from the first the moment the prompt
// gained an input nobody added to it.
//
// It did. The module path was added to the prompt — the fact that made forty-nine
// files agree on their import paths — and the key did not change, so a later run
// served forty-three files generated BEFORE that fix. They imported a module
// name that no longer existed, and the build failed with "package
// weblisk-server/internal/identity is not in std" in a file nobody had touched.
//
// Hashing the rendered prompt removes the possibility. Any change to what the
// model is told changes the key, without anyone having to remember.
//
// # What is deliberately excluded
//
// The prompt is rendered with no accumulated declarations, so a file's key does
// not depend on the siblings written before it. Including them would invalidate
// every later file whenever any earlier one changed, which costs a full
// regeneration for a change that usually affects nothing. That is the same
// trade the previous key made, kept deliberately rather than by omission.
func cacheKey(f PlannedFile, prompt, systemPrompt string) string {
	h := sha256.New()
	h.Write([]byte(prompt))
	h.Write([]byte(systemPrompt))
	return hex.EncodeToString(h.Sum(nil))
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// Get returns cached content for a key, or "" when there is none.
func (c *GenerationCache) Get(key string) string {
	if !c.Enabled {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(c.dir, key))
	if err != nil {
		c.Misses++
		return ""
	}
	c.Hits++
	return string(b)
}

// Put stores content under a key.
//
// Write failures are ignored on purpose: a cache that cannot save is slower, and
// a run that fails because it could not save is broken.
func (c *GenerationCache) Put(key, content string) {
	if !c.Enabled {
		return
	}
	_ = os.WriteFile(filepath.Join(c.dir, key), []byte(content), 0o644)
}

// Summary describes reuse for a run.
func (c *GenerationCache) Summary() string {
	if !c.Enabled {
		return "cache disabled"
	}
	total := c.Hits + c.Misses
	if total == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d files reused from cache", c.Hits, total)
}

// Prune removes cached entries not referenced by a set of live keys.
//
// Offered rather than run automatically: a stale entry costs disk and a pruned
// one costs a regeneration, and which is worse depends on how often blueprints
// change.
func (c *GenerationCache) Prune(live map[string]bool) (int, error) {
	if !c.Enabled {
		return 0, nil
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() || live[e.Name()] {
			continue
		}
		// Only remove things that look like our keys.
		if len(e.Name()) != 64 || strings.ContainsAny(e.Name(), "./") {
			continue
		}
		if os.Remove(filepath.Join(c.dir, e.Name())) == nil {
			removed++
		}
	}
	return removed, nil
}

// planKey hashes the inputs that determine a plan.
//
// The requirements and the planning instructions — nothing else. Two runs with
// unchanged blueprints should produce the SAME plan, and re-deriving it is not
// merely wasteful: a model that plans ten files where it planned twelve
// invalidates every per-file cache entry, so the file cache never hits and the
// whole run regenerates. Caching the plan is what makes caching the files work.
func planKey(req *Requirements, target, platform, platBP, systemPrompt, layout string) string {
	h := sha256.New()
	h.Write([]byte(target))
	h.Write([]byte(platform))
	for _, t := range req.Types {
		h.Write([]byte(t))
	}
	for _, e := range req.Endpoints {
		h.Write([]byte(e))
	}
	// The DECLARED names, not only the wire facts.
	//
	// A plan is a set of symbols, and these are what those symbols are spelled
	// from — so renaming an operation in a blueprint must re-derive the plan.
	// Leaving them out would serve a cached plan built from the old names and
	// generate against a contract nobody holds any more. cacheKey's own comment
	// records the last time an input was added to a prompt and not to its key:
	// forty-three files were served that imported a module path that no longer
	// existed.
	for _, e := range req.EndpointOps {
		h.Write([]byte(e.Method))
		h.Write([]byte(e.Path))
		h.Write([]byte(e.Operation))
	}
	for _, op := range req.Operations {
		h.Write([]byte(op))
	}
	for _, c := range req.Checklist {
		h.Write([]byte(c.Source))
		h.Write([]byte(c.Text))
	}
	h.Write([]byte(platBP))
	h.Write([]byte(systemPrompt))
	// Where the files go is now told to the planner, so it must reach the key
	// too — see the note above on the forty-three files served against a module
	// path that no longer existed.
	h.Write([]byte(layout))
	return "plan-" + hex.EncodeToString(h.Sum(nil))
}

// GetPlan returns a cached plan for these requirements, or nil.
func (c *GenerationCache) GetPlan(key string) *Plan {
	raw := c.Get(key)
	if raw == "" {
		return nil
	}
	var p Plan
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil
	}
	if len(p.Files) == 0 {
		return nil
	}
	return &p
}

// PutPlan stores a validated plan.
func (c *GenerationCache) PutPlan(key string, p *Plan) {
	if b, err := json.Marshal(p); err == nil {
		c.Put(key, string(b))
	}
}
