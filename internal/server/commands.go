package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/avaropoint/weblisk-cli/internal/dispatch"
	"github.com/avaropoint/weblisk-cli/internal/protocol"
)

// Handle dispatches server subcommands.
func Handle(args []string, root string) error {
	if len(args) == 0 {
		PrintHelp()
		return nil
	}

	switch args[0] {
	case "init":
		return handleInit(args[1:], root)
	case "start":
		return handleStart(args[1:], root)
	case "verify":
		return handleVerify(args[1:])
	case "status":
		return handleStatus()
	case "help", "--help", "-h":
		PrintHelp()
		return nil
	default:
		return fmt.Errorf("unknown server command: %s\n  Run 'weblisk server help' for usage", args[0])
	}
}

func handleInit(args []string, root string) error {
	platform := "local-go"
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--platform" && i+1 < len(args):
			i++
			platform = args[i]
		case strings.HasPrefix(args[i], "--platform="):
			platform = strings.SplitN(args[i], "=", 2)[1]
		}
	}

	serverDir := filepath.Join(root, "server")
	if _, err := os.Stat(serverDir); err == nil {
		return fmt.Errorf("server/ directory already exists at %s\n  Remove it first or use a different directory", serverDir)
	}

	fmt.Println()
	fmt.Println("  Weblisk Server Init")
	fmt.Println()
	fmt.Printf("  Platform:  %s\n", platform)
	fmt.Printf("  AI Model:  %s\n", dispatch.DiscoverProvider())
	fmt.Println()

	return dispatch.ServerInit(root, platform)
}

func handleStart(args []string, root string) error {
	serverDir := filepath.Join(root, "server")

	if _, err := os.Stat(filepath.Join(serverDir, "go.mod")); err == nil {
		return startGoServer(serverDir, args)
	}

	if _, err := os.Stat(filepath.Join(serverDir, "wrangler.toml")); err == nil {
		return startCFServer(serverDir, args)
	}

	return fmt.Errorf("no server found in %s\n  Run 'weblisk server init' first", serverDir)
}

func startGoServer(dir string, args []string) error {
	fmt.Println("  Building server...")
	build := exec.Command("go", "build", "-o", "server", ".")
	build.Dir = dir
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	cmdArgs := append([]string{filepath.Join(dir, "server")}, args...)
	run := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	run.Stdin = os.Stdin
	return run.Run()
}

func startCFServer(dir string, args []string) error {
	run := exec.Command("npx", append([]string{"wrangler", "dev"}, args...)...)
	run.Dir = dir
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	run.Stdin = os.Stdin
	return run.Run()
}

func handleVerify(args []string) error {
	url := "http://localhost:9800"
	for i := 0; i < len(args); i++ {
		if args[i] == "--url" && i+1 < len(args) {
			i++
			url = args[i]
		} else if strings.HasPrefix(args[i], "--url=") {
			url = strings.SplitN(args[i], "=", 2)[1]
		}
	}
	return protocol.VerifyOrchestrator(url)
}

func handleStatus() error {
	fmt.Println()
	fmt.Println("  Weblisk Server Status")
	fmt.Println()
	dispatch.PrintProviderStatus()
	return nil
}

// ParsePort extracts the server port from args and environment.
func ParsePort(args []string) int {
	port := 9800
	if p := os.Getenv("WL_ORCH_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "--port" && i+1 < len(args) {
			i++
			if n, err := strconv.Atoi(args[i]); err == nil {
				port = n
			}
		}
	}
	return port
}

// PrintHelp shows server command usage.
func PrintHelp() {
	fmt.Print(`
  Weblisk Server

  Usage:
    weblisk server init [--platform local-go|cloudflare]
      Generate orchestrator code using your AI model.
      The AI model builds the implementation from the protocol blueprint.

    weblisk server start [--port N]
      Build and run the generated orchestrator.

    weblisk server verify [--url URL]
      Test a running orchestrator against the protocol specification.
      Default URL: http://localhost:9800

    weblisk server status
      Show AI provider configuration and readiness.

  Platforms:
    local-go     Go binary, runs locally (default)
    cloudflare   Cloudflare Worker, edge deployment

  Environment:
    WL_AI_PROVIDER   AI backend (ollama, openai, anthropic, cloudflare)
    WL_AI_MODEL      Model name
    WL_AI_KEY        API key (if required)
    WL_ORCH_PORT     Orchestrator port (default: 9800)

  Workflow:
    1. Configure AI:    export WL_AI_PROVIDER=ollama WL_AI_MODEL=llama3
    2. Generate server: weblisk server init
    3. Start server:    weblisk server start
    4. Verify:          weblisk server verify

`)
}
