package domain

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/avaropoint/weblisk-cli/internal/dispatch"
)

// Handle dispatches domain subcommands.
func Handle(args []string, root string) error {
	if len(args) == 0 {
		PrintHelp()
		return nil
	}

	switch args[0] {
	case "create":
		if len(args) < 2 {
			return fmt.Errorf("usage: weblisk domain create <name> [--platform go|cloudflare]")
		}
		return handleCreate(args[1], args[2:], root)
	case "start":
		if len(args) < 2 {
			return fmt.Errorf("usage: weblisk domain start <name>")
		}
		return handleStart(args[1], args[2:], root)
	case "help", "--help", "-h":
		PrintHelp()
		return nil
	default:
		return fmt.Errorf("unknown domain command: %s\n  Run 'weblisk domain help' for usage", args[0])
	}
}

func handleCreate(name string, args []string, root string) error {
	platform := "go"
	fromMarketplace := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--platform" && i+1 < len(args):
			i++
			platform = args[i]
		case strings.HasPrefix(args[i], "--platform="):
			platform = strings.SplitN(args[i], "=", 2)[1]
		case args[i] == "--from" && i+1 < len(args):
			i++
			if args[i] == "marketplace" {
				fromMarketplace = true
			}
		case args[i] == "--from=marketplace":
			fromMarketplace = true
		}
	}

	domainDir := filepath.Join(root, "domains", name)
	if _, err := os.Stat(domainDir); err == nil {
		return fmt.Errorf("domains/%s/ already exists\n  Remove it first or choose a different name", name)
	}

	fmt.Println()
	fmt.Println("  Weblisk Domain Create")
	fmt.Println()
	fmt.Printf("  Domain:    %s\n", name)
	fmt.Printf("  Platform:  %s\n", platform)
	if fromMarketplace {
		fmt.Println("  Source:    marketplace purchase")
	}
	fmt.Printf("  AI Model:  %s\n", dispatch.DiscoverProvider())
	fmt.Println()

	return dispatch.DomainCreate(root, name, platform)
}

func handleStart(name string, args []string, root string) error {
	domainDir := filepath.Join(root, "domains", name)

	if _, err := os.Stat(filepath.Join(domainDir, "go.mod")); err == nil {
		return startGoDomain(domainDir, name, args)
	}

	if _, err := os.Stat(filepath.Join(domainDir, "wrangler.toml")); err == nil {
		return startCFDomain(domainDir, args)
	}

	return fmt.Errorf("no domain found at domains/%s/\n  Run 'weblisk domain create %s' first", name, name)
}

func startGoDomain(dir, name string, args []string) error {
	fmt.Printf("  Building %s domain controller...\n", name)
	binaryName := "domain-" + name
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

func startCFDomain(dir string, args []string) error {
	run := exec.Command("npx", append([]string{"wrangler", "dev"}, args...)...)
	run.Dir = dir
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	run.Stdin = os.Stdin
	return run.Run()
}

func PrintHelp() {
	fmt.Print(`
  Domain Commands:
    weblisk domain create <name>   Generate a domain controller via AI
      --platform <p>               Target: go (default), cloudflare
    weblisk domain start <name>    Build and run a domain controller

`)
}
