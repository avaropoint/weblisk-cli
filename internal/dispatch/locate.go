package dispatch

// locate.go — finding a component that has already been generated.
//
// `agent start billing` and `agent list` read `agents/billing/` from disk and
// looked for a go.mod in it. That was right for the single-shot generator,
// which wrote every agent as a self-contained module. It is wrong for the plan
// pipeline on go and node, where platforms/go.md puts an agent at cmd/<name>
// plus internal/agents/<name> and gives it no module of its own.
//
// Left alone, the switch to the pipeline would have generated agents that build
// and cannot be started or listed — the tooling reporting nothing while the
// code was there. So WHERE a component lives is asked of the layout here too,
// not only when planning it.
//
// The platform is not recorded anywhere at generation time, so it is read back
// off the disk: each platform's layout says where its component would be and
// what marks it, and the first that matches is the one it was built for. The
// markers do not overlap — a cloudflare agent is agents/<name>/wrangler.toml, a
// rust one agents/<name>/Cargo.toml, a go one cmd/<name>/main.go.

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// locatePlatforms is the order platforms are tried in when reading a component
// back off the disk. Contained platforms first: their markers are files they
// alone have, and go's marker is only an entry point.
var locatePlatforms = []string{"cloudflare", "rust", "node", "go"}

// Locate reports where a generated component lives, and which platform it was
// generated for. Found is false when nothing on disk answers to it.
//
// Three questions in order, weakest evidence last:
//
//  1. What did generation RECORD? The manifest names the platform, so no
//     probing is needed and no marker can be misread.
//  2. Failing that, which platform's layout has its marker on disk?
//  3. Failing that, where do the RECORDED FILES actually sit? A component is
//     told where to go and may not comply, and no platform blueprint states a
//     directory for a gateway at all — so the layout is the CLI's convention
//     there, and a gateway placed anywhere else would otherwise generate
//     cleanly and then be invisible to `gateway start`, `agent list` and
//     `weblisk validate`. The record is what actually happened.
func Locate(root string, c Component) (Layout, bool) {
	m, recorded := readManifest(manifestName(root, c.Key()))

	// 1. The platform generation recorded.
	if recorded && m.Platform != "" {
		l := LayoutOf(c, m.Platform)
		if platformMarker(root, l, m.Platform) != "" {
			return l, true
		}
	}
	// 2. Whichever layout answers on disk.
	for _, p := range locatePlatforms {
		cand := LayoutOf(c, p)
		if platformMarker(root, cand, p) != "" {
			return cand, true
		}
	}
	// 3. Where its files actually are.
	if recorded {
		if l, ok := locateByRecord(root, c, m); ok {
			return l, true
		}
	}
	return Layout{}, false
}

// locateByRecord finds a component from the files generation recorded writing.
//
// Used only when the layout does not answer. The entry point is identified by
// name — main.go, wrangler.toml, Cargo.toml, index.ts — because that is the one
// thing about a component's shape that every platform blueprint does state,
// even where it states no directory.
func locateByRecord(root string, c Component, m writtenManifest) (Layout, bool) {
	type mark struct{ base, platform string }
	marks := []mark{
		{"wrangler.toml", "cloudflare"}, {"Cargo.toml", "rust"},
		{"main.go", "go"}, {"main.rs", "rust"},
		{"index.ts", "node"}, {"index.js", "node"},
		{"server.ts", "node"}, {"server.js", "node"},
	}
	for _, want := range marks {
		if m.Platform != "" && m.Platform != want.platform {
			continue // the record already said which platform; believe it
		}
		for _, rel := range m.Files {
			clean := path.Clean(filepath.ToSlash(rel))
			if path.Base(clean) != want.base {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(clean))); err != nil {
				continue // recorded, and since removed
			}
			l := LayoutOf(c, want.platform)
			// Keep everything the platform decides — families, siblings,
			// whether it carries its own build manifest — and correct only
			// WHERE this one turned out to be.
			home := path.Dir(clean)
			if want.platform == "cloudflare" || want.platform == "rust" {
				l.Entry = path.Join(home, "src", map[string]string{
					"cloudflare": "index.js", "rust": "main.rs"}[want.platform])
			} else {
				l.Entry = clean
			}
			l.Dirs = append([]string{home}, keepExisting(root, l.Dirs, home)...)
			return l, true
		}
	}
	return Layout{}, false
}

// keepExisting returns the layout's own directories that are on disk and are
// not the one already found, so a Go component keeps internal/agents/<name>
// alongside the cmd/ directory its entry point named.
func keepExisting(root string, dirs []string, skip string) []string {
	var out []string
	for _, d := range dirs {
		if d == skip {
			continue
		}
		if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(d))); err == nil && fi.IsDir() {
			out = append(out, d)
		}
	}
	return out
}

// platformMarker is the file that says a component here was built for this
// platform, or "" if there is none.
//
// The split is Layout.Contained, which is the same distinction the plan rules
// read: a platform that gives each component its own build manifest is
// identified by that manifest, and one that does not is identified by the file
// its process starts at.
//
// Not "a file inside Home()": the node orchestrator's entry point is
// src/server.ts, which sits OUTSIDE src/orchestrator/, and looking only inside
// the directory would have failed to find one that had been generated
// correctly. That is the same shape as the ownership bug Layout.Owns carries a
// note about.
func platformMarker(root string, l Layout, platform string) string {
	if len(l.Dirs) == 0 {
		return ""
	}
	var rels []string
	switch {
	case platform == "cloudflare":
		rels = []string{path.Join(l.Home(), "wrangler.toml")}
	case platform == "rust":
		rels = []string{path.Join(l.Home(), "Cargo.toml")}
	case l.Entry != "":
		// go and node. The entry point, because neither gives a component a
		// build manifest of its own — one module, or one project, is rooted at
		// the tenant.
		rels = []string{l.Entry}
		// A TypeScript project may have been emitted as JavaScript. The layout
		// names one extension because a prompt has to name one; finding the
		// component afterwards should not depend on which was chosen.
		if ext := path.Ext(l.Entry); ext == ".ts" {
			rels = append(rels, strings.TrimSuffix(l.Entry, ext)+".js")
		}
	}
	for _, rel := range rels {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// GeneratedComponents lists what this tenant's manifests record it as having.
//
// Read from the manifests rather than by scanning directories, because the
// manifest names its owner exactly — "agent:billing" — while a directory scan
// has to guess which of cmd/'s entries is an agent and which is the
// orchestrator. Sorted, so two runs report the same order.
func GeneratedComponents(root string) []Component {
	entries, err := os.ReadDir(filepath.Join(root, cacheDirName))
	if err != nil {
		return nil
	}
	var out []Component
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "written-") {
			continue
		}
		owner, _ := readManifestOwner(filepath.Join(root, cacheDirName, e.Name()))
		if owner == "" {
			continue
		}
		out = append(out, ParseKey(owner))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// ParseKey is Component.Key's inverse.
func ParseKey(key string) Component {
	if kind, name, found := strings.Cut(key, ":"); found {
		return Component{Kind: kind, Name: name}
	}
	return Component{Kind: key}
}

// GeneratedOfKind lists the generated components of one kind.
func GeneratedOfKind(root, kind string) []Component {
	var out []Component
	for _, c := range GeneratedComponents(root) {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// StartComponent builds and runs a generated component.
//
// One implementation for agent, domain and gateway. They had three, each
// hardcoding `<kind>s/<name>/` with a go.mod in it — the shape the single-shot
// generator wrote and the shape platforms/go.md explicitly argues against.
func StartComponent(root string, c Component, args []string) error {
	l, found := Locate(root, c)
	if !found {
		return fmt.Errorf("no generated %s found\n  Run 'weblisk %s' first",
			c.Label(), createHint(c))
	}
	home := filepath.Join(root, filepath.FromSlash(l.Home()))
	switch l.Platform {
	case "cloudflare":
		return runIn(home, "npx", append([]string{"wrangler", "dev"}, args...)...)
	case "rust":
		return runIn(root, "cargo", append([]string{"run", "-p", l.Name}, args...)...)
	case "go":
		// Built from the tenant root, which is where the module is. The binary
		// is bin/<name>, the path platforms/go.md states for it.
		self := c.Name
		if self == "" {
			self = c.Kind
		}
		bin := filepath.Join("bin", self)
		fmt.Printf("  Building %s...\n", c.Label())
		if err := runIn(root, "go", "build", "-o", bin, "./"+filepath.Dir(l.Entry)); err != nil {
			return fmt.Errorf("build failed: %w", err)
		}
		return runIn(root, filepath.Join(root, bin), args...)
	}
	return fmt.Errorf("%s was generated for the %s platform, and starting one is not wired up\n"+
		"  Its files are in %s", c.Label(), l.Platform, l.Home())
}

func createHint(c Component) string {
	if c.Name != "" {
		return c.Kind + " create " + c.Name
	}
	return c.Kind + " create"
}

func runIn(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// PlatformFor settles which platform a component is generated for, and returns
// a line to print when that is not simply what was asked for.
//
// `weblisk agent create billing` defaults to go. Run against an agent that was
// generated for cloudflare, the default silently rebuilt it as a Go binary —
// and because the manifest names the cloudflare files, reconcile then DELETED
// them. A platform migration is a real thing to want and not a thing to do by
// omission, so an unstated platform now continues whatever the component was
// generated for, and a stated one that differs says what it is about to do.
func PlatformFor(root string, c Component, requested string, stated bool) (platform, note string) {
	m, ok := readManifest(manifestName(root, c.Key()))
	was := ""
	if ok {
		was = m.Platform
	}
	switch {
	case was == "" || was == requested:
		return requested, ""
	case !stated:
		return was, fmt.Sprintf("  Platform:  %s — continuing what this %s was generated for.\n"+
			"             Pass --platform %s to move it.", was, c.Kind, requested)
	default:
		return requested, fmt.Sprintf("  [warn] this %s was generated for %s and is being rebuilt for %s.\n"+
			"         Its %s files are recorded in its manifest and will be removed.",
			c.Kind, was, requested, was)
	}
}
