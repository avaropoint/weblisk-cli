package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A component is never shown its own previous output as existing code.
//
// After one content build, internal/content held 340 exported names. Because
// self-owned files are excluded from Owned, the package rendered with no owner
// — which reads as hand-written, the most protected category — and the prompt
// told the next content build not to re-declare its own types.
func TestAComponentIsNotShownItsOwnPreviousOutput(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module hubgen\n\ngo 1.22\n")
	write("internal/protocol/types.go", "package protocol\n\ntype ErrorResponse struct{}\n")
	write("internal/content/custody.go", "package content\n\ntype ContentRepository struct{}\n")
	write("cmd/content/main.go", "package main\n\nfunc main() {}\n")

	// The orchestrator's manifest claims the protocol package.
	m, _ := json.Marshal(writtenManifest{Target: "orchestrator", Root: ".",
		Files: []string{"internal/protocol/types.go"}})
	write(filepath.Join(cacheDirName, "written-"+"aaaaaaaaaaaa"+".json"), string(m))
	os.Rename(filepath.Join(root, cacheDirName, "written-aaaaaaaaaaaa.json"),
		manifestName(root, "orchestrator"))

	st := ReadTenantState(root, "content")

	if st.Module != "hubgen" {
		t.Errorf("module = %q, want hubgen", st.Module)
	}
	surface := st.FormatTenantPackages()
	if !strings.Contains(surface, "internal/protocol") || !strings.Contains(surface, "ErrorResponse") {
		t.Error("another component's importable surface is missing — files will guess its symbols")
	}
	for _, mine := range []string{"internal/content", "cmd/content", "ContentRepository"} {
		if strings.Contains(surface, mine) {
			t.Errorf("the content build is shown its own %q as pre-existing code", mine)
		}
	}
}
