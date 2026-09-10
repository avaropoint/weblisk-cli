package gateway

import (
	"fmt"
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

	if l, found := dispatch.Locate(root, dispatch.Gateway()); found {
		fmt.Printf("  Rebuilding the gateway in %s/\n", l.Home())
	}

	fmt.Println()
	fmt.Println("  Weblisk Gateway Create")
	fmt.Println()
	fmt.Printf("  Platform:  %s\n", platform)
	fmt.Printf("  AI Model:  %s\n", dispatch.DiscoverProvider())
	fmt.Println()

	if err := dispatch.GatewayCreate(root, platform); err != nil {
		return err
	}
	dispatch.InstallAndNoteSkills(root, platform, "gateway")
	return nil
}

func handleStart(args []string, root string) error {
	return dispatch.StartComponent(root, dispatch.Gateway(), args)
}

func PrintHelp() {
	fmt.Print(`
  Gateway Commands:
    weblisk gateway create         Generate the application gateway via AI
      --platform <p>               Target: go (default), cloudflare
    weblisk gateway start          Build and run the application gateway

`)
}
