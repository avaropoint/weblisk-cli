package domain

import (
	"fmt"
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

	if l, found := dispatch.Locate(root, dispatch.Domain(name)); found {
		fmt.Printf("  Rebuilding the %s domain controller in %s/\n", name, l.Home())
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

	if err := dispatch.DomainCreate(root, name, platform); err != nil {
		return err
	}
	dispatch.InstallAndNoteSkills(root, platform, "domain")
	return nil
}

func handleStart(name string, args []string, root string) error {
	return dispatch.StartComponent(root, dispatch.Domain(name), args)
}

func PrintHelp() {
	fmt.Print(`
  Domain Commands:
    weblisk domain create <name>   Generate a domain controller via AI
      --platform <p>               Target: go (default), cloudflare
    weblisk domain start <name>    Build and run a domain controller

`)
}
