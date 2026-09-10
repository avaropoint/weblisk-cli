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
	platform, stated := "go", false
	fromMarketplace := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--platform" && i+1 < len(args):
			i++
			platform, stated = args[i], true
		case strings.HasPrefix(args[i], "--platform="):
			platform, stated = strings.SplitN(args[i], "=", 2)[1], true
		case args[i] == "--from" && i+1 < len(args):
			i++
			if args[i] == "marketplace" {
				fromMarketplace = true
			}
		case args[i] == "--from=marketplace":
			fromMarketplace = true
		}
	}

	c := dispatch.Domain(name)
	platform, note := dispatch.PlatformFor(root, c, platform, stated)

	fmt.Println()
	fmt.Println("  Weblisk Domain Create")
	fmt.Println()
	fmt.Printf("  Domain:    %s\n", name)
	if l, found := dispatch.Locate(root, c); found {
		fmt.Printf("  Rebuilding: %s/\n", l.Home())
	}
	if note != "" {
		fmt.Println(note)
	} else {
		fmt.Printf("  Platform:  %s\n", platform)
	}
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
