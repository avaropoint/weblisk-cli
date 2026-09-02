package server

import (
	"fmt"
	"strings"

	"github.com/avaropoint/weblisk-cli/internal/dispatch"
)

// buildableComponents are the components a tenant can be asked to grow.
//
// The orchestrator keeps its own command because it is the tenant's first
// component and its absence means there is no tenant yet. Everything after it
// is an amendment to a tenant that already exists, which is a different act and
// reads better as one.
var buildableComponents = map[string]string{
	"content": "content repository service — custody-classified document storage",
}

// HandleComponent dispatches `weblisk component <name> <verb>`.
func HandleComponent(args []string, root string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		PrintComponentHelp()
		return nil
	}

	name := args[0]
	if _, ok := buildableComponents[name]; !ok {
		return fmt.Errorf("unknown component: %s\n  Known: %s", name, strings.Join(componentNames(), ", "))
	}
	rest := args[1:]
	if len(rest) == 0 {
		return fmt.Errorf("component %s: no verb\n  Usage: weblisk component %s init [--platform go]", name, name)
	}

	switch rest[0] {
	case "init":
		platform := "go"
		for i := 1; i < len(rest); i++ {
			switch {
			case rest[i] == "--platform" && i+1 < len(rest):
				i++
				platform = rest[i]
			case strings.HasPrefix(rest[i], "--platform="):
				platform = strings.SplitN(rest[i], "=", 2)[1]
			}
		}
		fmt.Println()
		fmt.Printf("  Weblisk Component Init — %s\n", name)
		fmt.Println()
		fmt.Printf("  Platform:  %s\n", platform)
		fmt.Printf("  AI Model:  %s\n", dispatch.DiscoverProvider())
		fmt.Println("  Mode:      amending an existing tenant")
		fmt.Println()
		// No generatedRootMarkers guard here. A component is generated INTO a
		// tenant that already has code by definition, and the manifest — keyed
		// per component — is what decides which files this build may replace.
		return dispatch.ComponentInit(root, name, platform)
	default:
		return fmt.Errorf("unknown verb for component %s: %s", name, rest[0])
	}
}

func componentNames() []string {
	var out []string
	for k := range buildableComponents {
		out = append(out, k)
	}
	return out
}

// PrintComponentHelp prints usage for the component command.
func PrintComponentHelp() {
	fmt.Println()
	fmt.Println("  Usage: weblisk component <name> init [--platform go]")
	fmt.Println()
	fmt.Println("  Components:")
	for _, n := range componentNames() {
		fmt.Printf("    %-10s %s\n", n, buildableComponents[n])
	}
	fmt.Println()
}
