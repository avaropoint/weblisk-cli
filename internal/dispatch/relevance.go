package dispatch

// Sending each file only the blueprints it needs.
//
// # The cost this removes
//
// Every file's prompt carried the whole corpus — protocol/spec.md,
// architecture/orchestrator.md, protocol/identity.md and the platform blueprint,
// roughly eighteen thousand tokens — on every call. Generating ten files sent it
// ten times. go.mod received the full ML-DSA-65 specification.
//
// Measured on a real run: about two minutes per file, and the prompt is the
// dominant term.
//
// # Why this is filtering and not summarising
//
// Nothing is rewritten or condensed. A blueprint is either sent whole or not
// sent, chosen by what the file's own plan entry says it does. Summarising would
// put a lossy paraphrase between the specification and the implementation, and
// the entire premise is that the blueprint is the source of truth.
//
// The default is INCLUSION. A file whose purpose matches nothing gets everything,
// because sending too much costs time and sending too little costs correctness.

import (
	"os"
	"strings"
)

// blueprintRelevance maps a blueprint to the words that mean a file needs it.
//
// Keyed on what a plan entry says the file DOES, which is the only description
// available before the file exists.
var blueprintRelevance = map[string][]string{
	"protocol/identity.md": {
		"identity", "ml-dsa", "sign", "signature", "key", "token", "wlt",
		"crypt", "argon2", "rotation", "auth", "verify", "replay",
	},
	"architecture/orchestrator.md": {
		"orchestrator", "registration", "register", "namespace", "routing",
		"directory", "channel", "audit", "server", "handler", "endpoint",
		"entry point", "http", "event", "broadcast", "registry",
	},
	"protocol/spec.md": {
		"protocol", "type", "endpoint", "wire", "envelope", "request",
		"response", "error", "handler", "serve", "registration", "register",
		"http", "json", "event", "channel", "audit", "directory",
	},
}

// alwaysSend are blueprints every file needs.
//
// The platform blueprint states the language, the dependency policy and the
// idioms; no file can be written correctly without it.
var alwaysSend = map[string]bool{}

// relevantBlueprints returns the subset of loaded blueprints a file needs.
//
// A file that serves endpoints always gets the protocol; a file that declares
// nothing and serves nothing — a module manifest, a config — gets only the
// platform blueprint.
// filteringEnabled gates the keyword filter. OFF by default.
//
// # Why it is off
//
// The filter withheld blueprints from a file when its plan entry matched no
// keyword in a table written here. That is the tooling deciding what a
// specification means, which is a claim to know better than the specification —
// and every compression of the spec in this pipeline has produced a defect:
// four types named where fifty-five existed, structs collapsed to the word
// "struct", and three blueprints loaded where nine were declared.
//
// It saved real time. So did collapsing struct fields. Correctness first, and
// the cache is what makes the cost bearable: unchanged blueprints are paid for
// once.
//
// Set WL_AI_FILTER_BLUEPRINTS=1 to re-enable, and expect to measure the effect
// on build errors rather than assume it.
func filteringEnabled() bool {
	return os.Getenv("WL_AI_FILTER_BLUEPRINTS") == "1"
}

func relevantBlueprints(f PlannedFile, loaded map[string]string) map[string]string {
	if !filteringEnabled() {
		return loaded
	}
	haystack := strings.ToLower(f.Purpose + " " + f.Path + " " +
		strings.Join(f.Declares, " ") + " " + strings.Join(f.Serves, " "))

	out := map[string]string{}
	for name, body := range loaded {
		if alwaysSend[name] {
			out[name] = body
			continue
		}
		words, known := blueprintRelevance[name]
		if !known {
			// An unrecognised blueprint is sent: inclusion is the safe default.
			out[name] = body
			continue
		}
		// Serving an endpoint means the protocol is needed, whatever the prose says.
		if len(f.Serves) > 0 && name == "protocol/spec.md" {
			out[name] = body
			continue
		}
		for _, w := range words {
			if strings.Contains(haystack, w) {
				out[name] = body
				break
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	// Matched nothing. Two very different cases, and treating them alike sent the
	// cheapest file the most context: a module manifest received the whole
	// ML-DSA-65 specification because it matched no keyword.
	//
	// A file the plan says declares NO symbols and serves NO endpoints has no
	// interface to conform to — the platform blueprint, which is always sent
	// separately, is all it can act on. Anything else matched nothing because
	// this table is incomplete, and there inclusion is the safe default.
	if len(f.Declares) == 0 && len(f.Serves) == 0 {
		return map[string]string{}
	}
	return loaded
}

// joinBlueprints renders a selection, each headed by its source so the model can
// tell which specification a statement came from.
func joinBlueprints(sel map[string]string, order []string) string {
	var b strings.Builder
	for _, name := range order {
		body, ok := sel[name]
		if !ok {
			continue
		}
		b.WriteString("\n--- " + name + " ---\n")
		b.WriteString(body)
		b.WriteString("\n")
	}
	return b.String()
}

// without returns order with one name removed.
//
// Used to keep a blueprint out of the corpus join when it is already sent as
// its own labelled section. Operates on the ORDER rather than the map so the
// caller's map stays shared and unmutated — two prompts are built from the same
// map in one run.
func without(order []string, name string) []string {
	out := make([]string, 0, len(order))
	for _, n := range order {
		if n != name {
			out = append(out, n)
		}
	}
	return out
}
