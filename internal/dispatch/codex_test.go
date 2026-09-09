package dispatch

import (
	"strings"
	"testing"
)

// fakeCLI is the shell stand-in from localcli_stream_test.go: a real
// subprocess, because that is what this code drives.

// codexLike reproduces what codex-cli 0.153.4 actually does, measured with the
// streams SEPARATED: the banner and the `model:` line go to stderr, the answer
// goes to stdout AND to the --output-last-message path, and the prompt arrives
// on stdin because the positional argument is "-".
const codexLike = `
answer_file=""
prompt_is_stdin=0
prev=""
for a in "$@"; do
  if [ "$prev" = "--output-last-message" ]; then answer_file="$a"; fi
  if [ "$a" = "-" ]; then prompt_is_stdin=1; fi
  prev="$a"
done
prompt=$(cat)
echo "OpenAI Codex v0.153.4" >&2
echo "--------" >&2
echo "workdir: /somewhere" >&2
echo "model: gpt-6-astra" >&2
echo "provider: openai" >&2
echo "sandbox: read-only" >&2
echo "prompt_bytes=${#prompt} stdin=${prompt_is_stdin}" >&2
out="ANSWER_ONLY"
[ "$prompt_is_stdin" = "1" ] || out="PROMPT_WAS_NOT_ON_STDIN"
[ -n "$answer_file" ] && printf '%s' "$out" > "$answer_file"
printf '%s\n' "$out"
`

func codexProvider(bin string) *LocalCLIProvider {
	return &LocalCLIProvider{
		Bin: bin, Name: "codex", Args: []string{"exec"}, JSON: false,
		OutputFileFlag: "--output-last-message",
		PromptStdin:    true, PromptArg: "-",
	}
}

// The whole codex contract our side depends on, without an OpenAI account.
// The real binary was driven through this same path against a mocked model
// endpoint; this keeps it honest in CI, where codex is not installed.
func TestCodexAnswerComesFromTheFileAndTheModelFromStderr(t *testing.T) {
	p := codexProvider(fakeCLI(t, codexLike))

	got, err := p.Chat([]Message{{Role: "user", Content: "say something"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got != "ANSWER_ONLY" {
		t.Errorf("answer = %q, want ANSWER_ONLY — the prompt must reach the tool on stdin", got)
	}
	// The banner is on stderr and must not reach the caller.
	if strings.Contains(got, "OpenAI Codex") || strings.Contains(got, "workdir") {
		t.Errorf("the preamble leaked into the completion: %q", got)
	}
	// And the model, which the answer file cannot carry, is recovered from it.
	if m := p.ModelUsed(); m != "gpt-6-astra" {
		t.Errorf("ModelUsed = %q, want gpt-6-astra — without it a codex-generated "+
			"hub records no model, and provenance exists to answer exactly that", m)
	}
}

// A build of the tool that does not write the file must degrade to stdout, not
// fail. This path was briefly a hard error, on the mistaken belief that stdout
// held a banner; it does not.
func TestCodexFallsBackToStdoutWhenNoAnswerFileAppears(t *testing.T) {
	p := codexProvider(fakeCLI(t, `
cat > /dev/null
echo "banner" >&2
printf 'STDOUT_ANSWER\n'
`))
	got, err := p.Chat([]Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("a tool that answered on stdout was treated as a failure: %v", err)
	}
	if got != "STDOUT_ANSWER" {
		t.Errorf("answer = %q, want STDOUT_ANSWER", got)
	}
}

// The reason the prompt goes on stdin at all: Linux caps a SINGLE argv entry at
// MAX_ARG_STRLEN (128 KiB) however large ARG_MAX is. Measured against the real
// binary: a 200 KB prompt as an argument fails with "Argument list too long"
// before codex starts; on stdin it runs.
func TestALargePromptSurvivesTheArgvLimit(t *testing.T) {
	p := codexProvider(fakeCLI(t, codexLike))
	big := strings.Repeat("x", 300*1024) // well past MAX_ARG_STRLEN

	got, err := p.Chat([]Message{{Role: "user", Content: big}})
	if err != nil {
		t.Fatalf("a %d-byte prompt failed: %v", len(big), err)
	}
	if got != "ANSWER_ONLY" {
		t.Errorf("answer = %q — a prompt this size must not go on argv", got)
	}
}

// modelFromTranscript must not pick up the prompt echoed back later in the
// stream: a prompt about model gateways would otherwise nominate itself.
func TestTheModelIsReadFromThePreambleOnly(t *testing.T) {
	if got := modelFromTranscript("OpenAI Codex\n--------\nmodel: gpt-6-astra\nprovider: openai\n"); got != "gpt-6-astra" {
		t.Errorf("preamble model = %q, want gpt-6-astra", got)
	}
	late := strings.Repeat("noise\n", 40) + "model: not-the-model\n"
	if got := modelFromTranscript(late); got != "" {
		t.Errorf("a `model:` line far past the preamble was believed: %q", got)
	}
	if got := modelFromTranscript("model: two words here\n"); got != "" {
		t.Errorf("a prose line was read as a model id: %q", got)
	}
	if got := modelFromTranscript("no model line at all\n"); got != "" {
		t.Errorf("invented a model: %q", got)
	}
}
