package gateway

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/avaropoint/weblisk-cli/internal/dispatch"
)

// Handle dispatches gateway subcommands.
func Handle(args []string, root string) error {
	if len(args) == 0 {
		PrintHelp()
		return nil
	}

	switch args[0] {
	case "create":
		return handleCreate(args[1:], root)
	case "start":
		return handleStart(args[1:], root)
	case "help", "--help", "-h":
		PrintHelp()
		return nil
	default:
		return fmt.Errorf("unknown gateway command: %s\n  Run 'weblisk gateway help' for usage", args[0])
	}
}

func handleCreate(args []string, root string) error {
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

	gatewayDir := filepath.Join(root, "gateway")
	if _, err := os.Stat(gatewayDir); err == nil {
		return fmt.Errorf("gateway/ already exists\n  Remove it first to regenerate")
	}

	fmt.Println()
	fmt.Println("  Weblisk Gateway Create")
	fmt.Println()
	fmt.Printf("  Platform:  %s\n", platform)
	fmt.Printf("  AI Model:  %s\n", dispatch.DiscoverProvider())
	fmt.Println()

	return dispatch.GatewayCreate(root, platform)
}

func handleStart(args []string, root string) error {
	gatewayDir := filepath.Join(root, "gateway")

	if _, err := os.Stat(filepath.Join(gatewayDir, "go.mod")); err == nil {
		return startGoGateway(gatewayDir, args)
	}

	if _, err := os.Stat(filepath.Join(gatewayDir, "wrangler.toml")); err == nil {
		return startCFGateway(gatewayDir, args)
	}

	return fmt.Errorf("no gateway found in gateway/\n  Run 'weblisk gateway create' first")
}

func startGoGateway(dir string, args []string) error {
	fmt.Println("  Building gateway...")
	build := exec.Command("go", "build", "-o", "gateway", ".")
	build.Dir = dir
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	cmdArgs := append([]string{filepath.Join(dir, "gateway")}, args...)
	run := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	run.Stdin = os.Stdin
	return run.Run()
}

func startCFGateway(dir string, args []string) error {
	run := exec.Command("npx", append([]string{"wrangler", "dev"}, args...)...)
	run.Dir = dir
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	run.Stdin = os.Stdin
	return run.Run()
}

func PrintHelp() {
	fmt.Print(`
  Gateway Commands:
    weblisk gateway create         Generate the application gateway via AI
      --platform <p>               Target: go (default), cloudflare
    weblisk gateway start          Build and run the application gateway

`)
}
