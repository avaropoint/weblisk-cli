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
func Locate(root string, c Component) (l Layout, found bool) {
	for _, p := range locatePlatforms {
		cand := LayoutOf(c, p)
		if marker := platformMarker(root, cand, p); marker != "" {
			return cand, true
		}
	}
	return Layout{}, false
}

// platformMarker is the file that says a component here was built for this
// platform, or "" if there is none.
func platformMarker(root string, l Layout, platform string) string {
	if len(l.Dirs) == 0 {
		return ""
	}
	home := filepath.Join(root, filepath.FromSlash(l.Home()))
	var names []string
	switch platform {
	case "cloudflare":
		names = []string{"wrangler.toml"}
	case "rust":
		names = []string{"Cargo.toml"}
	case "node":
		names = []string{"index.ts", "index.js", "package.json"}
	default:
		// go. The entry point, because platforms/go.md gives a component no
		// build manifest of its own — one module is rooted at the tenant.
		names = []string{filepath.Base(l.Entry)}
		home = filepath.Join(root, filepath.FromSlash(filepath.Dir(l.Entry)))
	}
	for _, n := range names {
		p := filepath.Join(home, n)
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
