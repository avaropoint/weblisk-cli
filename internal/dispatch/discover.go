package dispatch

// discover.go — what this machine can actually generate with, and which one to use.
//
// # The fault this replaces
//
// Provider selection was `os.Getenv("WL_AI_PROVIDER")` with a default of
// "openai", which then required WL_AI_KEY. So the out-of-the-box experience on a
// machine with Claude Code installed and logged in was:
//
//	weblisk server init  →  "WL_AI_KEY required for OpenAI"
//
// A working provider was sitting on the PATH and nothing looked. Worse, Studio
// shells out to this CLI and never set WL_AI_* at all, so every hub generated
// from the build console inherited whatever the Studio process happened to be
// launched with — which is to say, the same error.
//
// # Discovery is evidence, not a guess
//
// Available() probes: a binary that exists and runs, an Ollama that answers, a
// key that is present. Each answer carries WHY, because "claude code: not on
// PATH and not in ~/.local/bin" is actionable and "no provider" is not.
//
// # Choosing
//
// Resolution order, most specific first:
//
//	1. an explicit --provider (or WL_AI_PROVIDER)  — somebody said
//	2. the preference order, among what is AVAILABLE — claude, codex, ollama, api
//
// A default is only ever chosen from providers that were actually found, so the
// CLI cannot select something that will fail on first use. When more than one is
// available and nobody has chosen, that is reported as a CHOICE rather than
// resolved silently — the caller decides whether to prompt (a terminal) or to
// surface it (Studio, which asks before the build).

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// ProviderKind is a backend this pipeline knows how to drive.
type ProviderKind string

const (
	ProviderClaudeCode ProviderKind = "claude-code"
	ProviderCodex      ProviderKind = "codex"
	ProviderOllama     ProviderKind = "ollama"
	ProviderAnthropic  ProviderKind = "anthropic"
	ProviderOpenAI     ProviderKind = "openai"
)

// preferenceOrder is the order a default is taken in when nobody has chosen.
//
// Local coding-agent CLIs first because they need no key and no billing
// relationship — an operator who already has one is the case where "it just
// works" is achievable. Ollama next: also local and free, but a small local
// model produces materially weaker code from the same blueprints, so it is not
// preferred over a subscription that is already present. Keyed APIs last,
// because reaching for somebody's key without being asked is a cost decision
// this program has no standing to make.
var preferenceOrder = []ProviderKind{
	ProviderClaudeCode, ProviderCodex, ProviderOllama, ProviderAnthropic, ProviderOpenAI,
}

// ProviderStatus is one backend and whether it can be used right now.
type ProviderInfo struct {
	Kind ProviderKind `json:"kind"`
	// Label is what a person calls it.
	Label     string `json:"label"`
	Available bool   `json:"available"`
	// Detail says how it was found, or what is missing. Always populated: a
	// provider listed as unavailable with no reason cannot be acted on.
	Detail string `json:"detail"`
	// Path is the resolved binary, for the CLI-backed kinds.
	Path string `json:"path,omitempty"`
	// Model is what will be used unless something says otherwise.
	Model string `json:"model,omitempty"`
	// NeedsKey names the environment variable an API-keyed provider wants.
	NeedsKey string `json:"needs_key,omitempty"`
	// Local is true when nothing leaves the machine. A tenant with data
	// residency constraints cares about exactly this column.
	Local bool `json:"local"`
}

// defaultModels are the models used when nobody names one.
//
// Current as of 2026-09. These are checked into a program that outlives them,
// so they are in one place rather than scattered through newRawProvider — the
// previous defaults (claude-sonnet-4-20250514, gpt-4o, llama3) had all aged out
// of relevance while sitting in four different switch arms.
var defaultModels = map[ProviderKind]string{
	// Left empty on purpose for the CLI-backed kinds: the tool has its own
	// configured model and its own login, and overriding it here would silently
	// contradict a choice the operator made in that tool.
	ProviderClaudeCode: "",
	ProviderCodex:      "",
	ProviderOllama:     "llama3.1",
	ProviderAnthropic:  "claude-opus-5",
	ProviderOpenAI:     "gpt-4o",
}

// probeTimeout bounds one discovery probe. Short: this runs before a build, and
// a provider that cannot answer in two seconds is not the one to reach for.
const probeTimeout = 2 * time.Second

// Available reports every provider and whether it can be used.
//
// Ordered by preference so a caller listing them shows the one it would pick
// first. Never returns an empty slice — a machine with nothing installed still
// gets the list, with each entry saying what is missing.
func Available(ctx context.Context) []ProviderInfo {
	out := []ProviderInfo{
		probeLocalCLI(ctx, ProviderClaudeCode, "claude", "Claude Code"),
		probeLocalCLI(ctx, ProviderCodex, "codex", "Codex CLI"),
		probeOllama(ctx),
		probeKeyed(ProviderAnthropic, "Anthropic API", "ANTHROPIC_API_KEY"),
		probeKeyed(ProviderOpenAI, "OpenAI API", "OPENAI_API_KEY"),
	}
	rank := map[ProviderKind]int{}
	for i, k := range preferenceOrder {
		rank[k] = i
	}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Kind] < rank[out[j].Kind] })
	return out
}

// probeLocalCLI finds a coding-agent CLI and confirms it runs.
//
// Presence on disk is not enough: an installer can leave a wrapper script that
// exits non-zero, and reporting that as available moves the failure to the
// middle of a build. `--version` is the cheapest thing that proves the binary
// executes.
func probeLocalCLI(ctx context.Context, kind ProviderKind, binary, label string) ProviderInfo {
	st := ProviderInfo{Kind: kind, Label: label, Local: true}
	bin, searched := ResolveLocalCLI(binary, os.Getenv(strings.ToUpper(binary)+"_BIN"))
	if bin == "" {
		st.Detail = fmt.Sprintf("%s is not on PATH, and not in %s", binary, strings.Join(searched, ", "))
		return st
	}
	st.Path = bin
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	outBytes, err := exec.CommandContext(cctx, bin, "--version").CombinedOutput()
	if err != nil {
		st.Detail = fmt.Sprintf("%s is at %s but did not run: %v", binary, bin, err)
		return st
	}
	st.Available = true
	st.Detail = fmt.Sprintf("%s (%s)", bin, strings.TrimSpace(firstOutputLine(string(outBytes))))
	st.Model = defaultModels[kind]
	return st
}

// probeOllama asks a local Ollama what it is holding.
//
// Installed and running are different facts, and so are running and HAVING a
// model: an Ollama with no models pulled answers every request with a 404, so
// it is reported unavailable with the command that fixes it.
func probeOllama(ctx context.Context) ProviderInfo {
	st := ProviderInfo{Kind: ProviderOllama, Label: "Ollama", Local: true}
	base := strings.TrimRight(envOr("OLLAMA_HOST", "http://localhost:11434"), "/")
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", base+"/api/tags", nil)
	if err != nil {
		st.Detail = "not a usable Ollama address: " + base
		return st
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		st.Detail = "no Ollama answered at " + base + " — start it with `ollama serve`"
		return st
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&tags) != nil {
		st.Detail = "the Ollama at " + base + " did not answer with a model list"
		return st
	}
	if len(tags.Models) == 0 {
		st.Detail = "Ollama is running at " + base + " with no models — pull one, e.g. `ollama pull llama3.1`"
		return st
	}
	st.Available = true
	st.Model = pickOllamaModel(tags.Models, defaultModels[ProviderOllama])
	names := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		names = append(names, m.Name)
	}
	st.Detail = fmt.Sprintf("%s — %d model(s): %s", base, len(names), strings.Join(names, ", "))
	return st
}

// pickOllamaModel prefers the configured default when it is actually pulled.
//
// Naming a model the machine does not have produces a 404 at generation time,
// which reads as a broken pipeline rather than a missing download.
func pickOllamaModel(models []struct {
	Name string `json:"name"`
}, want string) string {
	for _, m := range models {
		if m.Name == want || strings.HasPrefix(m.Name, want+":") {
			return m.Name
		}
	}
	return models[0].Name
}

// probeKeyed reports an API-backed provider, which is available exactly when its
// key is present.
func probeKeyed(kind ProviderKind, label, keyEnv string) ProviderInfo {
	st := ProviderInfo{Kind: kind, Label: label, NeedsKey: keyEnv, Model: defaultModels[kind]}
	// WL_AI_KEY is the pipeline's own variable and outranks the vendor one: an
	// operator who set it meant it for this program specifically.
	if os.Getenv("WL_AI_KEY") != "" || os.Getenv(keyEnv) != "" {
		st.Available = true
		st.Detail = "key present"
		return st
	}
	st.Detail = "no key — set " + keyEnv + " (or WL_AI_KEY)"
	return st
}

// Choice is the outcome of resolving which provider to use.
type Choice struct {
	// Kind is what to use. Empty when Ambiguous or when nothing is available.
	Kind  ProviderKind `json:"kind,omitempty"`
	Model string       `json:"model,omitempty"`
	// Why says how this was arrived at, in a sentence a build log can carry.
	Why string `json:"why,omitempty"`
	// Ambiguous is set when several providers are available and nobody chose.
	// It is not an error: it is the moment to ask.
	Ambiguous bool           `json:"ambiguous,omitempty"`
	Options   []ProviderInfo `json:"options,omitempty"`
	// Err is set when nothing usable was found at all.
	Err error `json:"-"`
}

// Resolve decides which provider a build should use.
//
// `requested` is an explicit choice — a --provider flag, WL_AI_PROVIDER, or a
// tenant's stored preference. It wins, and if it names something unavailable
// that is an ERROR rather than a silent fall-back: an operator who pinned a
// tenant to a local model for data-residency reasons must not have the build
// quietly send that tenant's blueprints to an API instead.
//
// With no request: one available provider is used, several is a question, none
// is an error naming every place that was looked.
func Resolve(ctx context.Context, requested string) Choice {
	found := Available(ctx)
	byKind := map[ProviderKind]ProviderInfo{}
	for _, s := range found {
		byKind[s.Kind] = s
	}

	if r := ProviderKind(strings.TrimSpace(strings.ToLower(requested))); r != "" {
		st, known := byKind[normaliseKind(r)]
		if !known {
			return Choice{Err: fmt.Errorf("unknown provider %q — this pipeline drives %s",
				requested, strings.Join(kindNames(), ", "))}
		}
		if !st.Available {
			return Choice{Err: fmt.Errorf("%s was requested but cannot be used: %s", st.Label, st.Detail)}
		}
		return Choice{Kind: st.Kind, Model: st.Model, Why: "requested explicitly"}
	}

	var usable []ProviderInfo
	for _, s := range found {
		if s.Available {
			usable = append(usable, s)
		}
	}
	switch len(usable) {
	case 0:
		return Choice{Err: fmt.Errorf("no usable model provider was found.\n%s\n\n"+
			"Install one, or name it with --provider:\n  %s",
			indentDetails(found), strings.Join(kindNames(), "  ")), Options: found}
	case 1:
		return Choice{Kind: usable[0].Kind, Model: usable[0].Model,
			Why: "the only provider available on this machine"}
	default:
		// Several. Reported as a choice rather than resolved, because picking
		// between a local model and a paid API is a decision with cost and data
		// consequences that belong to the operator.
		return Choice{Ambiguous: true, Options: usable}
	}
}

// Default picks from what is available, in preference order.
//
// Used when a caller has offered the choice and been told to just get on with
// it. Separate from Resolve so that "nobody chose" and "nobody wants to be
// asked" stay different states.
func Default(options []ProviderInfo) (ProviderKind, string) {
	for _, want := range preferenceOrder {
		for _, s := range options {
			if s.Kind == want && s.Available {
				return s.Kind, s.Model
			}
		}
	}
	return "", ""
}

// normaliseKind accepts the aliases that already exist in the wild.
func normaliseKind(k ProviderKind) ProviderKind {
	switch k {
	case "claude", "claude-local", "claude_code":
		return ProviderClaudeCode
	case "codex-cli":
		return ProviderCodex
	case "local":
		return ProviderOllama
	}
	return k
}

func kindNames() []string {
	out := make([]string, 0, len(preferenceOrder))
	for _, k := range preferenceOrder {
		out = append(out, string(k))
	}
	return out
}

func indentDetails(all []ProviderInfo) string {
	var b strings.Builder
	for _, s := range all {
		b.WriteString("  " + s.Label + ": " + s.Detail + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func firstOutputLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ambiguousProviderError is what a build says when the machine offers a choice
// and nobody has made one.
//
// Not "no provider configured" — that is the opposite of the truth and sends an
// operator looking for something to install. It lists what was found, says how
// to pick, and names the flag.
// AmbiguousError is the exported form, for pkg/tenant — which is public API and
// cannot reach into internal/ from outside the module, but is inside it.
func AmbiguousError(options []ProviderInfo) error { return ambiguousProviderError(options) }

func ambiguousProviderError(options []ProviderInfo) error {
	var b strings.Builder
	b.WriteString("more than one model provider is available here, so this build will not guess.\n\n")
	for _, s := range options {
		where := "sends work off this machine"
		if s.Local {
			where = "runs on this machine"
		}
		model := s.Model
		if model == "" {
			model = "the tool's own configured model"
		}
		fmt.Fprintf(&b, "  %-12s %s — %s\n               %s\n", s.Kind, s.Label, where, model)
	}
	b.WriteString("\nChoose one:\n  weblisk server init --provider <name>\n")
	b.WriteString("or set WL_AI_PROVIDER, or pick a default for the tenant in Studio.")
	return fmt.Errorf("%s", b.String())
}

// discoveredOllamaModel returns a model this machine has actually pulled.
func discoveredOllamaModel() string {
	st := probeOllama(context.Background())
	if st.Model != "" {
		return st.Model
	}
	return defaultModels[ProviderOllama]
}

// ChooseProvider settles which backend this run uses, before any work starts.
//
// `requested` is a --provider flag or a tenant's stored preference; empty means
// nobody has said. On an interactive terminal an ambiguous machine is a question
// worth asking — that is the flow the operator asked for: discover, offer, then
// build. Everywhere else (Studio's subprocess, CI, a script) there is nobody to
// ask, so the ambiguity is returned as an error that names the options and the
// flag, and the caller offers the choice in its own idiom.
func ChooseProvider(requested, model string) error {
	c := Resolve(context.Background(), requested)
	if c.Err != nil {
		return c.Err
	}
	if c.Ambiguous {
		picked, ok := askForProvider(c.Options)
		if !ok {
			return ambiguousProviderError(c.Options)
		}
		c = Choice{Kind: picked.Kind, Model: picked.Model, Why: "chosen at the prompt"}
	}
	if model != "" {
		c.Model = model
	}
	UseProvider(c.Kind, c.Model)
	fmt.Printf("  Model provider: %s", c.Kind)
	if c.Model != "" {
		fmt.Printf(" (%s)", c.Model)
	}
	if c.Why != "" {
		fmt.Printf(" — %s", c.Why)
	}
	fmt.Println()
	return nil
}

// askForProvider offers the choice on a terminal, and only on a terminal.
//
// Guarded on stdin being a character device: Studio runs this binary with a pipe
// for stdin, and a prompt written into a pipe is a build that hangs forever with
// no indication why.
func askForProvider(options []ProviderInfo) (ProviderInfo, bool) {
	if !stdinIsTerminal() {
		return ProviderInfo{}, false
	}
	fmt.Println()
	fmt.Println("  More than one model provider is available here:")
	for i, s := range options {
		where := "off this machine"
		if s.Local {
			where = "on this machine"
		}
		model := s.Model
		if model == "" {
			model = "its own configured model"
		}
		fmt.Printf("    %d) %-12s %s, %s\n", i+1, s.Kind, model, where)
	}
	def, _ := Default(options)
	fmt.Printf("  Choose [1-%d], or Enter for %s: ", len(options), def)

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return ProviderInfo{}, false
	}
	answer := strings.TrimSpace(line)
	if answer == "" {
		for _, s := range options {
			if s.Kind == def {
				return s, true
			}
		}
		return ProviderInfo{}, false
	}
	if n, convErr := strconv.Atoi(answer); convErr == nil && n >= 1 && n <= len(options) {
		return options[n-1], true
	}
	// A name rather than a number, because people type "ollama".
	want := normaliseKind(ProviderKind(strings.ToLower(answer)))
	for _, s := range options {
		if s.Kind == want {
			return s, true
		}
	}
	return ProviderInfo{}, false
}

// stdinIsTerminal reports whether there is a person to ask.
//
// term.IsTerminal, not a ModeCharDevice check on the FileInfo. /dev/null IS a
// character device, so the mode test says "terminal" for `< /dev/null` — which
// is exactly how a non-interactive caller invokes this. Measured: the menu
// printed, the read returned EOF, and the build failed with the prompt already
// on screen. A test running under `go test` would have hung instead.
func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// PrintProviders shows what this machine offers, and which would be used.
func PrintProviders(asJSON bool) error {
	all := Available(context.Background())
	if asJSON {
		b, err := json.MarshalIndent(all, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	fmt.Println()
	fmt.Println("  Model providers")
	fmt.Println()
	for _, s := range all {
		mark := "·"
		if s.Available {
			mark = "✓"
		}
		where := "hosted"
		if s.Local {
			where = "local"
		}
		fmt.Printf("  %s %-12s %-16s %s\n", mark, s.Kind, s.Label+" ("+where+")", s.Detail)
		if s.Available && s.Model != "" {
			fmt.Printf("      model: %s\n", s.Model)
		}
	}
	fmt.Println()
	c := Resolve(context.Background(), "")
	switch {
	case c.Err != nil:
		fmt.Printf("  Nothing usable: %v\n", c.Err)
	case c.Ambiguous:
		def, model := Default(c.Options)
		fmt.Printf("  Several are available, so a build will ask. Without an answer it takes %s", def)
		if model != "" {
			fmt.Printf(" (%s)", model)
		}
		fmt.Println(".")
		fmt.Println("  Pin one with --provider, WL_AI_PROVIDER, or per tenant in Studio.")
	default:
		fmt.Printf("  A build here uses %s — %s.\n", c.Kind, c.Why)
	}
	fmt.Println()
	return nil
}
