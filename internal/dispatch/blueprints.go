package dispatch

// Resolving blueprints, and knowing which copy you resolved.
//
// # The cache that never expired
//
// Blueprints were fetched with a depth-one clone into ~/.weblisk/blueprints and
// then served forever. ensureCloned's entire freshness test was "does the
// directory have anything in it", so a clone taken in May was still answering
// questions in August. Nothing in the output said so.
//
// That is not a slow cache. It is a specification pipeline reading a
// specification nobody wrote, and reporting conformance against it.
//
// # Two things a cache owes its caller
//
// Freshness is the obvious one, and the cheap one: fetch and reset when the copy
// is older than the TTL, keep working offline when the network is gone.
//
// Provenance is the one that was actually missing. Blueprints resolve from three
// places — the project, custom sources, the core repo — and the first hit wins.
// When a measurement disagrees with the file you just edited, the question is
// never "is the cache stale"; it is "which copy did that number come from". A
// resolver that cannot answer it turns every disagreement into a guess.
//
// So resolution reports the directory, the kind of source, and the commit. The
// generation cache keys on blueprint CONTENT (see cache.go), so a refresh
// invalidates exactly the files whose inputs moved — but only if the refresh
// happens at all.

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/avaropoint/weblisk-cli/internal/config"
)

const coreRepo = "https://github.com/avaropoint/weblisk-blueprints.git"

// blueprintTTL is how long a fetched copy is trusted before it is refreshed.
//
// A day, because blueprints are specifications: they change on the scale of
// someone deciding something, not on the scale of a build. Long enough that a
// day of generation runs costs one fetch; short enough that yesterday's decision
// is in today's build.
const blueprintTTL = 24 * time.Hour

// fetchStamp records when a cached source was last refreshed. Untracked inside
// the checkout, so `git reset --hard` leaves it alone.
const fetchStamp = ".weblisk-fetched"

// Source is a place blueprints are read from, and what is known about it.
type Source struct {
	Dir      string    // directory searched
	Kind     string    // "project", "custom" or "core"
	Origin   string    // repo URL; empty for a project directory
	Revision string    // short commit, when the directory is a checkout
	Fetched  time.Time // when this copy was last refreshed; zero when unknown
}

// Describe renders a source for output — everything needed to tell two copies
// of the same blueprints apart.
func (s Source) Describe() string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "%s (%s", s.Dir, s.Kind)
	if s.Revision != "" {
		fmt.Fprintf(b, " @%s", s.Revision)
	}
	if !s.Fetched.IsZero() {
		fmt.Fprintf(b, ", fetched %s ago", roundDuration(time.Since(s.Fetched)))
	}
	b.WriteString(")")
	return b.String()
}

func roundDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "moments"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// sourceDir returns a deterministic cache directory name for a repo URL.
func sourceDir(repoURL string) string {
	// Use the repo name if parseable, otherwise hash the URL.
	name := repoURL
	name = strings.TrimSuffix(name, ".git")
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	// Append a short hash to avoid collisions between repos with the same name.
	h := sha256.Sum256([]byte(repoURL))
	return fmt.Sprintf("%s-%x", name, h[:4])
}

// blueprintCacheBase returns the user-global blueprint cache directory.
func blueprintCacheBase() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".weblisk", "blueprints")
}

// offline reports whether network refreshes are suppressed.
//
// An explicit switch, because the alternative is inferring it from a failed
// fetch — which is slow on every run and wrong on a flaky link.
func offline() bool {
	v := strings.ToLower(os.Getenv("WL_BLUEPRINT_OFFLINE"))
	return v == "1" || v == "true" || v == "yes"
}

// ResolveSources returns the ordered sources blueprints are read from, refreshing
// cached copies whose TTL has passed.
//
// Order: local project → custom sources → core. First hit wins, which is what
// makes a local checkout override the cache — and what makes provenance worth
// reporting.
func ResolveSources(root string) []Source {
	var out []Source

	// 1. Local project blueprints (highest priority). Never refreshed: this is a
	// working directory, not a copy, and fetching over someone's edits would be a
	// cache eating their work.
	localDir := filepath.Join(root, "blueprints")
	if info, err := os.Stat(localDir); err == nil && info.IsDir() {
		out = append(out, Source{Dir: localDir, Kind: "project", Revision: revisionOf(localDir)})
	}

	cacheBase := blueprintCacheBase()

	// 2. Custom sources from WL_BLUEPRINT_SOURCES.
	cfg := config.Resolve()
	for _, repo := range cfg.BlueprintSources {
		cacheDir := filepath.Join(cacheBase, sourceDir(repo))
		if err := ensureFresh(repo, cacheDir); err != nil {
			fmt.Fprintf(os.Stderr, "  [warn] Blueprint source %s: %v\n", repo, err)
			continue
		}
		out = append(out, describeCache(cacheDir, "custom", repo))
	}

	// 3. Core blueprints (always present as fallback).
	coreDir := filepath.Join(cacheBase, sourceDir(coreRepo))
	if err := ensureFresh(coreRepo, coreDir); err != nil {
		fmt.Fprintf(os.Stderr, "  [warn] Core blueprints: %v\n", err)
	} else {
		out = append(out, describeCache(coreDir, "core", coreRepo))
	}

	return out
}

// resolvedSources returns just the directories, in order.
func resolvedSources(root string) []string {
	srcs := ResolveSources(root)
	dirs := make([]string, 0, len(srcs))
	for _, s := range srcs {
		dirs = append(dirs, s.Dir)
	}
	return dirs
}

func describeCache(dir, kind, origin string) Source {
	return Source{
		Dir:      dir,
		Kind:     kind,
		Origin:   origin,
		Revision: revisionOf(dir),
		Fetched:  stampTime(dir),
	}
}

// revisionOf returns the short commit of a directory that is a git checkout.
// revisionOf identifies the blueprint content a hub was generated from.
//
// # Why "-dirty" is not cosmetic here
//
// This returned HEAD alone. A tenant was generated from a working tree with
// six thousand lines of uncommitted edits and the run recorded
//
//	blueprints (project @79a5280) — 8 of 8
//
// which names a commit the content did not come from. An auditor who checks out
// 79a5280 gets a different corpus and cannot reproduce the artifact — and
// architecture/cli requires the blueprint version a hub was generated from to
// be part of its provenance.
//
// A hash that is wrong is worse than no hash, because it is acted on. The
// git convention for this is a "-dirty" suffix, and it is the honest answer:
// the artifact came from a tree that has no name.
func revisionOf(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD")
	outBytes, err := cmd.Output()
	if err != nil {
		return ""
	}
	rev := strings.TrimSpace(string(outBytes))
	if rev == "" {
		return ""
	}
	// Tracked modifications AND untracked files both count: a blueprint that
	// has not been added yet still reached the model.
	//
	// Except our OWN fetch marker, which every cached source carries. With it
	// counted, every cache read `@abc1234-dirty` forever — so the flag that
	// exists to say "somebody edited these blueprints" was permanently on, and
	// a cache that had genuinely been edited looked exactly like one that had
	// not. A warning that is always showing is not a warning.
	status, serr := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if serr == nil && hasRealChanges(string(status)) {
		return rev + "-dirty"
	}
	return rev
}

// hasRealChanges reports whether a porcelain status names anything that is not
// this program's own bookkeeping.
func hasRealChanges(status string) bool {
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Porcelain is "XY path"; the path is what matters here.
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[len(fields)-1] == fetchStamp {
			continue
		}
		return true
	}
	return false
}

func stampTime(dir string) time.Time {
	info, err := os.Stat(filepath.Join(dir, fetchStamp))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func touchStamp(dir string) {
	_ = os.WriteFile(filepath.Join(dir, fetchStamp), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

// ensureFresh clones a source that is missing and refreshes one that is stale.
//
// A refresh failure is a warning, never an error: a cached specification is
// worse than a current one and far better than none, and a build on a plane must
// still run. The staleness is reported by Describe, so the output says which it
// was working from.
func ensureFresh(repoURL, cacheDir string) error {
	entries, err := os.ReadDir(cacheDir)
	present := err == nil && len(entries) > 0

	if !present {
		return clone(repoURL, cacheDir)
	}
	if offline() {
		return nil
	}
	stamp := stampTime(cacheDir)
	if !stamp.IsZero() && time.Since(stamp) < blueprintTTL {
		return nil
	}
	if err := refresh(cacheDir); err != nil {
		fmt.Fprintf(os.Stderr, "  [warn] Could not refresh %s: %v\n    Using the cached copy; run `weblisk blueprint update` when connected.\n",
			filepath.Base(cacheDir), err)
		// Stamp anyway, so an offline session does not retry on every command.
		touchStamp(cacheDir)
	}
	return nil
}

func clone(repoURL, cacheDir string) error {
	fmt.Printf("  Fetching blueprints from %s...\n", repoURL)
	if err := os.MkdirAll(filepath.Dir(cacheDir), 0755); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}

	os.RemoveAll(cacheDir)
	cmd := exec.Command("git", "clone", "--depth=1", repoURL, cacheDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cloning %s: %w\n  If this is a private repo, ensure your Git credentials have access.", repoURL, err)
	}
	touchStamp(cacheDir)

	fmt.Printf("  [ok] Cached %s\n", filepath.Base(cacheDir))
	return nil
}

// refresh brings an existing shallow checkout up to its remote head.
//
// fetch + reset rather than delete + clone: it moves only what changed, and it
// cannot leave the cache empty if the network drops halfway.
func refresh(cacheDir string) error {
	before := revisionOf(cacheDir)
	fetch := exec.Command("git", "-C", cacheDir, "fetch", "--depth=1", "origin", "HEAD")
	if out, err := fetch.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	reset := exec.Command("git", "-C", cacheDir, "reset", "--hard", "FETCH_HEAD")
	if out, err := reset.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	touchStamp(cacheDir)
	if after := revisionOf(cacheDir); after != before && before != "" {
		fmt.Printf("  Blueprints updated: %s → %s\n", before, after)
	}
	return nil
}

// EnsureBlueprints resolves all blueprint sources and returns the list
// of cache directories. For backward compatibility, it returns the core
// cache directory as the primary path.
func EnsureBlueprints(root string) (string, error) {
	dirs := resolvedSources(root)
	if len(dirs) == 0 {
		return "", fmt.Errorf("no blueprint sources available — check your internet connection")
	}
	// Return the last dir (core) for callers that expect a single path.
	return dirs[len(dirs)-1], nil
}

// LoadBlueprint reads a single blueprint by name, checking sources in order.
func LoadBlueprint(root, name string) (string, error) {
	dirs := resolvedSources(root)
	for _, dir := range dirs {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err == nil {
			return string(data), nil
		}
	}
	return "", fmt.Errorf("blueprint %q not found in any source (%d sources checked)", name, len(dirs))
}

// LoadBlueprints loads and concatenates multiple blueprints.
func LoadBlueprints(root string, names ...string) (string, error) {
	var parts []string
	for _, name := range names {
		content, err := LoadBlueprint(root, name)
		if err != nil {
			return "", err
		}
		parts = append(parts, content)
	}
	return strings.Join(parts, "\n\n---\n\n"), nil
}

// LoadBlueprintMap loads blueprints keyed by name, so a caller can send only
// the ones a given file needs rather than the whole corpus every time.
//
// LoadBlueprints joins them into one string, which is right for a single request
// about everything and wrong for ten requests about one file each.
func LoadBlueprintMap(root string, names ...string) (map[string]string, []string, error) {
	out := map[string]string{}
	var order []string
	for _, name := range names {
		content, err := LoadBlueprint(root, name)
		if err != nil {
			return nil, nil, err
		}
		out[name] = content
		order = append(order, name)
	}
	return out, order, nil
}

// UpdateBlueprints forces every cached source to its remote head, ignoring the
// TTL, and reports what is now in place.
//
// It used to delete ~/.weblisk/blueprints outright. That threw away every custom
// source along with the core one and turned a refresh into a full re-clone of
// each — for a change that is usually a handful of edited lines. Expiring the
// stamps makes the ordinary refresh path do the work, so there is one mechanism
// to be correct rather than two.
func UpdateBlueprints(root string) error {
	expireStamps(blueprintCacheBase())

	srcs := ResolveSources(root)
	if len(srcs) == 0 {
		return fmt.Errorf("no blueprint sources available after refresh")
	}
	fmt.Printf("  [ok] %d blueprint source(s) ready\n", len(srcs))
	for _, s := range srcs {
		fmt.Printf("    %s\n", s.Describe())
	}
	return nil
}

// expireStamps removes the fetch stamps under a cache base so the next
// resolution refreshes rather than trusting the TTL.
func expireStamps(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			_ = os.Remove(filepath.Join(base, e.Name(), fetchStamp))
		}
	}
}

// PlatformBlueprint returns the blueprint path for a platform.
func PlatformBlueprint(platform string) string {
	switch platform {
	case "cloudflare":
		return "platforms/cloudflare.md"
	case "node":
		return "platforms/node.md"
	case "rust":
		return "platforms/rust.md"
	default:
		return "platforms/go.md"
	}
}

// DomainBlueprint returns the blueprint path for an agent's domain.
func DomainBlueprint(name string) string {
	// Check agents/ first, then domains/ for backwards compatibility
	return "agents/" + name + ".md"
}

// DomainControllerBlueprint returns the blueprint path for a domain controller.
func DomainControllerBlueprint(name string) string {
	return "architecture/domain.md"
}

// PatternBlueprint returns the blueprint path for a pattern.
func PatternBlueprint(name string) string {
	return "patterns/" + name + ".md"
}

// AnnounceSources states which blueprints a build is about to read.
//
// # Why a build must say this out loud
//
// Blueprints resolve local checkout → custom sources → shared cache, first hit
// wins, and until this existed a build said nothing about which it used. On
// 2026-09-06 that produced the quietest possible failure: a week of blueprint
// work sat in a local repository forty commits ahead of its remote, the shared
// cache faithfully refreshed itself FROM that remote, and every hub generated
// outside the local checkout was built and conformance-repaired against the old
// specification. Nothing was broken. Nothing said anything. The work simply did
// not reach a single build.
//
// Reported before generation rather than after, because after is a diagnosis
// and before is a decision.
func AnnounceSources(srcs []Source) {
	if len(srcs) == 0 {
		return
	}
	fmt.Println("  Blueprints:")
	onlyCore := true
	for _, s := range srcs {
		// Describe already carries the revision and the fetch age; adding a
		// second rendering of the same fact printed "fetched 1h ago — fetched
		// 1 hours ago".
		fmt.Printf("    %s\n", s.Describe())
		if s.Kind != "core" {
			onlyCore = false
		}
	}
	// The case worth naming. A cache is not stale by being a cache — it is the
	// normal source — but somebody editing blueprints in a checkout this build
	// cannot see is about to spend minutes generating against work they have
	// already replaced.
	if onlyCore {
		fmt.Println("    note: reading the shared cache, which tracks the published blueprints.")
		fmt.Println("          Local edits are only used from a `blueprints/` directory in this")
		fmt.Println("          tenant, or a path in WL_BLUEPRINT_SOURCES.")
	}
	fmt.Println()
}
