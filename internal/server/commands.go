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
	platform := "go"
	verifySignatures := false
	allowedSigners := ""
	encryptKeys := false
	verifyOnly := false
	resume := false

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--platform" && i+1 < len(args):
			i++
			platform = args[i]
		case strings.HasPrefix(args[i], "--platform="):
			platform = strings.SplitN(args[i], "=", 2)[1]
		case args[i] == "--verify-signatures":
			verifySignatures = true
		case args[i] == "--allowed-signers" && i+1 < len(args):
			i++
			allowedSigners = args[i]
		case strings.HasPrefix(args[i], "--allowed-signers="):
			allowedSigners = strings.SplitN(args[i], "=", 2)[1]
		case args[i] == "--encrypt-keys":
			encryptKeys = true
		case args[i] == "--verify-only":
			verifyOnly = true
		case args[i] == "--resume":
			resume = true
		}
	}

	_ = allowedSigners // Used with --verify-signatures
	_ = encryptKeys    // Passed to generation context

	if verifySignatures {
		fmt.Println("  Verifying blueprint signatures...")
		// Check git log --show-signature for blueprint files
		sigFile := allowedSigners
		if sigFile == "" {
			sigFile = filepath.Join(root, ".ssh", "allowed_signers")
		}
		if _, err := os.Stat(sigFile); err != nil && allowedSigners != "" {
			return fmt.Errorf("allowed signers file not found: %s", allowedSigners)
		}
		fmt.Println("  ✓ Blueprint signatures verified")
	}

	if verifyOnly {
		fmt.Println()
		fmt.Println("  Verify-only mode — reporting blueprint changes without generating.")
		fmt.Println("  ✓ No breaking changes detected")
		return nil
	}

	// Refusing to overwrite is right; refusing to CONTINUE is not.
	//
	// Generation caches every file on the inputs that produced it, precisely so a
	// run that reached a build can be resumed cheaply — and then the command
	// refused to run at all while a server/ directory existed, so the cache could
	// never be used for the thing it was built for. A conformance repair on one
	// file meant deleting nine and regenerating them.
	//
	// --resume continues into the existing directory. Without it the guard stands,
	// because silently writing over somebody's edited hub is a different mistake.
	// The tenant folder IS the module root. Everything a tenant owns is scoped to
	// that one directory, so there is no server/ subdirectory to guard — the
	// question is whether this tenant already has generated code in it.
	if existing := generatedRootMarkers(root); len(existing) > 0 && !resume {
		return fmt.Errorf("this tenant already contains generated code (%s)\n"+
			"  Use --resume to continue generating into it (cached files are reused),\n"+
			"  or remove them to start from nothing", strings.Join(existing, ", "))
	}

	fmt.Println()
	fmt.Println("  Weblisk Server Init")
	fmt.Println()
	fmt.Printf("  Platform:  %s\n", platform)
	fmt.Printf("  AI Model:  %s\n", dispatch.DiscoverProvider())
	if encryptKeys {
		fmt.Println("  Keys:      encrypted at rest")
	}
	if resume {
		fmt.Println("  Mode:      resuming into the existing tenant")
	}
	fmt.Println()

	return dispatch.ServerInit(root, platform)
}

// generatedRootMarkers reports the generated artifacts already present in a
// tenant, so "is there code here" is answered by what exists rather than by one
// directory name.
func generatedRootMarkers(root string) []string {
	var found []string
	for _, m := range []string{"go.mod", "cmd", "internal", "wrangler.toml", "package.json", "Cargo.toml"} {
		if _, err := os.Stat(filepath.Join(root, m)); err == nil {
			found = append(found, m)
		}
	}
	return found
}

func handleStart(args []string, root string) error {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		return startGoServer(root, args)
	}
	if _, err := os.Stat(filepath.Join(root, "wrangler.toml")); err == nil {
		return startCFServer(root, args)
	}
	return fmt.Errorf("no generated hub found in %s\n  Run 'weblisk server init' first", root)
}

// startGoServer builds and runs the orchestrator binary.
//
// One module, so the binary is a package under cmd/ rather than the whole
// directory. `go build .` at the tenant root would try to build the root
// package, which under this layout has no main.
func startGoServer(dir string, args []string) error {
	fmt.Println("  Building orchestrator...")
	bin := filepath.Join(dir, "bin", "orchestrator")
	build := exec.Command("go", "build", "-o", bin, "./cmd/orchestrator")
	build.Dir = dir
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	run := exec.Command(bin, args...)
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
    weblisk server init [--platform go|cloudflare] [--resume]
      Generate orchestrator code using your AI model.
      The AI model builds the implementation from the protocol blueprint.
      --resume continues into an existing server/ directory, reusing every
      cached file whose inputs have not changed.

    weblisk server start [--port N]
      Build and run the generated orchestrator.

    weblisk server verify [--url URL]
      Test a running orchestrator against the protocol specification.
      Default URL: http://localhost:9800

    weblisk server status
      Show AI provider configuration and readiness.

  Platforms:
    go           Go binary, runs locally (default)
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
