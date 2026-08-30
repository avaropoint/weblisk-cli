package dispatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProvider replays scripted responses, so contract enforcement can be tested
// without spending model calls — and deterministically, which a real model
// cannot be.
type fakeProvider struct {
	responses []string
	calls     int
	prompts   []string
}

func (f *fakeProvider) Chat(msgs []Message) (string, error) {
	for _, m := range msgs {
		if m.Role == "user" {
			f.prompts = append(f.prompts, m.Content)
		}
	}
	if f.calls >= len(f.responses) {
		return "", fmt.Errorf("no scripted response for call %d", f.calls+1)
	}
	r := f.responses[f.calls]
	f.calls++
	return r, nil
}

func oneFilePlan() *Plan {
	return &Plan{
		Target: "orchestrator", Root: "server", Build: "go build ./...",
		Files: []PlannedFile{{
			Path: "main.go", Purpose: "Entry point",
			Declares: []string{"func main"},
			Serves:   []string{"GET /v1/health"},
		}},
	}
}

func TestProseIsRejectedAndRetried(t *testing.T) {
	// The failure that produced "AI returned no code files" after 45 minutes,
	// now caught on the first response and named.
	good := "package main\n\nfunc main() { http.HandleFunc(\"/v1/health\", nil) }\n"
	p := &fakeProvider{responses: []string{
		"Here is the main.go file you requested:\n\n" + good,
		good,
	}}
	root := t.TempDir()
	var retried string
	_, err := GenerateTarget(p, oneFilePlan(), "go", "specs", "platform", root, func(pr Progress) {
		if pr.Status == "retrying" {
			retried = pr.Detail
		}
	}, nil)
	if err != nil {
		t.Fatalf("generation failed: %v", err)
	}
	if p.calls != 2 {
		t.Errorf("made %d calls, want 2 (one rejected, one accepted)", p.calls)
	}
	if !strings.Contains(retried, "prose") {
		t.Errorf("retry did not name the reason: %q", retried)
	}
	got, _ := os.ReadFile(filepath.Join(root, "server", "main.go"))
	if !strings.Contains(string(got), "func main") {
		t.Errorf("wrong content written: %q", got)
	}
}

func TestMissingRequiredSymbolIsRejected(t *testing.T) {
	// Structural checking before build: a file that compiles but omits what the
	// manifest requires would otherwise surface as a conformance failure much
	// later.
	p := &fakeProvider{responses: []string{
		"package main\n// no main function, no health route\n",
		"package main\n// still wrong\n",
		"package main\n// wrong a third time\n",
	}}
	_, err := GenerateTarget(p, oneFilePlan(), "go", "s", "p", t.TempDir(), nil, nil)
	if err == nil {
		t.Fatal("a file missing its required symbol was accepted")
	}
	if !strings.Contains(err.Error(), "main.go") {
		t.Errorf("failure does not name the file: %v", err)
	}
	if p.calls != maxFileAttempts {
		t.Errorf("made %d attempts, want %d", p.calls, maxFileAttempts)
	}
}

func TestFencedOutputIsAccepted(t *testing.T) {
	// Models fence code by reflex. Rejecting an otherwise-correct response over
	// formatting would fail for the wrong reason.
	body := "package main\n\nfunc main() { _ = \"/v1/health\" }\n"
	p := &fakeProvider{responses: []string{"```go\n" + body + "```"}}
	root := t.TempDir()
	if _, err := GenerateTarget(p, oneFilePlan(), "go", "s", "p", root, nil, nil); err != nil {
		t.Fatalf("fenced output was rejected: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "server", "main.go"))
	if strings.Contains(string(got), "```") {
		t.Error("the fence was written into the file")
	}
}

func TestNothingIsWrittenUntilEveryFileSucceeds(t *testing.T) {
	// A half-written target looks like a build to fix rather than a run to
	// repeat.
	target := &Plan{
		Target: "orchestrator", Root: "server", Build: "go build",
		Files: []PlannedFile{
			{Path: "a.go", Purpose: "first", Declares: []string{"AAA"}},
			{Path: "b.go", Purpose: "second", Declares: []string{"BBB"}},
		},
	}
	p := &fakeProvider{responses: []string{
		"package main\n// AAA\n",
		"package main\n// wrong\n", "package main\n// wrong\n", "package main\n// wrong\n",
	}}
	root := t.TempDir()
	if _, err := GenerateTarget(p, target, "go", "s", "p", root, nil, nil); err == nil {
		t.Fatal("generation reported success despite a failed file")
	}
	if _, err := os.Stat(filepath.Join(root, "server", "a.go")); err == nil {
		t.Error("the successful file was written even though the target failed")
	}
}

func TestEachFileIsToldWhatAlreadyExists(t *testing.T) {
	// Without this, every file redeclares the shared types and nothing compiles.
	target := &Plan{
		Target: "orchestrator", Root: "server", Build: "go build",
		Files: []PlannedFile{
			{Path: "protocol.go", Purpose: "types"},
			{Path: "main.go", Purpose: "entry", DependsOn: []string{"protocol.go"}},
		},
	}
	p := &fakeProvider{responses: []string{"package main\n", "package main\n"}}
	if _, err := GenerateTarget(p, target, "go", "s", "p", t.TempDir(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(p.prompts) != 2 {
		t.Fatalf("got %d prompts", len(p.prompts))
	}
	if strings.Contains(p.prompts[0], "Already generated") {
		t.Error("the first file was told about files that did not exist yet")
	}
	if !strings.Contains(p.prompts[1], "protocol.go") {
		t.Error("the second file was not told protocol.go already exists")
	}
}

func TestProgressReportsEveryFile(t *testing.T) {
	p := &fakeProvider{responses: []string{"package main\n\nfunc main() { _ = \"/v1/health\" }\n"}}
	var steps []string
	if _, err := GenerateTarget(p, oneFilePlan(), "go", "s", "p", t.TempDir(), func(pr Progress) {
		steps = append(steps, pr.Status)
	}, nil); err != nil {
		t.Fatal(err)
	}
	if len(steps) < 2 || steps[len(steps)-1] != "written" {
		t.Errorf("progress did not report generating then written: %v", steps)
	}
}

// TestFrontmatterIsRejectedAsNotSource reproduces a real failure: one file came
// back as a YAML document whose prose happened to contain the required symbol
// names, so every other check passed and the file was written.
func TestFrontmatterIsRejectedAsNotSource(t *testing.T) {
	frontmatter := "---\nname: weblisk-events-file\ndescription: Notes on generating events. " +
		"It declares PublishSystemEvent and BroadcastDirectory.\n---\n"
	good := "package main\n\nfunc PublishSystemEvent() {}\nfunc BroadcastDirectory() {}\n"
	p := &fakeProvider{responses: []string{frontmatter, good}}
	plan := &Plan{Root: "server", Files: []PlannedFile{{
		Path: "events.go", Purpose: "events",
		Declares: []string{"PublishSystemEvent", "BroadcastDirectory"},
	}}}
	var retried string
	root := t.TempDir()
	if _, err := GenerateTarget(p, plan, "go", "s", "p", root, func(pr Progress) {
		if pr.Status == "retrying" {
			retried = pr.Detail
		}
	}, nil); err != nil {
		t.Fatalf("generation failed: %v", err)
	}
	if !strings.Contains(retried, "frontmatter") {
		t.Errorf("the rejection did not name the reason: %q", retried)
	}
	got, _ := os.ReadFile(filepath.Join(root, "server", "events.go"))
	if strings.HasPrefix(string(got), "---") {
		t.Error("a YAML document was written as Go source")
	}
}

func TestNonGoContentIsRejectedForAGoPath(t *testing.T) {
	p := &fakeProvider{responses: []string{
		"// just a comment, no package clause\nfunc main() {}\n",
		"package main\n\nfunc main() {}\n",
	}}
	plan := &Plan{Root: "server", Files: []PlannedFile{{Path: "main.go", Purpose: "entry"}}}
	if _, err := GenerateTarget(p, plan, "go", "s", "p", t.TempDir(), nil, nil); err != nil {
		t.Fatalf("valid Go on retry was rejected: %v", err)
	}
	if p.calls != 2 {
		t.Errorf("made %d calls, want 2 — the packageless response should be rejected", p.calls)
	}
}

func TestAGoFileMayOpenWithADocComment(t *testing.T) {
	// The shape check must not reject a file that legitimately opens with a
	// comment block, which generated files routinely do.
	body := "// Package main implements the orchestrator.\n//\n// Long notes.\npackage main\n\nfunc main() {}\n"
	p := &fakeProvider{responses: []string{body}}
	plan := &Plan{Root: "server", Files: []PlannedFile{{Path: "main.go", Purpose: "entry"}}}
	if _, err := GenerateTarget(p, plan, "go", "s", "p", t.TempDir(), nil, nil); err != nil {
		t.Fatalf("a doc-commented file was rejected: %v", err)
	}
	if p.calls != 1 {
		t.Errorf("made %d calls; a valid file should be accepted first time", p.calls)
	}
}

// TestMethodNotationIsMatchedByParsing reproduces the run that failed three
// times on correct code: a plan declares "(ScopeLevel).Valid" and Go source
// writes "func (s ScopeLevel) Valid() bool", which shares no substring with it.
func TestMethodNotationIsMatchedByParsing(t *testing.T) {
	src := `package main

type ScopeLevel int

func (s ScopeLevel) Valid() bool { return true }

func NewThing() *ScopeLevel { return nil }
`
	f := PlannedFile{Path: "protocol.go", Declares: []string{"(ScopeLevel).Valid", "ScopeLevel", "NewThing"}}
	if v := contractViolation(src, f); v != "" {
		t.Errorf("correct source was rejected: %s", v)
	}
}

func TestAMethodOnTheWrongTypeIsNotAccepted(t *testing.T) {
	// A bare-name match would accept this, and it is a different promise.
	src := "package main\n\ntype Other int\n\nfunc (o Other) Valid() bool { return true }\n"
	f := PlannedFile{Path: "protocol.go", Declares: []string{"(ScopeLevel).Valid"}}
	if v := contractViolation(src, f); v == "" {
		t.Error("a method declared on the wrong type satisfied the contract")
	}
}

func TestAGenuinelyMissingSymbolIsStillCaught(t *testing.T) {
	src := "package main\n\ntype ScopeLevel int\n"
	f := PlannedFile{Path: "protocol.go", Declares: []string{"(ScopeLevel).Valid"}}
	v := contractViolation(src, f)
	if v == "" {
		t.Fatal("a missing method was accepted")
	}
	if !strings.Contains(v, "ScopeLevel") {
		t.Errorf("the complaint does not name the symbol: %s", v)
	}
}

func TestFuncPrefixNotationIsTolerated(t *testing.T) {
	// Plans sometimes write "func main" rather than "main".
	src := "package main\n\nfunc main() {}\n"
	if v := contractViolation(src, PlannedFile{Path: "main.go", Declares: []string{"func main"}}); v != "" {
		t.Errorf("correct source was rejected: %s", v)
	}
}
