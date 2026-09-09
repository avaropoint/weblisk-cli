package dispatch

// modelgw.go — after generation, the hub must serve its own model routes.
//
// Studio is a client of those routes. A hub that was generated before they
// existed answers 404, and Studio must not fall back to its own llm.json.
// Writing the gateway into the orchestrator package is deterministic — it
// does not wait for a model to emit the handlers from a blueprint.

import (
	"bytes"
	_ "embed"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

const modelGatewayFile = "model_gateway.go"

//go:embed modelgw_src/model_gateway.go.txt
var modelGatewaySrc string

// InstallModelGateway writes the hub's model HTTP surface into the generated
// orchestrator and wraps Handler() so the routes are reached.
func InstallModelGateway(root string) error {
	dir, err := findOrchestratorDir(root)
	if err != nil {
		return err
	}
	dest := filepath.Join(dir, modelGatewayFile)
	if err := os.WriteFile(dest, []byte(modelGatewaySrc), 0o644); err != nil {
		return fmt.Errorf("writing model gateway: %w", err)
	}
	return patchOrchestratorHandler(dir)
}

func findOrchestratorDir(root string) (string, error) {
	var found string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "vendor" || info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.PackageClauseOnly)
		if perr != nil || f.Name.Name != "orchestrator" {
			return nil
		}
		found = filepath.Dir(path)
		return filepath.SkipAll
	})
	if found == "" {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("no package orchestrator in %s", root)
	}
	return found, nil
}

func patchOrchestratorHandler(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	needle := []byte("var h http.Handler = o.Routes()")
	repl := []byte("var h http.Handler = wrapModelGateway(o.Routes())")
	for _, e := range entries {
		if e.IsDir() || e.Name() == modelGatewayFile || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if bytes.Contains(body, []byte("wrapModelGateway(o.Routes())")) {
			return nil
		}
		if !bytes.Contains(body, needle) {
			continue
		}
		patched := bytes.Replace(body, needle, repl, 1)
		return os.WriteFile(path, patched, 0o644)
	}
	return fmt.Errorf("could not find Orchestrator.Handler to wrap in %s", dir)
}
