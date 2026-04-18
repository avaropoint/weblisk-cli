package main

// Weblisk CLI — zero-dependency static site builder and AI agent dispatcher.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/avaropoint/weblisk-cli/internal/build"
	"github.com/avaropoint/weblisk-cli/internal/config"
	"github.com/avaropoint/weblisk-cli/internal/pro"
	"github.com/avaropoint/weblisk-cli/internal/project"
	"github.com/avaropoint/weblisk-cli/internal/serve"
	"github.com/avaropoint/weblisk-cli/internal/server"
	"github.com/avaropoint/weblisk-cli/internal/server/agent"
)

// version is set at build time via -ldflags "-X main.version=X.Y.Z"
var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		printHelp()
		return
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "new":
		name, template, local := parseNewArgs(rest)
		if name == "" {
			fatal("Usage: weblisk new <project-name> [--template blog|dashboard|docs] [--local]")
		}
		cwd, _ := os.Getwd()
		if err := project.Scaffold(name, cwd, template, local); err != nil {
			fatal("scaffold failed: %v", err)
		}

	case "dev":
		root, port := parseDevArgs(rest)
		config.Load(root)
		if err := serve.Serve(root, port); err != nil {
			fatal("dev server failed: %v", err)
		}

	case "build":
		root, opts := parseBuildArgs(rest)
		config.Load(root)
		if err := build.Build(root, opts); err != nil {
			fatal("build failed: %v", err)
		}

	case "add":
		if len(rest) < 2 {
			fatal("Usage: weblisk add <page|island> <name>")
		}
		kind, name := rest[0], rest[1]
		cwd, _ := os.Getwd()
		config.Load(cwd)
		switch kind {
		case "page":
			if err := project.AddPage(name, cwd); err != nil {
				fatal("add page failed: %v", err)
			}
		case "island":
			if err := project.AddIsland(name, cwd); err != nil {
				fatal("add island failed: %v", err)
			}
		default:
			fatal("Unknown type %q. Use: page or island", kind)
		}

	case "version", "--version", "-v":
		fmt.Printf("weblisk v%s\n", version)

	case "license", "pro":
		key, domain := pro.ParseArgs(rest)
		cwd, _ := os.Getwd()
		config.Load(cwd)
		if key == "" {
			key = os.Getenv("WL_LICENSE")
		}
		if err := pro.Activate(cwd, key, domain, version); err != nil {
			fatal("license activation failed: %v", err)
		}

	case "server":
		cwd, _ := os.Getwd()
		config.Load(cwd)
		if err := server.Handle(rest, cwd); err != nil {
			fatal("server: %v", err)
		}

	case "agent":
		cwd, _ := os.Getwd()
		config.Load(cwd)
		agentArgs, agentRoot := agent.ParseArgs(rest)
		if agentRoot == "." {
			agentRoot = cwd
		}
		if err := agent.Handle(agentArgs, agentRoot); err != nil {
			fatal("agent: %v", err)
		}

	case "update":
		cwd, _ := os.Getwd()
		config.Load(cwd)
		if err := pro.Update(cwd, version); err != nil {
			fatal("update failed: %v", err)
		}

	case "vendor":
		dest := "lib/weblisk"
		for i := 0; i < len(rest); i++ {
			a := rest[i]
			if a == "--dest" && i+1 < len(rest) {
				i++
				dest = rest[i]
			} else if strings.HasPrefix(a, "--dest=") {
				dest = strings.SplitN(a, "=", 2)[1]
			}
		}
		cwd, _ := os.Getwd()
		if err := project.Vendor(cwd, dest); err != nil {
			fatal("vendor failed: %v", err)
		}

	case "help", "--help", "-h":
		printHelp()

	default:
		fmt.Fprintf(os.Stderr, "  Unknown command: %s\n\n", cmd)
		printHelp()
		os.Exit(1)
	}
}

// Arg parsers

func parseNewArgs(args []string) (string, string, bool) {
	var name, template string
	template = "default"
	local := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--local" {
			local = true
		} else if strings.HasPrefix(a, "--template") {
			if strings.Contains(a, "=") {
				template = strings.SplitN(a, "=", 2)[1]
			} else if i+1 < len(args) {
				i++
				template = args[i]
			}
		} else if !strings.HasPrefix(a, "-") {
			name = a
		}
	}
	return name, template, local
}

func parseBuildArgs(args []string) (string, build.Options) {
	root := "."
	var opts build.Options
	for _, a := range args {
		switch a {
		case "--minify":
			opts.Minify = true
		case "--fingerprint":
			opts.Fingerprint = true
		default:
			if !strings.HasPrefix(a, "-") {
				root = a
			}
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return abs, opts
}

func parseDevArgs(args []string) (string, int) {
	root := "."
	port := config.Resolve().Port
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--port" && i+1 < len(args) {
			i++
			if p, err := strconv.Atoi(args[i]); err == nil {
				port = p
			}
		} else if strings.HasPrefix(a, "--port=") {
			if p, err := strconv.Atoi(strings.SplitN(a, "=", 2)[1]); err == nil {
				port = p
			}
		} else if !strings.HasPrefix(a, "-") {
			root = a
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return abs, port
}

// Help text

func printHelp() {
	fmt.Print(`
  Weblisk CLI v` + version + `

  Usage:
    weblisk <command> [options]

  Commands:
    new <name>          Scaffold a new Weblisk project
      --template <t>    Starter template: blog, dashboard, docs (default: basic)
      --local           Include framework files locally instead of CDN
    dev [root]          Start a local dev server
      --port <n>        Port number (default: 3000)
    add page <name>     Add a new HTML page
    add island <name>   Add an island script
    build [root]        Copy site to dist/ for deployment
      --minify          Minify HTML, CSS, and JS
      --fingerprint     Hash asset filenames for cache busting

  Agent System:
    server init         Generate orchestrator code via AI
      --platform <p>    Target: local-go (default), cloudflare
    server start        Build and start the generated orchestrator
      --port <n>        Orchestrator port (default: 9800)
    server verify       Verify a running orchestrator against protocol spec
      --url <url>       Orchestrator URL (default: http://localhost:9800)
    server status       Show AI provider configuration

    agent create <n>    Generate an agent via AI
      --platform <p>    Target: local-go (default), cloudflare
    agent start <name>  Build and start a generated agent
      --port <n>        Agent port (default: auto-assigned)
      --orch <url>      Orchestrator URL (default: http://localhost:9800)
    agent verify <name> Verify a running agent against protocol spec
      --url <url>       Agent URL (default: auto-detect)
    agent list          Show generated agents and available blueprints

    license             Register a pro license and download pro modules
      --key <key>       License key (or set WL_LICENSE in .env)
      --domain <d>      Register domain for CDN-mode pro module serving
    update              Re-download framework + pro modules (--local projects)
    vendor              Download framework files into any existing project
      --dest <path>     Destination directory (default: lib/weblisk)
    version             Print version
    help                Show this help message

  Environment (.env):
    WL_ORIGIN       Production origin URL (for sitemap)
    WL_PORT         Dev server port (default: 3000)
    WL_DIST         Output directory (default: dist)
    WL_CDN          CDN base URL (rewrites importmaps on build)
    WL_LICENSE      Pro license key (optional)
    WL_AI_PROVIDER  AI backend: openai, ollama, anthropic, cloudflare
    WL_AI_MODEL     Model name (provider-specific default)
    WL_AI_BASE_URL  Endpoint override
    WL_AI_KEY       API key

  The CLI carries the blueprint — your AI model writes the code.
  Configure WL_AI_PROVIDER to get started with agent generation.
  No Node.js required. No package manager. No build step.
`)
}

// ── Helpers ─────────────────────────────────────────────────

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "  Error: "+format+"\n", a...)
	os.Exit(1)
}
