package secrets

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const secretsBaseDir = ".weblisk/secrets"

// Handle dispatches secrets subcommands.
func Handle(args []string, root string) error {
	if len(args) == 0 {
		return handleList(root)
	}

	switch args[0] {
	case "list":
		return handleList(root)
	case "set":
		if len(args) < 3 {
			return fmt.Errorf("usage: weblisk secret set <agent> <key>")
		}
		useStdin := false
		for _, a := range args[3:] {
			if a == "--stdin" {
				useStdin = true
			}
		}
		return handleSet(root, args[1], args[2], useStdin)
	case "get":
		if len(args) < 3 {
			return fmt.Errorf("usage: weblisk secrets get <agent> <key> --confirm")
		}
		confirm := false
		for _, a := range args[3:] {
			if a == "--confirm" {
				confirm = true
			}
		}
		return handleGet(root, args[1], args[2], confirm)
	case "delete":
		if len(args) < 3 {
			return fmt.Errorf("usage: weblisk secrets delete <agent> <key>")
		}
		return handleDelete(root, args[1], args[2])
	case "rotate":
		if len(args) < 3 {
			return fmt.Errorf("usage: weblisk secrets rotate <agent> <key>")
		}
		return handleRotate(root, args[1], args[2])
	case "help", "--help", "-h":
		PrintHelp()
		return nil
	default:
		return fmt.Errorf("unknown secrets command: %s\n  Try: weblisk secrets list|set|get|delete|rotate", args[0])
	}
}

func handleList(root string) error {
	dir := filepath.Join(root, secretsBaseDir)
	if _, err := os.Stat(dir); err != nil {
		fmt.Print("\n  No secrets configured.\n\n")
		return nil
	}

	fmt.Println()
	fmt.Printf("  %-16s %-24s %s\n", "AGENT", "KEY", "STATUS")

	count := 0
	entries, _ := os.ReadDir(dir)
	for _, agentEntry := range entries {
		if !agentEntry.IsDir() {
			continue
		}
		agent := agentEntry.Name()
		keys, _ := os.ReadDir(filepath.Join(dir, agent))
		for _, keyEntry := range keys {
			if keyEntry.IsDir() {
				continue
			}
			info, _ := keyEntry.Info()
			status := "set"
			if info != nil && info.Size() == 0 {
				status = "empty"
			}
			fmt.Printf("  %-16s %-24s %s\n", agent, keyEntry.Name(), status)
			count++
		}
	}

	if count == 0 {
		fmt.Println("  (none)")
	}
	fmt.Printf("\n  %d secret(s)\n\n", count)
	return nil
}

func handleSet(root, agent, key string, useStdin bool) error {
	var value string
	var err error

	if useStdin {
		// Read from stdin (for CI/CD piping)
		reader := bufio.NewReader(os.Stdin)
		value, err = reader.ReadString('\n')
		if err != nil && value == "" {
			return fmt.Errorf("reading from stdin: %w", err)
		}
		value = strings.TrimRight(value, "\n\r")
	} else {
		// Security: value is prompted interactively, never passed as CLI argument
		fmt.Printf("  Enter value for %s/%s: ", agent, key)
		value, err = readSecretValue()
		if err != nil {
			return fmt.Errorf("reading value: %w", err)
		}
		fmt.Println()
	}

	if value == "" {
		return fmt.Errorf("value cannot be empty")
	}

	secretDir := filepath.Join(root, secretsBaseDir, agent)
	if err := os.MkdirAll(secretDir, 0700); err != nil {
		return fmt.Errorf("creating secrets directory: %w", err)
	}

	secretPath := filepath.Join(secretDir, key)
	if err := os.WriteFile(secretPath, []byte(value), 0600); err != nil {
		return fmt.Errorf("writing secret: %w", err)
	}

	fmt.Printf("  [ok] Secret %s/%s stored.\n", agent, key)
	return nil
}

func handleGet(root, agent, key string, confirm bool) error {
	if !confirm {
		fmt.Println("  ⚠  Displaying secret values is a security risk in shared terminals.")
		fmt.Println("  Use --confirm to proceed.")
		return nil
	}

	secretPath := filepath.Join(root, secretsBaseDir, agent, key)
	data, err := os.ReadFile(secretPath)
	if err != nil {
		return fmt.Errorf("secret %s/%s not found", agent, key)
	}

	fmt.Printf("  %s/%s = %s\n", agent, key, string(data))
	return nil
}

func handleDelete(root, agent, key string) error {
	secretPath := filepath.Join(root, secretsBaseDir, agent, key)
	if _, err := os.Stat(secretPath); err != nil {
		return fmt.Errorf("secret %s/%s not found", agent, key)
	}

	if err := os.Remove(secretPath); err != nil {
		return fmt.Errorf("removing secret: %w", err)
	}

	// Remove agent directory if now empty
	agentDir := filepath.Join(root, secretsBaseDir, agent)
	entries, _ := os.ReadDir(agentDir)
	if len(entries) == 0 {
		os.Remove(agentDir)
	}

	fmt.Printf("  [ok] Secret %s/%s deleted.\n", agent, key)
	return nil
}

func handleRotate(root, agent, key string) error {
	secretPath := filepath.Join(root, secretsBaseDir, agent, key)
	if _, err := os.Stat(secretPath); err != nil {
		return fmt.Errorf("secret %s/%s not found — set it first", agent, key)
	}

	fmt.Printf("  Rotating %s/%s\n", agent, key)
	fmt.Printf("  Enter new value: ")

	value, err := readSecretValue()
	if err != nil {
		return fmt.Errorf("reading value: %w", err)
	}
	fmt.Println()

	if value == "" {
		return fmt.Errorf("value cannot be empty")
	}

	if err := os.WriteFile(secretPath, []byte(value), 0600); err != nil {
		return fmt.Errorf("writing secret: %w", err)
	}

	fmt.Printf("  [ok] Secret %s/%s rotated.\n", agent, key)
	return nil
}

// readSecretValue reads a line from stdin without echoing.
// Uses stty to disable echo on UNIX terminals.
func readSecretValue() (string, error) {
	// Disable echo via stty (works on macOS and Linux, no external deps)
	disableEcho()
	defer enableEcho()

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func disableEcho() {
	// Best-effort — if stty isn't available, input will still work (just visible)
	cmd := exec.Command("stty", "-echo")
	cmd.Stdin = os.Stdin
	cmd.Run()
}

func enableEcho() {
	cmd := exec.Command("stty", "echo")
	cmd.Stdin = os.Stdin
	cmd.Run()
}

// PrintHelp shows secrets command usage.
func PrintHelp() {
	fmt.Print(`
  Weblisk Secrets

  Usage:
    weblisk secrets list                 Show all secrets and status
    weblisk secrets set <agent> <key>    Set a secret (prompts for value)
    weblisk secrets get <agent> <key>    Print secret value (--confirm required)
    weblisk secrets delete <agent> <key> Remove a secret
    weblisk secrets rotate <agent> <key> Rotate a secret (prompts for new value)

  Security:
    - Values are NEVER passed as CLI arguments (shell history safe)
    - Files stored with 0600 permissions in .weblisk/secrets/<agent>/<key>
    - Use _shared as the agent name for cross-agent secrets

  Examples:
    weblisk secrets set email-send SMTP_PASSWORD
    weblisk secrets set _shared LLM_API_KEY
    weblisk secrets list
    weblisk secrets rotate email-send SMTP_PASSWORD
`)
}
