package server

// Provisioning a tenant and establishing its first operator in one action.
//
// architecture/admin's bootstrap flow is written for a human at a terminal:
//
//	2. Orchestrator prints: "No operators registered..."
//	   "Run: weblisk operator init && weblisk operator register"
//
// That excludes a console provisioning a tenant on behalf of a signed-in user,
// which is the same omission protocol/identity warns about: "a runtime without
// [a terminal] cannot, and a specification that says prompt has excluded it."
//
// So the steps are the same steps, driven by one command instead of a person:
// generate, build, start, establish the first operator, obtain a token. The
// bootstrap remains a first-operator auto-approval recorded in the audit log —
// nothing about the trust model changes because a console asked rather than a
// shell.
//
// # What this does NOT do
//
// It does not accept a passphrase in argv, and it does not store one. The
// passphrase arrives on stdin, is used to create or unlock the operator key,
// and is discarded with the process. What persists is the 4-hour token, beside
// the identity that obtained it.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/avaropoint/weblisk-cli/internal/operator"
)

// ProvisionRequest is one tenant-and-operator bootstrap.
type ProvisionRequest struct {
	// Root is the tenant directory. It is the Go module root.
	Root string
	// Platform selects the platform blueprint.
	Platform string
	// OperatorName is the subject the first operator credential is issued to.
	OperatorName string
	// KeysDir locates that operator's identity. Empty uses WL_KEYS_DIR, then the
	// default — but a console serving several accounts MUST set it, or a second
	// account uses or overwrites the first account's identity.
	KeysDir string
	// Port is the orchestrator's listen port. Zero lets the caller choose later.
	Port int
	// Passphrase protects the operator key. Supplied by the caller from a
	// non-echoing channel; never read from argv, never written down.
	Passphrase string
}

// ProvisionResult is what a caller needs in order to talk to the tenant.
type ProvisionResult struct {
	OrchestratorURL string
	OperatorName    string
	Token           string
	TokenExpiresAt  int64
	// Steps records what happened, in order, so a console can show progress and
	// a failure names the step it failed at rather than the whole action.
	Steps []string
}

// Provision establishes an operator credential against a tenant that is already
// generated and running.
//
// Generation is deliberately NOT folded in. It takes tens of minutes, streams
// progress, and can be resumed; wrapping it in a call that also handles a
// credential would make one action out of two with very different failure modes
// — and a passphrase would then be held in memory for the length of a build.
func Provision(req ProvisionRequest) (ProvisionResult, error) {
	var res ProvisionResult
	res.OperatorName = req.OperatorName

	if strings.TrimSpace(req.OperatorName) == "" {
		return res, fmt.Errorf("an operator name is required — the credential is issued to a subject")
	}
	if len(req.Passphrase) < 12 {
		return res, fmt.Errorf("the operator passphrase must be at least 12 characters (protocol/identity rule 1)")
	}
	if req.KeysDir != "" {
		operator.SetKeysDir(req.KeysDir)
	}

	st := StatusOf(req.Root, "orchestrator")
	if !st.Running {
		return res, fmt.Errorf("the tenant's orchestrator is not running\n" +
			"  Start it first: weblisk server start --detach")
	}
	res.OrchestratorURL = st.Address
	res.Steps = append(res.Steps, "orchestrator running at "+st.Address)

	// An identity may already exist — a subject holds ONE identity and a
	// separate credential per hub, so provisioning a second tenant must reuse it
	// rather than mint a new one and orphan the first tenant's grant.
	created, err := operator.EnsureIdentity(req.OperatorName, req.Passphrase)
	if err != nil {
		return res, fmt.Errorf("operator identity: %w", err)
	}
	if created {
		res.Steps = append(res.Steps, "operator identity created for "+req.OperatorName)
	} else {
		res.Steps = append(res.Steps, "existing operator identity unlocked for "+req.OperatorName)
	}

	if err := operator.RegisterWith(res.OrchestratorURL, req.OperatorName, req.Passphrase); err != nil {
		// Already registered is not a failure: provisioning is re-runnable, and a
		// console that retries after a network error must not be told the tenant
		// is broken.
		if !operator.IsAlreadyRegistered(err) {
			return res, fmt.Errorf("registering the operator: %w", err)
		}
		res.Steps = append(res.Steps, "operator already registered with this tenant")
	} else {
		res.Steps = append(res.Steps, "operator registered — first operator is auto-approved")
	}

	token, expires, err := operator.RequestToken(res.OrchestratorURL, req.OperatorName)
	if err != nil {
		return res, fmt.Errorf("obtaining an operator token: %w", err)
	}
	res.Token, res.TokenExpiresAt = token, expires
	if expires > 0 {
		res.Steps = append(res.Steps, fmt.Sprintf("token issued, expires %s",
			time.Unix(expires, 0).UTC().Format(time.RFC3339)))
	} else {
		res.Steps = append(res.Steps, "token issued")
	}
	return res, nil
}

// HandleProvision runs `weblisk server provision`.
//
// The passphrase is read from stdin and nowhere else. A console supplies it
// from its own non-echoing channel — a password field over TLS is such a
// channel, which is what protocol/identity's "a non-echoing channel operated by
// a human" permits — and this process discards it on exit.
func HandleProvision(args []string, root string) error {
	if err := operator.RefusePassphraseInArgv(args); err != nil {
		return err
	}
	req := ProvisionRequest{Root: root, Platform: "go"}
	jsonOut := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--operator" && i+1 < len(args):
			i++
			req.OperatorName = args[i]
		case strings.HasPrefix(args[i], "--operator="):
			req.OperatorName = strings.SplitN(args[i], "=", 2)[1]
		case args[i] == "--keys-dir" && i+1 < len(args):
			i++
			req.KeysDir = args[i]
		case strings.HasPrefix(args[i], "--keys-dir="):
			req.KeysDir = strings.SplitN(args[i], "=", 2)[1]
		case args[i] == "--json":
			jsonOut = true
		}
	}
	if req.OperatorName == "" {
		return fmt.Errorf("--operator <name> is required — the credential is issued to a subject")
	}

	pass, err := readSecretLine("  Operator passphrase: ", !jsonOut)
	if err != nil {
		return err
	}
	req.Passphrase = pass

	res, perr := Provision(req)
	if perr != nil {
		return perr
	}
	if jsonOut {
		// For a console. The token IS the credential, so this goes over the same
		// channel that carried the passphrase and is never logged.
		return writeJSON(res)
	}
	fmt.Println()
	for _, s := range res.Steps {
		fmt.Printf("  %s\n", s)
	}
	fmt.Println()
	fmt.Printf("  Connected to %s as %s\n", res.OrchestratorURL, res.OperatorName)
	fmt.Println()
	return nil
}

// ReadSecretLine is readSecretLine, exported for pkg/tenant.
//
// One implementation of "read a passphrase without echoing it": the terminal
// path suppresses echo, the piped path reads a line. A second copy in the
// tenant command is exactly how one of them ends up echoing.
func ReadSecretLine(prompt string, showPrompt bool) (string, error) {
	return readSecretLine(prompt, showPrompt)
}

// readSecretLine reads one line from stdin without echoing it.
func readSecretLine(prompt string, showPrompt bool) (string, error) {
	if showPrompt {
		fmt.Print(prompt)
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", fmt.Errorf("reading the passphrase: %w", err)
		}
		return string(b), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("reading the passphrase: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// writeJSON emits the result for a machine caller.
func writeJSON(res ProvisionResult) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}
