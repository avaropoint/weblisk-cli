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
// Descriptions only. WHICH components exist is read from the corpus by
// dispatch.BuildableComponents — a blueprint stating an HTTP surface and its
// bindings IS a buildable component, and needing a Go edit as well meant the
// corpus could describe something the tool refused to build.
var componentDescriptions = map[string]string{
	"content":      "content repository service — custody-classified document storage",
	"orchestrator": "the trust anchor and agent registry (see `weblisk server init`)",
}

// HandleComponent dispatches `weblisk component <name> <verb>`.
func HandleComponent(args []string, root string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		PrintComponentHelpFor(root)
		return nil
	}

	name := args[0]
	known := componentNames(root)
	if !containsStr(known, name) {
		return fmt.Errorf("unknown component: %s\n  Buildable from this installation's blueprints: %s",
			name, strings.Join(known, ", "))
	}
	if name == "orchestrator" {
		return fmt.Errorf("the orchestrator is a tenant's first component, not an amendment to one\n" +
			"  Use: weblisk server init")
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
		return dispatch.SupervisedComponentInit(root, name, platform)
	default:
		return fmt.Errorf("unknown verb for component %s: %s", name, rest[0])
	}
}

// componentNames reads the buildable set from the installation's blueprints.
func componentNames(root string) []string {
	if got := dispatch.BuildableComponents(dispatch.LoadArchitectureCorpus(root)); len(got) > 0 {
		return got
	}
	return nil
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// PrintComponentHelp prints usage for the component command.
func PrintComponentHelp() { PrintComponentHelpFor("") }

// PrintComponentHelpFor lists what this installation's blueprints can build.
func PrintComponentHelpFor(root string) {
	fmt.Println()
	fmt.Println("  Usage: weblisk component <name> init [--platform go]")
	fmt.Println()
	names := componentNames(root)
	if len(names) == 0 {
		fmt.Println("  No blueprints found. A component is an architecture blueprint that")
		fmt.Println("  states an Endpoints table and declares its bindings.")
		fmt.Println()
		return
	}
	fmt.Println("  Components, from this installation's blueprints:")
	for _, n := range names {
		fmt.Printf("    %-14s %s\n", n, componentDescriptions[n])
	}
	fmt.Println()
}
