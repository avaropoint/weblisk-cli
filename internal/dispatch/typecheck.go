package dispatch

// typecheck.go — finding a fault while it is still cheap to fix.
//
// # The gap this closes
//
// Per-file checking was deliberately shallow. contractViolation's own comment
// says it is "a fast pre-filter, not the authority — the COMPILER is", and
// justifies its permissiveness with "a false acceptance is caught by the build
// minutes later".
//
// The build is not minutes later. It runs once, after EVERY file is generated:
// twenty-four files at roughly three minutes each, so a type error in file four
// surfaces seventy minutes after it was introduced. Worse than the wait, a run
// that long does not fit inside one provider session window — three runs died
// on a session limit before one completed — so the compiler was never reached
// at all, and the error was never found on those runs rather than found late.
//
// Two checks, at the two moments the answer becomes knowable:
//
//	syntax   the moment a file's text exists. It needs nothing else.
//	types    the moment a PACKAGE's last planned file exists. Not before:
//	         a package half-generated is legitimately full of undefined
//	         symbols, and reporting those would be reporting our own
//	         incompleteness as the model's error.
//
// # Why an overlay rather than writing the files
//
// Generation writes nothing to disk until every file is produced, and that is a
// safety property worth keeping: a run that dies part-way leaves the previous,
// working tenant exactly as it was. Type-checking by writing files early would
// trade that away.
//
// `go build -overlay` exists for this. The generated content lives in a temp
// directory and is mapped over the tenant's paths for the duration of one
// command; the tenant itself is never touched.

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// syntaxFault parses generated source and returns the first error, or "".
//
// Reported as a contract violation so it is fed back on the SAME file's next
// attempt, with the line, while the model still has the file in hand. It cost
// a full run to discover otherwise.
//
// Only for languages this binary can parse. A TypeScript syntax error is a real
// fault and not one to guess at from Go's parser.
func syntaxFault(filePath, content string) string {
	if strings.ToLower(pathExt(filePath)) != ".go" {
		return ""
	}
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, filePath, content, parser.SkipObjectResolution); err != nil {
		// The first error only. A parser cascades after the first real fault,
		// and a wall of positions is less actionable than one.
		msg := err.Error()
		if i := strings.Index(msg, "\n"); i > 0 {
			msg = msg[:i]
		}
		return "the file does not parse: " + strings.TrimSpace(msg)
	}
	return ""
}

// packageGate reports when a package's last planned file has been generated.
//
// Type-checking earlier than that reports our own incompleteness: a package
// whose second of five files just arrived is legitimately full of symbols the
// remaining three will declare.
type packageGate struct {
	remaining map[string]int // package dir -> planned files not yet generated
}

func newPackageGate(plan *Plan) *packageGate {
	g := &packageGate{remaining: map[string]int{}}
	for _, f := range plan.Files {
		if strings.ToLower(pathExt(f.Path)) != ".go" {
			continue
		}
		g.remaining[path.Dir(filepath.ToSlash(f.Path))]++
	}
	return g
}

// done records a generated file and returns the package to check, or "" if that
// package is not complete yet.
func (g *packageGate) done(filePath string) string {
	if strings.ToLower(pathExt(filePath)) != ".go" {
		return ""
	}
	dir := path.Dir(filepath.ToSlash(filePath))
	n, tracked := g.remaining[dir]
	if !tracked || n == 0 {
		return ""
	}
	g.remaining[dir] = n - 1
	if n-1 > 0 {
		return ""
	}
	return dir
}

// typeCheckPackage compiles one package from generated content, without writing
// any of it into the tenant.
//
// Returns the compiler's complaint, or "" when the package is sound. An error
// from the mechanism itself — no toolchain, no temp directory — returns "" too:
// this is a fast-feedback path, and a tool that cannot run must not be reported
// as the artifact being wrong. The build at the end is still the authority.
func typeCheckPackage(root, pkgDir string, files []GeneratedFile) string {
	overlayDir, err := os.MkdirTemp("", "weblisk-overlay-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(overlayDir)

	replace := map[string]string{}
	for i, f := range files {
		if strings.ToLower(pathExt(f.Path)) != ".go" {
			continue
		}
		tmp := filepath.Join(overlayDir, fmt.Sprintf("%03d-%s", i, filepath.Base(f.Path)))
		if werr := os.WriteFile(tmp, []byte(f.Content), 0o644); werr != nil {
			return ""
		}
		replace[filepath.Join(root, filepath.FromSlash(f.Path))] = tmp
	}
	if len(replace) == 0 {
		return ""
	}
	spec, merr := json.Marshal(map[string]any{"Replace": replace})
	if merr != nil {
		return ""
	}
	overlayFile := filepath.Join(overlayDir, "overlay.json")
	if werr := os.WriteFile(overlayFile, spec, 0o644); werr != nil {
		return ""
	}

	// `go build` rather than `go vet`: this asks whether the package COMPILES,
	// which is the question the run is about to bet seventy minutes on.
	cmd := exec.Command("go", "build", "-overlay", overlayFile, "-o", os.DevNull, "./"+pkgDir)
	cmd.Dir = root
	out, berr := cmd.CombinedOutput()
	if berr == nil {
		return ""
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "" // failed with no diagnostic; the mechanism, not the code
	}
	// `go: ...` is the toolchain refusing to run — no module, no network for a
	// dependency, a bad flag. That says nothing about the generated package, and
	// reporting it as "this does not compile yet" would blame the artifact for
	// the tool. Compiler diagnostics name a file and a position; these do not.
	//
	// The build at the end is the authority and will report the same condition
	// with the whole component in view, so staying quiet here loses nothing.
	if strings.HasPrefix(text, "go: ") {
		return ""
	}
	// An overlay path in a diagnostic names a temp file the model has never
	// heard of. Map it back to the path it was generated as.
	for real, tmp := range replace {
		rel, rerr := filepath.Rel(root, real)
		if rerr != nil {
			continue
		}
		text = strings.ReplaceAll(text, tmp, filepath.ToSlash(rel))
	}
	return lastLines(text, 20)
}
