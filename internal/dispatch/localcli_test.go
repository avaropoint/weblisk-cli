package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSplitArgsHonoursQuotedValues(t *testing.T) {
	// A flag value containing a space is the reason this is not strings.Fields.
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"--flag value", []string{"--flag", "value"}},
		{`--system "you are a hub generator"`, []string{"--system", "you are a hub generator"}},
		{"  --a   --b  ", []string{"--a", "--b"}},
		{`--p "a b" --q c`, []string{"--p", "a b", "--q", "c"}},
	}
	for _, c := range cases {
		got := splitArgs(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("splitArgs(%q) = %q, want %q", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitArgs(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestAnExplicitPathIsCheckedRatherThanTrusted(t *testing.T) {
	// The rule is "honour what the operator typed, never substitute" — but a
	// path that cannot execute must fail where it was set, not as an exec error
	// several steps later.
	missing := filepath.Join(t.TempDir(), "nope")
	if bin, _ := ResolveLocalCLI("claude", missing); bin != "" {
		t.Errorf("a non-existent absolute path resolved to %q", bin)
	}

	dir := t.TempDir()
	if bin, _ := ResolveLocalCLI("claude", dir); bin != "" {
		t.Errorf("a directory resolved as an executable: %q", bin)
	}

	notExec := filepath.Join(dir, "tool")
	if err := os.WriteFile(notExec, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if bin, _ := ResolveLocalCLI("claude", notExec); bin != "" {
		t.Errorf("a non-executable file resolved: %q", bin)
	}

	if err := os.Chmod(notExec, 0o755); err != nil {
		t.Fatal(err)
	}
	if bin, _ := ResolveLocalCLI("claude", notExec); bin != notExec {
		t.Errorf("an executable file did not resolve: got %q, want %q", bin, notExec)
	}
}

func TestResolveReportsWhereItLooked(t *testing.T) {
	// The failure this exists for: a tool installed in ~/.local/bin is invisible
	// to a process that did not inherit a login shell's PATH, and "not
	// installed" is then a lie. The message has to show the search path.
	bin, searched := ResolveLocalCLI("definitely-not-a-real-tool-xyz", "")
	if bin != "" {
		t.Fatalf("a nonexistent tool resolved to %q", bin)
	}
	if len(searched) == 0 {
		t.Fatal("resolution failed without reporting anywhere it looked")
	}
	found := false
	for _, p := range searched {
		if filepath.Base(filepath.Dir(p)) == "bin" || filepath.Base(p) != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("search list looks wrong: %q", searched)
	}
}

func TestFlattenMessagesLabelsOnlyMultiTurn(t *testing.T) {
	// A single-turn prompt must not be decorated with a role header — that text
	// reaches the model and changes the output.
	one := flattenMessages([]Message{{Role: "user", Content: "generate the hub"}})
	if one != "generate the hub" {
		t.Errorf("single message = %q, want it unlabelled", one)
	}

	two := flattenMessages([]Message{
		{Role: "system", Content: "be terse"},
		{Role: "user", Content: "go"},
	})
	if two != "System:\nbe terse\n\nUser:\ngo" {
		t.Errorf("multi-turn flattening = %q", two)
	}
}

func TestTimeoutFallsBackRatherThanFailing(t *testing.T) {
	// A malformed duration must not break the command; the default is a safer
	// outcome than refusing to run over a setting.
	for _, bad := range []string{"", "   ", "nonsense", "-5m", "0"} {
		if d := parseTimeoutEnv(bad); d != 0 {
			t.Errorf("parseTimeoutEnv(%q) = %v, want 0 (use default)", bad, d)
		}
	}
	if d := parseTimeoutEnv("90s"); d != 90*time.Second {
		t.Errorf("parseTimeoutEnv(90s) = %v", d)
	}
}

func TestLocalProvidersAreSelectableAndNeedNoKey(t *testing.T) {
	// The point of these providers: generating a hub must not require a paid
	// account. Selecting one must not demand WL_AI_KEY.
	for _, k := range []string{"WL_AI_PROVIDER", "WL_AI_KEY", "WL_AI_COMMAND", "WL_AI_MODEL", "WL_AI_BASE_URL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("WL_AI_PROVIDER", "local-cli")
	t.Setenv("WL_AI_COMMAND", "")
	_, err := NewProvider()
	if err == nil {
		t.Fatal("local-cli with no WL_AI_COMMAND was accepted")
	}
	// Assert the REASON, not merely that something failed. Without the explicit
	// guard an empty command still errors — filepath.Base("") is "." and that
	// resolves to nothing — so a test that only checks err != nil passes against
	// a build with the guard deleted, and says nothing.
	if !strings.Contains(err.Error(), "WL_AI_COMMAND is required") {
		t.Errorf("refusal did not name the missing setting: %v", err)
	}

	// A real executable is accepted with no key set anywhere.
	dir := t.TempDir()
	tool := filepath.Join(dir, "mytool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WL_AI_COMMAND", tool)
	p, err := NewProvider()
	if err != nil {
		t.Fatalf("local-cli with a valid command was refused: %v", err)
	}
	// Every provider is built with transient-failure retry applied, so what
	// comes back is the wrapper. That is asserted too: a provider handed out
	// without retry is how five tenant builds were lost.
	if _, wrapped := p.(*retryingProvider); !wrapped {
		t.Fatalf("NewProvider returned %T, which does not retry transient failures", p)
	}
	lp, ok := Underlying(p).(*LocalCLIProvider)
	if !ok {
		t.Fatalf("local-cli built %T, want *LocalCLIProvider", Underlying(p))
	}
	if lp.Bin != tool {
		t.Errorf("bin = %q, want %q", lp.Bin, tool)
	}
}

func TestHostedProvidersStillRequireTheirKey(t *testing.T) {
	// The local additions must not have loosened the hosted paths.
	for _, k := range []string{"WL_AI_KEY", "WL_AI_BASE_URL", "WL_AI_MODEL", "WL_AI_COMMAND"} {
		os.Unsetenv(k)
	}
	for _, provider := range []string{"openai", "anthropic"} {
		t.Setenv("WL_AI_PROVIDER", provider)
		t.Setenv("WL_AI_KEY", "")
		os.Unsetenv("WL_AI_KEY")
		if _, err := NewProvider(); err == nil {
			t.Errorf("%s was accepted with no WL_AI_KEY", provider)
		}
	}
}
