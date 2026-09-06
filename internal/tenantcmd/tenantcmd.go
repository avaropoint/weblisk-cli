// Package tenantcmd is the `weblisk tenant` command: a thin wrapper over
// pkg/tenant that prints progress.
//
// Thin is the point. Every decision — the order of the steps, what is refused,
// which provider — lives in pkg/tenant, which Studio drives too. What lives
// here is argument parsing, printing, and reading a passphrase from a channel
// that does not echo it.
package tenantcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/avaropoint/weblisk-cli/internal/operator"
	wserver "github.com/avaropoint/weblisk-cli/internal/server"
	"github.com/avaropoint/weblisk-cli/pkg/tenant"
)

// Handle dispatches `weblisk tenant …`.
func Handle(args []string, cwd string) error {
	if len(args) == 0 {
		PrintHelp()
		return nil
	}
	switch args[0] {
	case "create":
		return handleCreate(args[1:], cwd)
	case "help", "--help", "-h":
		PrintHelp()
		return nil
	default:
		return fmt.Errorf("unknown tenant command: %s\n  Run 'weblisk tenant help' for usage", args[0])
	}
}

// PrintHelp shows the tenant commands.
func PrintHelp() {
	fmt.Print(`
  Weblisk Tenant

  Usage:
    weblisk tenant create <name> [options]
      Turn a name into a running tenant, with a first operator credential.

      --dir <path>        Where to create it (default: ./<name>)
      --platform <p>      go (default), cloudflare, node, rust
      --provider <p>      Model backend. Omit to be asked when there is a choice
                          — see 'weblisk providers'
      --model <m>         Model name, when the provider takes one
      --operator <name>   First operator (default: $USER)
      --port <n>          Orchestrator port
      --resume            Continue into a directory that already has code
      --json              Emit one progress object per line, for a console

  The passphrase is read from stdin and never from the command line: argv is
  readable by every process on this machine.

`)
}

func handleCreate(args []string, cwd string) error {
	if err := operator.RefusePassphraseInArgv(args); err != nil {
		return err
	}
	var (
		name     string
		dir      string
		spec     = tenant.Spec{Platform: "go"}
		jsonOut  bool
		haveName bool
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dir" && i+1 < len(args):
			i++
			dir = args[i]
		case strings.HasPrefix(a, "--dir="):
			dir = strings.SplitN(a, "=", 2)[1]
		case a == "--platform" && i+1 < len(args):
			i++
			spec.Platform = args[i]
		case strings.HasPrefix(a, "--platform="):
			spec.Platform = strings.SplitN(a, "=", 2)[1]
		case a == "--provider" && i+1 < len(args):
			i++
			spec.Provider = args[i]
		case strings.HasPrefix(a, "--provider="):
			spec.Provider = strings.SplitN(a, "=", 2)[1]
		case a == "--model" && i+1 < len(args):
			i++
			spec.Model = args[i]
		case strings.HasPrefix(a, "--model="):
			spec.Model = strings.SplitN(a, "=", 2)[1]
		case a == "--operator" && i+1 < len(args):
			i++
			spec.Operator = args[i]
		case strings.HasPrefix(a, "--operator="):
			spec.Operator = strings.SplitN(a, "=", 2)[1]
		case a == "--port" && i+1 < len(args):
			i++
			spec.Port, _ = strconv.Atoi(args[i])
		case strings.HasPrefix(a, "--port="):
			spec.Port, _ = strconv.Atoi(strings.SplitN(a, "=", 2)[1])
		case a == "--resume":
			spec.Resume = true
		case a == "--json":
			jsonOut = true
		case !strings.HasPrefix(a, "-") && !haveName:
			name, haveName = a, true
		}
	}
	if !haveName {
		return fmt.Errorf("a tenant needs a name:  weblisk tenant create <name>")
	}
	spec.Name = name
	spec.Root = dir
	if spec.Root == "" {
		spec.Root = filepath.Join(cwd, Slugify(name))
	}
	if spec.Operator == "" {
		spec.Operator = defaultOperatorName()
	}

	// Read before any work, so a mistyped passphrase costs nothing.
	pass, err := wserver.ReadSecretLine("  Operator passphrase (min 12 chars): ", !jsonOut)
	if err != nil {
		return err
	}
	spec.Passphrase = pass

	if !jsonOut {
		fmt.Println()
		fmt.Printf("  Creating tenant %q in %s\n", name, spec.Root)
		fmt.Println()
	}

	progress, err := tenant.Create(context.Background(), spec)
	if err != nil {
		return err
	}
	var last *tenant.Result
	for p := range progress {
		if jsonOut {
			// One object per line. A console reads this incrementally; a single
			// document at the end would mean no progress at all, which is what
			// makes a front end grow its own implementation.
			b, _ := json.Marshal(p)
			fmt.Println(string(b))
		} else {
			printStep(p)
		}
		if p.Err != "" {
			return fmt.Errorf("%s", p.Err)
		}
		if p.Result != nil {
			last = p.Result
		}
	}
	if last == nil {
		return fmt.Errorf("the tenant was not created, and nothing said why")
	}
	if !jsonOut {
		fmt.Println()
		fmt.Printf("  Tenant %q is running.\n", last.Name)
		fmt.Printf("    address   %s\n", last.Address)
		fmt.Printf("    operator  %s\n", last.Operator)
		fmt.Printf("    model     %s\n", last.Provider)
		fmt.Println()
		fmt.Println("  Connect Studio to it with that address, or:")
		fmt.Printf("    weblisk operator connect --orch %s --name %s\n\n", last.Address, last.Operator)
	}
	return nil
}

func printStep(p tenant.Progress) {
	if p.Err != "" {
		fmt.Printf("  x %s: %s\n", p.Step, p.Err)
		return
	}
	fmt.Printf("  . %-10s %s\n", p.Step, p.Message)
}

// Slugify turns a name into a directory component.
//
// Conservative: anything that is not a letter or digit becomes a dash, so a
// tenant called "Lancaster Group (AB)" produces a path a shell can handle — and
// never a "..", which is the reason this is an allowlist rather than a
// blocklist.
func Slugify(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func defaultOperatorName() string {
	for _, env := range []string{"USER", "LOGNAME", "USERNAME"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return Slugify(v)
		}
	}
	return "operator"
}
