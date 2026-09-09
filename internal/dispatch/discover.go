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
//	2. the catalog weight, among what is AVAILABLE — walk from highest to
//	   lowest and take the first that this workstation can actually run
//
// A default is only ever chosen from providers that were actually found, so the
// CLI cannot select something that will fail on first use. When more than one is
// available and nobody has chosen, the highest-weighted one is the operator
// default. That is not a guess at cost or data residency: it is the ranking the
// catalog already states, applied to evidence. --provider still pins.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

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

// probeTimeout bounds one discovery probe. Short: this runs before a build, and
// a provider that cannot answer in two seconds is not the one to reach for.
const probeTimeout = 2 * time.Second

// Available reports every provider and whether it can be used.
//
// Ordered by preference so a caller listing them shows the one it would pick
// first. Never returns an empty slice — a machine with nothing installed still
// gets the list, with each entry saying what is missing.
func Available(ctx context.Context) []ProviderInfo {
	out := make([]ProviderInfo, len(backends), len(backends)+2)
	var wg sync.WaitGroup
	for i, b := range backends {
		wg.Add(1)
		go func(i int, b backend) {
			defer wg.Done()
			out[i] = probeBackend(ctx, b)
		}(i, b)
	}
	wg.Wait()
	// Generic escapes, only when the operator configured them. Listing
	// local-cli as "not installed" on every machine would be noise; listing it
	// when WL_AI_COMMAND is set is how an arbitrary local tool becomes a
	// choice rather than a secret environment variable.
	if cmd := strings.TrimSpace(os.Getenv("WL_AI_COMMAND")); cmd != "" {
		out = append(out, probeConfiguredLocalCLI(ctx, cmd))
	}
	if base := strings.TrimSpace(os.Getenv("WL_AI_BASE_URL")); base != "" && !catalogOwnsBaseURL(base) {
		out = append(out, probeCustomEndpoint(ctx, base))
	}
	return out
}

func probeBackend(ctx context.Context, b backend) ProviderInfo {
	switch b.Driver {
	case driverLocalCLI:
		return probeLocalCLI(ctx, b.Kind, b.Binary, b.Label)
	case driverOllama:
		return probeOllama(ctx)
	case driverAnthropic:
		return probeKeyed(b)
	case driverOpenAI:
		if b.Local {
			return probeLocalOpenAI(ctx, b)
		}
		if b.RequiresURL && strings.TrimSpace(os.Getenv("WL_AI_BASE_URL")) == "" {
			st := probeKeyed(b)
			st.Available = false
			st.Detail = "no endpoint — set WL_AI_BASE_URL"
			return st
		}
		return probeKeyed(b)
	default:
		return ProviderInfo{Kind: b.Kind, Label: b.Label, Detail: "unknown driver"}
	}
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
	// Installed and runnable is what --version proves, and it is ALL it proves.
	// A coding-agent CLI keeps its credentials in the OS keychain, so there is no
	// cheap portable way to know whether it is logged in — and `claude --version`
	// succeeds identically either way. Measured.
	//
	// Said here rather than implied, because "available" was being read as "will
	// work": discovery offered claude-code, a tenant build started, and thirty
	// seconds later it stopped with "Not logged in · Please run /login". The
	// pre-flight in RequireProvider is what actually verifies it — one real call
	// before any generation — and that division is right. What was wrong was
	// claiming more here than this probe can see.
	st.Detail = fmt.Sprintf("%s (%s) — installed; whether it is logged in is settled by ResolveReady before a build",
		bin, strings.TrimSpace(firstOutputLine(string(outBytes))))
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
func probeKeyed(b backend) ProviderInfo {
	st := ProviderInfo{Kind: b.Kind, Label: b.Label, Local: b.Local, Model: b.DefaultModel}
	if len(b.KeyEnvs) > 0 {
		st.NeedsKey = b.KeyEnvs[0]
	} else {
		st.NeedsKey = "WL_AI_KEY"
	}
	if apiKeyFor(&b) != "" {
		st.Available = true
		st.Detail = "key present"
		return st
	}
	st.Detail = "no key — set " + b.keyHint()
	return st
}

// probeLocalOpenAI asks a local OpenAI-compatible server what it is holding.
//
// LM Studio, llama.cpp, vLLM and similar all answer GET /v1/models. A server
// that is not running is unavailable; a server with no models is the same
// shape as an empty Ollama — reported with the address, not offered.
func probeLocalOpenAI(ctx context.Context, b backend) ProviderInfo {
	st := ProviderInfo{Kind: b.Kind, Label: b.Label, Local: true}
	base := strings.TrimRight(envOr("WL_AI_BASE_URL", b.BaseURL), "/")
	if base == "" {
		st.Detail = "no endpoint — set WL_AI_BASE_URL"
		return st
	}
	ids, detail, ok := listOpenAIModels(ctx, base)
	st.Detail = detail
	if !ok {
		return st
	}
	st.Available = true
	if want := defaultModels[b.Kind]; want != "" {
		st.Model = pickNamedModel(ids, want)
	} else if len(ids) > 0 {
		st.Model = ids[0]
	}
	return st
}

func listOpenAIModels(ctx context.Context, base string) (ids []string, detail string, ok bool) {
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", base+"/models", nil)
	if err != nil {
		return nil, "not a usable address: " + base, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "nothing answered at " + base + " — start the local server, or set WL_AI_BASE_URL", false
	}
	defer resp.Body.Close()
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return nil, "the server at " + base + " did not answer with a model list", false
	}
	if len(body.Data) == 0 {
		return nil, "a server is running at " + base + " with no models loaded", false
	}
	ids = make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, fmt.Sprintf("%s — %d model(s): %s", base, len(ids), strings.Join(ids, ", ")), true
}

func pickNamedModel(ids []string, want string) string {
	for _, id := range ids {
		if id == want || strings.HasPrefix(id, want+":") {
			return id
		}
	}
	if len(ids) == 0 {
		return want
	}
	return ids[0]
}

func probeConfiguredLocalCLI(ctx context.Context, command string) ProviderInfo {
	st := ProviderInfo{Kind: ProviderLocalCLI, Label: "local CLI", Local: true}
	bin, searched := ResolveLocalCLI(filepath.Base(command), command)
	if bin == "" {
		st.Detail = fmt.Sprintf("WL_AI_COMMAND=%q was not found. Looked on PATH and in: %s",
			command, strings.Join(searched, ", "))
		return st
	}
	st.Path = bin
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if err := exec.CommandContext(cctx, bin, "--version").Run(); err != nil {
		st.Detail = fmt.Sprintf("%s is at %s but did not run: %v", command, bin, err)
		return st
	}
	st.Available = true
	st.Detail = bin + " — WL_AI_COMMAND"
	return st
}

func probeCustomEndpoint(ctx context.Context, base string) ProviderInfo {
	st := ProviderInfo{
		Kind:  ProviderKind("custom"),
		Label: "custom OpenAI-compatible",
		Local: isLocalURL(base),
	}
	base = strings.TrimRight(base, "/")
	ids, detail, ok := listOpenAIModels(ctx, base)
	if !ok {
		// A configured URL that does not answer /v1/models is still a choice:
		// some OpenAI-compatible servers implement chat but not the models
		// list. Offer it; generation will fail with the server's own error if
		// the URL is wrong.
		st.Available = true
		st.Model = envOr("WL_AI_MODEL", "default")
		st.Detail = "WL_AI_BASE_URL=" + base + " — " + detail
		return st
	}
	st.Available = true
	st.Detail = "WL_AI_BASE_URL — " + detail
	if m := os.Getenv("WL_AI_MODEL"); m != "" {
		st.Model = m
	} else if len(ids) > 0 {
		st.Model = ids[0]
	}
	return st
}

func catalogOwnsBaseURL(base string) bool {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	for _, b := range backends {
		if b.BaseURL != "" && strings.TrimRight(b.BaseURL, "/") == base {
			return true
		}
	}
	return false
}

func isLocalURL(u string) bool {
	u = strings.ToLower(u)
	return strings.Contains(u, "localhost") || strings.Contains(u, "127.0.0.1") || strings.Contains(u, "[::1]")
}

// Choice is the outcome of resolving which provider to use.
type Choice struct {
	// Kind is what to use. Empty when nothing is available.
	Kind  ProviderKind `json:"kind,omitempty"`
	Model string       `json:"model,omitempty"`
	// Why says how this was arrived at, in a sentence a build log can carry.
	Why string `json:"why,omitempty"`
	// Ambiguous is retained for callers that still inspect it. Resolve no
	// longer sets it: the operator default is the highest-weighted available
	// backend, not a question.
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
// With no request: the highest-weighted available provider is the operator
// default, walking the catalog until one can actually run. None is an error
// naming every place that was looked.
func Resolve(ctx context.Context, requested string) Choice {
	found := Available(ctx)
	byKind := map[ProviderKind]ProviderInfo{}
	for _, s := range found {
		byKind[s.Kind] = s
	}

	if r := ProviderKind(strings.TrimSpace(strings.ToLower(requested))); r != "" {
		canon := normaliseKind(r)
		st, known := byKind[canon]
		if !known {
			// An OpenAI-compatible URL or a configured local binary is how this
			// pipeline drives something that is not in the catalog. Refusing a
			// name the operator typed, while those variables are set, is the
			// generic path that used to exist only in newRawProvider — which
			// ChooseProvider never reached.
			if c := resolveGeneric(canon); c.Kind != "" || c.Err != nil {
				return c
			}
			return Choice{Err: fmt.Errorf("unknown provider %q — this pipeline drives %s\n"+
				"  For anything else: WL_AI_BASE_URL (OpenAI-compatible HTTP) or "+
				"WL_AI_PROVIDER=local-cli with WL_AI_COMMAND",
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
	return chooseFromUsable(usable, found)
}

// chooseFromUsable is the operator default: walk the catalog from highest
// weight to lowest and take the first backend this machine can actually run.
func chooseFromUsable(usable, found []ProviderInfo) Choice {
	switch len(usable) {
	case 0:
		return Choice{Err: fmt.Errorf("no usable model provider was found.\n%s\n\n"+
			"Install one, or name it with --provider:\n  %s",
			indentDetails(found), strings.Join(kindNames(), "  ")), Options: found}
	case 1:
		return Choice{Kind: usable[0].Kind, Model: usable[0].Model,
			Why: "the only provider present on this machine", Options: usable}
	default:
		k, m := Default(usable)
		if k == "" {
			return Choice{Err: fmt.Errorf("no usable model provider was found"), Options: found}
		}
		return Choice{Kind: k, Model: m,
			Why: "highest-weighted present on this machine", Options: usable}
	}
}

// Default picks from what is available, walking the catalog from highest
// weight to lowest. An unavailable higher-weighted backend is skipped, not
// substituted for — the next row that can actually run wins.
func Default(options []ProviderInfo) (ProviderKind, string) {
	for _, want := range preferenceOrder() {
		for _, s := range options {
			if s.Kind == want && s.Available {
				return s.Kind, s.Model
			}
		}
	}
	// Configured extras (local-cli, a custom URL) are not in the catalog, so
	// they would otherwise lose to "nothing" even when they are the only
	// usable backend on the machine.
	for _, s := range options {
		if s.Available {
			return s.Kind, s.Model
		}
	}
	return "", ""
}

// resolveGeneric accepts a name that is not in the catalog when the operator
// has configured a way to reach it.
func resolveGeneric(k ProviderKind) Choice {
	if k == ProviderLocalCLI {
		if strings.TrimSpace(os.Getenv("WL_AI_COMMAND")) == "" {
			return Choice{Err: fmt.Errorf("local-cli was requested but WL_AI_COMMAND is not set")}
		}
		return Choice{Kind: ProviderLocalCLI, Model: os.Getenv("WL_AI_MODEL"), Why: "requested explicitly"}
	}
	if strings.TrimSpace(os.Getenv("WL_AI_BASE_URL")) != "" {
		model := os.Getenv("WL_AI_MODEL")
		if model == "" {
			model = "default"
		}
		return Choice{Kind: k, Model: model, Why: "custom OpenAI-compatible endpoint"}
	}
	return Choice{}
}

// normaliseKind accepts the aliases that already exist in the wild.
func normaliseKind(k ProviderKind) ProviderKind {
	k = ProviderKind(strings.ToLower(string(k)))
	if canon, ok := aliasToKind[k]; ok {
		return canon
	}
	return k
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
// AmbiguousError is retained for callers that still surface a choice. The
// operator default no longer produces this: it walks the catalog instead.
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

// Readiness — the difference between "installed" and "will generate"
//
// probeLocalCLI proves a binary exists and runs. That is all `--version` can
// prove, and it was being read as "this will work": discovery offered
// claude-code, a build started, and it stopped with "Not logged in". The same
// machine now demonstrates the failure three ways at once —
//
//	claude   installed, logged in            works
//	grok     installed, logged in, no balance HTTP 402
//	codex    installed, never authenticated   HTTP 401
//
// — and `--version` succeeds for all three. So the walk cannot be decided by
// discovery alone. It is decided here, by asking.
//
// This is NOT moved into Available(): that function backs `weblisk providers`,
// which must stay a listing and not a bill. Readiness is spent only where a
// wrong answer costs a whole build.

// readinessPrompt is the smallest thing that proves a model will answer. It is
// the same sentence RequireProvider uses, so a provider verified here does not
// have to be asked twice — see MarkVerified.
const readinessPrompt = "Respond with exactly: ok"

// readinessTimeout bounds ONE candidate. A provider that cannot say "ok" in
// this long is not the one to hand a hub-generation prompt to, and the walk
// still has other rows to try.
const readinessTimeout = 90 * time.Second

// verifiedKinds records what has already been proved ready in this process, so
// the walk's call and RequireProvider's pre-flight are the same call rather
// than two.
var verifiedKinds = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

// verifiedKey is kind AND model, and the model half is not decoration.
//
// Keyed by kind alone, this said "verified" about a model nothing had asked.
// ChooseProvider and pkg/tenant both apply a --model / spec.Model override
// AFTER the walk has probed — the walk asks claude-code's default, then the
// override replaces the model — and RequireProvider then skipped its pre-flight
// because the KIND had answered. So `--model something-that-does-not-exist`
// sailed past every check and the status line printed "[ready]" for it. Now the
// override simply does not match a verified key, and the pre-flight runs.
func verifiedKey(k ProviderKind, model string) string {
	return string(normaliseKind(k)) + "\x00" + strings.TrimSpace(model)
}

// MarkVerified records that this backend answered for real, on this model.
func MarkVerified(k ProviderKind, model string) {
	verifiedKinds.Lock()
	defer verifiedKinds.Unlock()
	verifiedKinds.m[verifiedKey(k, model)] = true
}

// AlreadyVerified reports whether this exact backend AND model has answered in
// this process.
func AlreadyVerified(k ProviderKind, model string) bool {
	verifiedKinds.Lock()
	defer verifiedKinds.Unlock()
	return verifiedKinds.m[verifiedKey(k, model)]
}

// probeReady is the readiness call, in a variable so tests can walk the
// decision table without a model.
var probeReady = func(kind ProviderKind, model string) error {
	p, err := BuildProvider(kind, model)
	if err != nil {
		return err
	}
	// Deliberately NOT WithTransientRetry: this is a question about whether to
	// use this backend at all, and spending the retry budget on it would turn a
	// three-row walk into several minutes of silence. The build's own retry
	// still applies once a provider is chosen.
	//
	// Bounding it takes three settings, not one, because three different
	// transports get here and each reads a different field:
	//
	//	streaming CLI (claude, grok)  TotalCap / IdleTimeout
	//	plain CLI (codex, local-cli)  Timeout — the other two are read ONLY by
	//	                              runStreaming, so setting them alone left
	//	                              codex bounded by the 10-minute default
	//	HTTP (ollama, the keyed APIs) neither; they use http.DefaultClient,
	//	                              which has no timeout at all
	//
	// So the fields are set where they are read, and the whole probe is then
	// wrapped in a wall-clock guard that covers the transport that has no field
	// to set. A walk that can block forever on its first candidate is worse
	// than the guess it replaced.
	if lp, ok := Underlying(p).(*LocalCLIProvider); ok {
		lp.TotalCap = readinessTimeout
		if lp.IdleTimeout == 0 {
			lp.IdleTimeout = readinessTimeout
		}
		if lp.Timeout == 0 || lp.Timeout > readinessTimeout {
			lp.Timeout = readinessTimeout
		}
	}

	done := make(chan error, 1)
	go func() {
		_, cerr := p.Chat([]Message{{Role: "user", Content: readinessPrompt}})
		done <- cerr
	}()
	timer := time.NewTimer(readinessTimeout + probeGrace)
	defer timer.Stop()
	select {
	case cerr := <-done:
		return cerr
	case <-timer.C:
		// Unblocks the WALK. The goroutine is left to finish on its own — the
		// CLI transports carry their own deadline and will exit; an HTTP one
		// may not, and leaking one request is much cheaper than a build that
		// never starts. Reported as a timeout so notReady keeps the provider:
		// slow is not the same as broken, and this deadline is OURS.
		return &ProviderFault{Provider: string(kind), Status: 408,
			Message: fmt.Sprintf("did not answer a one-word readiness check within %s", readinessTimeout)}
	}
}

// probeGrace lets a transport's own deadline fire first, so the error an
// operator sees is the provider's ("did not run", "404 unknown model") rather
// than this outer guard's, which says only that time passed.
const probeGrace = 5 * time.Second

// notReady decides whether a failed readiness call disqualifies a backend.
//
// Only a PERMANENT fault does. A provider that is merely busy — a 529, a rate
// limit, a stream that dropped — is still the right provider, and the build's
// retry is what handles it. Skipping it here would demote a working default
// because it was overloaded for two seconds.
func notReady(err error) bool {
	if err == nil {
		return false
	}
	if f := FaultOf(err); f != nil {
		return f.Class() == FaultPermanent
	}
	// OUR decision to stop waiting is never a provider condition — transient.go
	// says so in as many words, and the walk has to agree. A readiness probe
	// that hits its own cap, or a stream this process aborted for going quiet,
	// says nothing about whether the backend can generate; disqualifying a
	// healthy provider for being slower than a number we picked is the opposite
	// of what this walk is for. isTransient returns FALSE for both of these
	// (retrying them does not help EITHER), so they have to be named here
	// rather than inferred from it.
	var exhausted *advisoryExhausted
	var idle *idleAbort
	if errors.As(err, &exhausted) || errors.As(err, &idle) {
		return false
	}
	// No structured fault: a subprocess that would not start, a binary that is
	// not really there. Retrying does not fix those either.
	return !isTransient(err)
}

// ResolveReady is Resolve, plus proof.
//
// An explicit request is passed straight through: an operator who pinned a
// backend must get that backend's own error, not a silent walk to a different
// one — which for a data-residency pin would mean sending the tenant's
// blueprints somewhere they were told not to go.
//
// With nobody choosing, each candidate is asked before it is accepted, in
// catalog order, and one that answers with a PERMANENT failure is dropped with
// its reason recorded against it. That is what makes "walk the catalog" mean
// what it says.
func ResolveReady(ctx context.Context, requested string) Choice {
	if strings.TrimSpace(requested) != "" {
		return Resolve(ctx, requested)
	}

	found := Available(ctx)
	var usable []ProviderInfo
	for _, s := range found {
		if s.Available {
			usable = append(usable, s)
		}
	}
	if len(usable) == 0 {
		return chooseFromUsable(usable, found)
	}
	return walkReady(usable)
}

// walkReady is ResolveReady's decision, separated from discovering the machine
// so the table it implements can be tested without one.
func walkReady(usable []ProviderInfo) Choice {
	var rejected []string
	for _, cand := range orderCandidates(usable) {
		err := probeReady(cand.Kind, cand.Model)
		if err == nil {
			MarkVerified(cand.Kind, cand.Model)
			return Choice{Kind: cand.Kind, Model: cand.Model,
				Why:     whyChosen(cand, usable, rejected),
				Options: markUnavailable(usable, rejected)}
		}
		if !notReady(err) {
			// Busy, not broken. Take it and let the build's retry ride it out.
			return Choice{Kind: cand.Kind, Model: cand.Model,
				Why:     whyChosen(cand, usable, rejected) + "; it is busy right now and the build will retry",
				Options: markUnavailable(usable, rejected)}
		}
		reason := skipReason(err)
		fmt.Printf("  Skipping %s — installed, but it will not generate: %s\n", cand.Kind, reason)
		rejected = append(rejected, string(cand.Kind)+": "+reason)
	}

	return Choice{Options: markUnavailable(usable, rejected),
		Err: fmt.Errorf("%w:\n  %s\n\n"+
			"Each line above is that provider's own reason, and they do not all have the\n"+
			"same remedy — a login, a balance, or a key. Fix one, or name a different\n"+
			"provider with --provider.",
			ErrNothingReady, strings.Join(rejected, "\n  "))}
}

// ErrNothingReady marks the walk's own verdict: providers were FOUND, asked,
// and every one of them refused.
//
// It exists so RequireProvider can tell that case apart from "nothing is
// installed". The two need opposite advice, and giving the wrong one is worse
// than giving none: an operator whose grok has no balance and whose codex was
// never signed in was being told to "configure an AI provider — set
// WL_AI_PROVIDER=grok", which is what they already had. The remedy is a login,
// not a variable.
var ErrNothingReady = errors.New("every model provider this machine can reach was asked, and none can generate")

// orderCandidates puts the catalog's ranking over the discovery order, then
// appends anything configured that the catalog does not know about.
func orderCandidates(usable []ProviderInfo) []ProviderInfo {
	var out []ProviderInfo
	seen := map[ProviderKind]bool{}
	for _, want := range preferenceOrder() {
		for _, s := range usable {
			if s.Kind == want && !seen[s.Kind] {
				out = append(out, s)
				seen[s.Kind] = true
			}
		}
	}
	for _, s := range usable {
		if !seen[s.Kind] {
			out = append(out, s)
			seen[s.Kind] = true
		}
	}
	return out
}

func whyChosen(cand ProviderInfo, usable []ProviderInfo, rejected []string) string {
	switch {
	case len(rejected) > 0:
		return fmt.Sprintf("highest-weighted that will actually generate (%d ahead of it could not)", len(rejected))
	case len(usable) == 1:
		return "the only provider available on this machine, and it answered"
	default:
		return "highest-weighted available on this machine, and it answered"
	}
}

// markUnavailable rewrites the options list so a caller printing it says the
// same thing the walk decided, rather than still listing a skipped backend as
// available.
func markUnavailable(usable []ProviderInfo, rejected []string) []ProviderInfo {
	if len(rejected) == 0 {
		return usable
	}
	out := make([]ProviderInfo, len(usable))
	copy(out, usable)
	for i := range out {
		for _, r := range rejected {
			if strings.HasPrefix(r, string(out[i].Kind)+": ") {
				out[i].Available = false
				out[i].Detail = strings.TrimPrefix(r, string(out[i].Kind)+": ")
			}
		}
	}
	return out
}

// skipReason is the one line an operator reads to know what to fix.
//
// Three things went wrong when this was err.Error() cut at the first newline.
// A ProviderFault already prefixes its own name, so the line read "grok: grok:".
// Grok's 402 arrives with embedded newlines, so cutting at the first one
// printed "Internal error: {" and nothing else. And codex writes its banner to
// STDOUT and its failure there too, so the first line was "Reading additional
// input from stdin..." — a progress note presented as the reason a provider
// was rejected. All three measured on 2026-09-09.
func skipReason(err error) string {
	msg := err.Error()
	if f := FaultOf(err); f != nil {
		// The fault's own message, not its Error(): the walk prints the
		// provider's name itself.
		if m := strings.TrimSpace(f.Message); m != "" {
			msg = m
			if f.Status != 0 {
				msg = fmt.Sprintf("HTTP %d — %s", f.Status, m)
			}
		}
	}
	return firstReasonLine(msg)
}

// firstReasonLine reduces a provider's failure to one readable line.
//
// Whitespace is collapsed rather than cut at, because the sentence that
// matters is often not on the first line of what a CLI prints. When the text
// carries an explicit error line, that line wins over whatever came before it.
func firstReasonLine(s string) string {
	if line := salientErrorLine(s); line != "" {
		s = line
	}
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	// By RUNES. These messages are full of non-ASCII — "Not logged in · Please
	// run /login" carries a middle dot, and every provider uses em-dashes — so
	// a byte slice at 200 lands inside a multi-byte rune and the operator's
	// primary diagnostic line ends in U+FFFD.
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

// salientErrorLine finds the line that says what failed, in output that is
// mostly progress. Returns "" when no line stands out, leaving the caller to
// use the whole text.
func salientErrorLine(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= 1 {
		return ""
	}
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		lower := strings.ToLower(l)
		if strings.HasPrefix(lower, "error") || strings.Contains(lower, "error:") ||
			strings.Contains(lower, "unauthorized") || strings.Contains(lower, "not logged in") {
			return l
		}
	}
	return ""
}

// ChooseProvider settles which backend this run uses, before any work starts.
//
// `requested` is a --provider flag or a tenant's stored preference; empty means
// nobody has said, and the highest-weighted backend this workstation can
// actually GENERATE with is the operator default. An explicit name still wins,
// and still fails rather than falling back if that backend cannot be used.
//
// ResolveReady, not Resolve: "available" from discovery means the binary runs,
// and a CLI that is installed but not logged in runs perfectly and generates
// nothing. This is the path a build takes, so this is where it is worth one
// call to find that out first.
func ChooseProvider(requested, model string) error {
	c := ResolveReady(context.Background(), requested)
	if c.Err != nil {
		return c.Err
	}
	if c.Ambiguous {
		// Defensive: Resolve no longer leaves the default path ambiguous, but
		// a caller that constructed a Choice by hand still gets the ranking
		// rather than a prompt that hangs in Studio's pipe.
		k, m := Default(c.Options)
		c = Choice{Kind: k, Model: m, Why: "highest-weighted available on this machine", Options: c.Options}
	}
	if model != "" && model != c.Model {
		// The walk answered on the backend's default. An override replaces the
		// model AFTER that, so "and it answered" would be describing a call
		// that used something else — and RequireProvider's pre-flight, which is
		// keyed on kind AND model, correctly runs again for this one.
		c.Why = strings.TrimSuffix(c.Why, ", and it answered") +
			"; --model overrides what the walk asked, so this one is checked next"
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
	if others := otherAvailable(c.Kind, c.Options); others != "" {
		// "installed", not "available": the walk stopped at the first backend
		// that answered, so these were never asked. Claiming more than was
		// measured is the whole fault this walk exists to fix, and it would be
		// a poor showing to reintroduce it one line below the fix.
		fmt.Printf("  Also installed: %s (not asked — the walk stopped here). Pin one with --provider.\n", others)
	}
	return nil
}

func otherAvailable(picked ProviderKind, options []ProviderInfo) string {
	var names []string
	for _, s := range options {
		if s.Available && s.Kind != picked {
			names = append(names, string(s.Kind))
		}
	}
	return strings.Join(names, ", ")
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
	c := Resolve(context.Background(), "")
	for _, s := range all {
		mark := "·"
		if s.Available {
			mark = "✓"
		}
		where := "hosted"
		if s.Local {
			where = "local"
		}
		tag := ""
		if c.Kind == s.Kind && s.Available && c.Err == nil {
			tag = "  ← default"
		}
		fmt.Printf("  %s %-12s %-16s %s%s\n", mark, s.Kind, s.Label+" ("+where+")", s.Detail, tag)
		if s.Available && s.Model != "" {
			fmt.Printf("      model: %s\n", s.Model)
		}
	}
	fmt.Println()
	switch {
	case c.Err != nil:
		fmt.Printf("  Nothing usable: %v\n", c.Err)
	default:
		// "would START with", not "uses". This listing is deliberately free of
		// model calls — it must stay a listing and not a bill — so it reports
		// the RANKING and says plainly that nothing here has been asked.
		// A build runs ResolveReady, which asks, and will skip this row if it
		// turns out to be installed-but-logged-out. Printing "a build here uses
		// X" from evidence that cannot support it is the same overclaim the
		// walk exists to correct, on the one screen operators are sent to.
		fmt.Printf("  A build here would start with %s", c.Kind)
		if c.Model != "" {
			fmt.Printf(" (%s)", c.Model)
		}
		fmt.Printf(" — %s.\n", c.Why)
		fmt.Println("  Nothing above has been asked to generate: a ✓ means the binary runs or a key")
		fmt.Println("  is present. A build settles it by asking, and moves on if one cannot answer.")
		if others := otherAvailable(c.Kind, c.Options); others != "" {
			fmt.Printf("  Also present: %s. Pin one with --provider, WL_AI_PROVIDER, or per tenant in Studio.\n", others)
		}
	}
	fmt.Println()
	return nil
}
