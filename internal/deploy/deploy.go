package deploy

// Deploy commands — rollback and deployment management.

import (
	"fmt"
	"strings"

	"github.com/avaropoint/weblisk-cli/internal/admin"
)

// Handle dispatches deploy subcommands.
func Handle(args []string) error {
	if len(args) == 0 {
		PrintHelp()
		return nil
	}

	switch args[0] {
	case "rollback":
		return handleRollback(args[1:])
	case "help", "--help", "-h":
		PrintHelp()
		return nil
	default:
		return fmt.Errorf("unknown deploy command: %s\n  Try: weblisk deploy rollback", args[0])
	}
}

func handleRollback(args []string) error {
	env := ""
	version := ""

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--env" && i+1 < len(args):
			i++
			env = args[i]
		case strings.HasPrefix(args[i], "--env="):
			env = strings.SplitN(args[i], "=", 2)[1]
		case args[i] == "--version" && i+1 < len(args):
			i++
			version = args[i]
		case strings.HasPrefix(args[i], "--version="):
			version = strings.SplitN(args[i], "=", 2)[1]
		}
	}

	if env == "" {
		return fmt.Errorf("--env is required.\n  Usage: weblisk deploy rollback --env production [--version X.Y.Z]")
	}

	client, err := admin.NewClient(args)
	if err != nil {
		return err
	}

	payload := map[string]string{
		"environment": env,
	}
	if version != "" {
		payload["version"] = version
	}

	fmt.Printf("Rolling back %s", env)
	if version != "" {
		fmt.Printf(" to v%s", version)
	} else {
		fmt.Print(" to previous version")
	}
	fmt.Println("...")

	_, err = client.Post("/v1/admin/deploy/rollback", payload)
	if err != nil {
		return err
	}

	fmt.Println("✓ Rollback complete.")
	return nil
}

// PrintHelp prints deploy command usage.
func PrintHelp() {
	fmt.Print(`
  Deploy Commands:
    weblisk deploy rollback         Roll back a deployed environment
      --env <name>                  Target environment (required)
      --version <v>                 Specific version to roll back to (default: previous)

`)
}
