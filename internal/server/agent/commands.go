package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/avaropoint/weblisk-cli/internal/dispatch"
	"github.com/avaropoint/weblisk-cli/internal/protocol"
)

// Handle dispatches agent subcommands.
func Handle(args []string, root string) error {
	if len(args) == 0 {
		PrintHelp()
		return nil
	}

	switch args[0] {
	case "create":
		if len(args) < 2 {
			return fmt.Errorf("usage: weblisk agent create <name> [--platform go|cloudflare]")
		}
		return handleCreate(args[1], args[2:], root)
	case "start":
		if len(args) < 2 {
			return fmt.Errorf("usage: weblisk agent start <name> [--port N] [--orch URL]")
		}
		return handleStart(args[1], args[2:], root)
	case "verify":
		return handleVerify(args[1:])
	case "list":
		return handleList(root)
	case "help", "--help", "-h":
		PrintHelp()
		return nil
	default:
		return fmt.Errorf("unknown agent command: %s\n  Run 'weblisk agent help' for usage", args[0])
	}
}

func handleCreate(name string, args []string, root string) error {
	platform := "go"
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--platform" && i+1 < len(args):
			i++
			platform = args[i]
		case strings.HasPrefix(args[i], "--platform="):
			platform = strings.SplitN(args[i], "=", 2)[1]
		}
	}

	agentDir := filepath.Join(root, "agents", name)
	if _, err := os.Stat(agentDir); err == nil {
		return fmt.Errorf("agents/%s/ already exists\n  Remove it first or choose a different name", name)
	}

	fmt.Println()
	fmt.Println("  Weblisk Agent Create")
	fmt.Println()
	fmt.Printf("  Agent:     %s\n", name)
	fmt.Printf("  Platform:  %s\n", platform)
	fmt.Printf("  AI Model:  %s\n", dispatch.DiscoverProvider())
	fmt.Println()

	return dispatch.AgentCreate(root, name, platform)
}

func handleStart(name string, args []string, root string) error {
	agentDir := filepath.Join(root, "agents", name)

	if _, err := os.Stat(filepath.Join(agentDir, "go.mod")); err == nil {
		return startGoAgent(agentDir, name, args)
	}

	if _, err := os.Stat(filepath.Join(agentDir, "wrangler.toml")); err == nil {
		return startCFAgent(agentDir, args)
	}

	return fmt.Errorf("no agent found at agents/%s/\n  Run 'weblisk agent create %s' first", name, name)
}

func startGoAgent(dir, name string, args []string) error {
	fmt.Printf("  Building %s agent...\n", name)
	binaryName := "agent-" + name
	build := exec.Command("go", "build", "-o", binaryName, ".")
	build.Dir = dir
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	cmdArgs := append([]string{filepath.Join(dir, binaryName)}, args...)
	run := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	run.Stdin = os.Stdin
	return run.Run()
}

func startCFAgent(dir string, args []string) error {
	run := exec.Command("npx", append([]string{"wrangler", "dev"}, args...)...)
	run.Dir = dir
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	run.Stdin = os.Stdin
	return run.Run()
}

func handleVerify(args []string) error {
	url := ""
	name := ""

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--url" && i+1 < len(args):
			i++
			url = args[i]
		case strings.HasPrefix(args[i], "--url="):
			url = strings.SplitN(args[i], "=", 2)[1]
		default:
			if name == "" {
				name = args[i]
			}
		}
	}

	if url == "" {
		if name != "" {
			port := Port(name)
			url = fmt.Sprintf("http://localhost:%d", port)
		} else {
			return fmt.Errorf("usage: weblisk agent verify <name> [--url URL]")
		}
	}

	return protocol.VerifyAgent(url)
}

func handleList(root string) error {
	fmt.Println()
	fmt.Println("  Agents")
	fmt.Println()

	agentsDir := filepath.Join(root, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err == nil && len(entries) > 0 {
		fmt.Println("  Generated agents:")
		for _, e := range entries {
			if e.IsDir() {
				platform := detectPlatform(filepath.Join(agentsDir, e.Name()))
				fmt.Printf("    %-15s  [%s]  agents/%s/\n", e.Name(), platform, e.Name())
			}
		}
		fmt.Println()
	} else {
		fmt.Println("  No agents generated yet.")
		fmt.Println()
	}

	fmt.Println("  Available domain blueprints:")
	domains := []struct {
		name string
		desc string
	}{
		{"seo", "Scan and optimize SEO across all HTML files"},
		{"a11y", "Review accessibility compliance"},
		{"perf", "Analyze performance and suggest optimizations"},
		{"security", "Scan for security vulnerabilities"},
	}
	for _, d := range domains {
		fmt.Printf("    %-15s  %s\n", d.name, d.desc)
	}
	fmt.Println()

	fmt.Println("  Create an agent:  weblisk agent create <name> [--platform go|cloudflare]")
	fmt.Println("  Start an agent:   weblisk agent start <name> [--orch URL]")
	fmt.Println("  Custom agents:    Any program implementing the Weblisk Agent Protocol")
	fmt.Println()
	return nil
}

func detectPlatform(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return "go"
	}
	if _, err := os.Stat(filepath.Join(dir, "wrangler.toml")); err == nil {
		return "cloudflare"
	}
	if _, err := os.Stat(filepath.Join(dir, "Cargo.toml")); err == nil {
		return "rust"
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		return "node"
	}
	return "unknown"
}

// Port deterministically assigns a port to an agent name.
func Port(name string) int {
	ports := map[string]int{
		"seo": 9710, "a11y": 9711, "perf": 9712, "security": 9713,
		"workflow": 9714, "task": 9715, "scheduler": 9716,
	}
	if p, ok := ports[name]; ok {
		return p
	}
	h := 0
	for _, c := range name {
		h = h*31 + int(c)
	}
	return 9720 + (h % 80)
}

// ParseArgs extracts agent args and root from the CLI args.
func ParseArgs(args []string) ([]string, string) {
	root := "."
	var clean []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--root" && i+1 < len(args) {
			i++
			root = args[i]
		} else {
			clean = append(clean, args[i])
		}
	}
	return clean, root
}

// PrintHelp shows agent command usage.
func PrintHelp() {
	fmt.Print(`
  Weblisk Agent

  Usage:
    weblisk agent create <name> [--platform go|cloudflare]
      Generate agent code using your AI model.
      Uses domain blueprint if available (seo, a11y, perf, security).
      Custom agents get the universal agent blueprint.

    weblisk agent start <name> [--port N] [--orch URL]
      Build and run a generated agent.

    weblisk agent verify <name> [--url URL]
      Test a running agent against the protocol specification.

    weblisk agent list
      List generated agents and available domain blueprints.

  Platforms:
    go           Go binary, runs locally (default)
    cloudflare   Cloudflare Worker, edge deployment

  Workflow:
    1. Create agent:  weblisk agent create seo
    2. Start agent:   weblisk agent start seo --orch http://localhost:9800
    3. Verify:        weblisk agent verify seo

  Custom agents:
    Any program that implements the Weblisk Agent Protocol can register
    with the orchestrator. The protocol uses HTTP + JSON with Ed25519
    authentication. See: weblisk.dev/docs/agents

`)
}
