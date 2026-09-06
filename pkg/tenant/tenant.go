// Package tenant is the tenant lifecycle: turning a name into a running hub
// that a console is admitted to, in one operation.
//
// # Why this is a package and not a command
//
// The requirement is that the CLI and Studio achieve the same thing. The only
// way that holds is if they are the same code — parity by construction, because
// two implementations of one operation drift by default, and this project has
// already paid for that twice (provider selection, and hub discovery).
//
// The CLI's logic lives under internal/, which nothing outside the module can
// import. So the lifecycle lives here, in public API:
//
//	weblisk tenant create   is a thin wrapper that prints Progress
//	Studio                  drives the same operation and renders Progress
//
// Neither owns it.
//
// # Why Create returns a channel
//
// Hub generation is minutes long. A front end that cannot show what is happening
// during it will grow its own parallel implementation in order to get progress —
// and then there are two again. Progress is therefore part of the contract, not
// something a caller reconstructs from logs.
//
// # The order, and why it is the order
//
// Each step depends on the one before, and the sequence is what makes it safe to
// run on a machine with a network interface:
//
//  1. provider   which model writes this tenant — settled BEFORE any work,
//     because discovering there is none after ten minutes of
//     generation wastes all of it
//  2. directory  refused if non-empty, rather than merged into
//  3. generate   the hub, from the blueprints, cache-backed per file
//  4. skills     the tenant's own rules travel with it
//  5. provision  hub identity, bootstrap secret, start, claim — one action,
//     because the secret exists only between steps and a person
//     holding it in a terminal is the whole safety argument
package tenant

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/avaropoint/weblisk-cli/internal/dispatch"
	wserver "github.com/avaropoint/weblisk-cli/internal/server"
)

// Spec is everything creating a tenant needs to know.
type Spec struct {
	// Root is the tenant directory, and it is the module root. Everything the
	// tenant owns is scoped to it.
	Root string
	// Name is what the organisation calls itself.
	Name string
	// Platform selects the platform blueprint.
	Platform string
	// Provider names the model backend. Empty means "resolve it" — which asks
	// the machine, and fails with the options listed when there is a real
	// choice to make.
	Provider string
	Model    string
	// Operator is the subject the first credential is issued to, and Passphrase
	// protects that operator's key.
	//
	// The passphrase arrives from a channel that does not echo it — a terminal
	// prompt, a password field. It is never read from argv, because argv is
	// readable by every process on the machine.
	Operator   string
	Passphrase string
	Port       int
	// Resume continues into a tenant that already has generated code, reusing
	// every cached file whose inputs have not changed.
	Resume bool
}

// Step names one stage of the operation. Stable strings: a front end renders
// them, so renaming one is a breaking change to both front ends at once.
type Step string

const (
	StepProvider  Step = "provider"
	StepDirectory Step = "directory"
	StepGenerate  Step = "generate"
	StepSkills    Step = "skills"
	StepProvision Step = "provision"
	StepDone      Step = "done"
)

// Progress is one thing that happened.
type Progress struct {
	Step    Step   `json:"step"`
	Message string `json:"message"`
	// Err ends the operation. The channel closes after it.
	Err string `json:"error,omitempty"`
	// Result is present on the final message, and only then.
	Result *Result `json:"result,omitempty"`
}

// Result is what a caller needs in order to talk to the new tenant.
type Result struct {
	Root     string   `json:"root"`
	Name     string   `json:"name"`
	Address  string   `json:"address"`
	Operator string   `json:"operator"`
	Provider string   `json:"provider"`
	Model    string   `json:"model,omitempty"`
	Skills   []string `json:"skills,omitempty"`
}

// Create produces a tenant and returns progress as it happens.
//
// The returned channel is closed when the operation ends, successfully or not.
// A caller that stops reading it will block the operation at the next send, so
// read it to completion or cancel the context.
func Create(ctx context.Context, spec Spec) (<-chan Progress, error) {
	// Validated before anything is created, so a bad request costs nothing and
	// leaves no half-made directory behind.
	if err := spec.validate(); err != nil {
		return nil, err
	}
	out := make(chan Progress, 8)
	go func() {
		defer close(out)
		run(ctx, spec, out)
	}()
	return out, nil
}

func (s Spec) validate() error {
	if strings.TrimSpace(s.Root) == "" {
		return fmt.Errorf("a tenant needs a directory")
	}
	if strings.TrimSpace(s.Operator) == "" {
		return fmt.Errorf("an operator name is required — the first credential is issued to a subject")
	}
	// Length only, and never the value. protocol/identity rule 1.
	if len(s.Passphrase) < 12 {
		return fmt.Errorf("the operator passphrase must be at least 12 characters")
	}
	return nil
}

func run(ctx context.Context, spec Spec, out chan<- Progress) {
	say := func(step Step, msg string) bool {
		select {
		case out <- Progress{Step: step, Message: msg}:
			return true
		case <-ctx.Done():
			return false
		}
	}
	fail := func(step Step, err error) {
		select {
		case out <- Progress{Step: step, Err: err.Error()}:
		case <-ctx.Done():
		}
	}

	platform := spec.Platform
	if platform == "" {
		platform = "go"
	}

	// 1. Provider — first, because everything after it costs time.
	choice := dispatch.Resolve(ctx, spec.Provider)
	if choice.Err != nil {
		fail(StepProvider, choice.Err)
		return
	}
	if choice.Ambiguous {
		// Returned rather than resolved. A library must not pick between a
		// local model and a metered API on somebody's behalf; the front end
		// asks, in its own idiom, and calls again with an answer.
		fail(StepProvider, dispatch.AmbiguousError(choice.Options))
		return
	}
	model := choice.Model
	if spec.Model != "" {
		model = spec.Model
	}
	dispatch.UseProvider(choice.Kind, model)
	if !say(StepProvider, fmt.Sprintf("generating with %s%s", choice.Kind, bracket(model))) {
		return
	}

	// 2. Directory — refused if it already holds a tenant, unless resuming.
	if err := os.MkdirAll(spec.Root, 0o755); err != nil {
		fail(StepDirectory, err)
		return
	}
	if existing := generatedMarkers(spec.Root); len(existing) > 0 && !spec.Resume {
		fail(StepDirectory, fmt.Errorf(
			"%s already contains generated code (%s) — pass Resume to continue into it, "+
				"or choose an empty directory", spec.Root, strings.Join(existing, ", ")))
		return
	}
	if !say(StepDirectory, spec.Root) {
		return
	}

	// 3. Generate. Supervised: cache-backed per file, and resumed across
	// provider outages rather than needing a person to notice and retype.
	if !say(StepGenerate, "building the hub from the blueprints — this takes minutes") {
		return
	}
	if err := dispatch.ServerInit(spec.Root, platform); err != nil {
		fail(StepGenerate, err)
		return
	}

	// 4. Skills — after generation, so a failed build leaves no files
	// describing a tenant that does not exist.
	skills, serr := dispatch.InstallSkills(spec.Root, string(choice.Kind), platform)
	if serr != nil {
		// Said, not fatal. The hub exists and works; what is missing is that
		// the next editor of this tenant will be less well informed.
		if !say(StepSkills, "not installed: "+serr.Error()) {
			return
		}
	} else if len(skills) > 0 && !say(StepSkills, fmt.Sprintf("%d installed", len(skills))) {
		return
	}

	// 5. Provision — identity, bootstrap, start, claim, in one action.
	if !say(StepProvision, "starting the hub and claiming the first operator") {
		return
	}
	pr, perr := wserver.Provision(wserver.ProvisionRequest{
		Root:         spec.Root,
		Platform:     platform,
		OperatorName: spec.Operator,
		Port:         spec.Port,
		Passphrase:   spec.Passphrase,
	})
	if perr != nil {
		fail(StepProvision, perr)
		return
	}

	select {
	case out <- Progress{Step: StepDone, Message: "tenant is running", Result: &Result{
		Root:     spec.Root,
		Name:     spec.Name,
		Address:  pr.OrchestratorURL,
		Operator: pr.OperatorName,
		Provider: string(choice.Kind),
		Model:    model,
		Skills:   skills,
	}}:
	case <-ctx.Done():
	}
}

// generatedMarkers reports the generated artifacts already in a directory, so
// "is there a tenant here" is answered by what exists rather than by one name.
func generatedMarkers(root string) []string {
	var found []string
	for _, m := range []string{"go.mod", "cmd", "internal", "wrangler.toml", "package.json", "Cargo.toml"} {
		if _, err := os.Stat(filepath.Join(root, m)); err == nil {
			found = append(found, m)
		}
	}
	return found
}

func bracket(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}
