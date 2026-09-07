package tenant

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The orchestrator must be STARTED before a credential is claimed against it.
//
// wserver.Provision establishes a credential against a tenant that is already
// running, and refuses one that is not. The provision step announced that it
// was "starting the hub and claiming the first operator" and then called
// Provision with nothing in between, so `weblisk tenant create` failed at its
// last step on every fresh directory — after the entire generation, tens of
// minutes, had succeeded.
//
// Checked at the source rather than by driving Create, because driving Create
// needs a model provider, a corpus and forty minutes. This is a question about
// call order and can be answered by reading.
func TestTheOrchestratorIsStartedBeforeItIsClaimed(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "tenant.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	var run *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "run" {
			run = fn
		}
	}
	if run == nil {
		t.Fatal("no run() in tenant.go — this guard's subject is gone")
	}

	// Positions, so the ORDER is what is asserted and not merely the presence
	// of both calls. Both being present in the wrong order is the bug.
	var startPos, provisionPos token.Pos
	ast.Inspect(run.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "wserver" {
			return true
		}
		switch sel.Sel.Name {
		case "StartDetached":
			if !startPos.IsValid() {
				startPos = call.Pos()
			}
		case "Provision":
			if !provisionPos.IsValid() {
				provisionPos = call.Pos()
			}
		}
		return true
	})

	if !provisionPos.IsValid() {
		t.Fatal("run() does not call wserver.Provision — the credential is never claimed")
	}
	if !startPos.IsValid() {
		t.Fatal("run() never calls wserver.StartDetached, so nothing starts the orchestrator — " +
			"Provision will refuse every fresh tenant with 'the orchestrator is not running'")
	}
	if startPos > provisionPos {
		t.Errorf("the orchestrator is started at line %d, after the credential is claimed at line %d",
			fset.Position(startPos).Line, fset.Position(provisionPos).Line)
	}
}

// And the step must wait for it to LISTEN, not merely to have been spawned.
// Detached means started; Provision's first act is an HTTP request, and a
// socket that is not open yet fails for a tenant seconds from being fine.
//
// On the AST, not on the text. The first version of this used strings.Index and
// found "wserver.Provision" in a DOC COMMENT that appears above the call to
// StartDetached — so its ordering precondition looked violated, it skipped, and
// a skip reads as a pass in the summary line. It proved nothing and said so in
// small print.
func TestTheStepWaitsForTheOrchestratorToListen(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "tenant.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var run *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "run" {
			run = fn
		}
	}
	if run == nil {
		t.Fatal("no run() in tenant.go")
	}

	var startPos, provisionPos, waitPos token.Pos
	ast.Inspect(run.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "wserver" {
				if fn.Sel.Name == "StartDetached" && !startPos.IsValid() {
					startPos = call.Pos()
				}
				if fn.Sel.Name == "Provision" && !provisionPos.IsValid() {
					provisionPos = call.Pos()
				}
			}
		case *ast.Ident:
			if fn.Name == "waitForOrchestrator" && !waitPos.IsValid() {
				waitPos = call.Pos()
			}
		}
		return true
	})

	if !startPos.IsValid() || !provisionPos.IsValid() {
		t.Fatal("the order test covers this; both calls must be present")
	}
	if !waitPos.IsValid() {
		t.Fatal("nothing waits for the orchestrator to listen — Provision's first act is an HTTP request")
	}
	if waitPos < startPos || waitPos > provisionPos {
		t.Errorf("waitForOrchestrator is at line %d; it must sit between the start (line %d) and the claim (line %d)",
			fset.Position(waitPos).Line, fset.Position(startPos).Line, fset.Position(provisionPos).Line)
	}
}

// A recorded run whose process is gone is a CRASH and is reported immediately.
// Waiting it out delays reaching the log, which is the diagnosis.
func TestACrashedOrchestratorIsNotWaitedOut(t *testing.T) {
	root := t.TempDir()
	// A run state naming a pid nothing holds. StatusOf reports Recorded and not
	// Running, which is Stale.
	runDir := filepath.Join(root, ".weblisk", "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := `{"component":"orchestrator","pid":2147483646,"port":9800,` +
		`"address":"http://127.0.0.1:9800","started_at":"2020-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(runDir, "orchestrator.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}

	began := time.Now()
	err := waitForOrchestrator(context.Background(), root)
	if err == nil {
		t.Fatal("a crashed orchestrator was reported as running")
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("took %s to report a crash — it is known immediately from the run state", took)
	}
	if !strings.Contains(err.Error(), "logs") {
		t.Errorf("the error does not point at the log, which is the diagnosis: %v", err)
	}
}

// A cancelled context ends the wait rather than holding the operation for the
// full timeout.
func TestWaitingHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForOrchestrator(ctx, t.TempDir()); err == nil {
		t.Fatal("a cancelled wait returned success")
	}
}

// The default must match `weblisk server start`'s, or a tenant created with no
// port lands where none of the other commands look for it.
func TestTheDefaultPortMatchesTheServerCommand(t *testing.T) {
	if defaultOrchestratorPort != 9800 {
		t.Errorf("default port is %d; `weblisk server start` defaults to 9800", defaultOrchestratorPort)
	}
}
