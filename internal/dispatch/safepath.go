package dispatch

// Containment for model-supplied file paths.
//
// # The vulnerability this closes
//
// Generation asks a model for code and the CLI writes what comes back. The paths
// in that response are MODEL OUTPUT, and they were joined to the target
// directory and written without any containment check:
//
//	fullPath := filepath.Join(targetDir, f.Path)   // Join CLEANS the path
//	os.WriteFile(fullPath, ...)                    // so "../.." escapes
//
// Demonstrated writing to <root>/.weblisk/secrets/master.key and to a path
// entirely outside the project.
//
// # Why this is reachable, not theoretical
//
// Blueprints are the generator's instructions, and a hub adopts blueprints from
// a MARKETPLACE. An untrusted blueprint is untrusted input to a code generator —
// prompt injection there is a file-write primitive, and a file write to
// .git/hooks/ or a shell profile is code execution on the operator's machine.
//
// The model does not need to be malicious for this to matter either: a wrong
// path in a hallucinated response destroys a key store just as thoroughly.
//
// # The rule
//
// A generated file MUST land inside the target directory, and MUST NOT land in
// a location that carries authority — a secrets store, a key directory, a VCS
// hook, or an environment file — even when that location is nominally inside.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// protectedNames are path segments a generated file may never be written into
// or over, regardless of containment. Defence in depth: containment already
// keeps generation inside its target, and this keeps a mistake in the target's
// own configuration from becoming a key disclosure.
var protectedNames = map[string]bool{
	".git":     true, // hooks are executed — a write here is code execution
	"secrets":  true,
	"keys":     true,
	".ssh":     true,
	".env":     true,
	".weblisk": true, // the instance's own state, including secrets/ and keys/
}

// safeGeneratedPath resolves a model-supplied relative path inside targetDir.
//
// Returns the absolute path to write, or an error naming what was refused. The
// caller MUST treat an error as fatal for that file: a generation that tried to
// escape is not a generation to partially apply.
func safeGeneratedPath(targetDir, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("generated file has no path")
	}
	// An absolute path is never a file "within" the target, whatever it cleans to.
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", fmt.Errorf("refusing absolute path %q from model output", rel)
	}
	// Reject the traversal before cleaning, so the refusal names what was sent
	// rather than what it resolved to.
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == ".." {
			return "", fmt.Errorf("refusing path %q from model output: it traverses outside the target directory", rel)
		}
		if protectedNames[strings.ToLower(seg)] {
			return "", fmt.Errorf("refusing path %q from model output: %q is a protected location", rel, seg)
		}
	}

	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return "", err
	}
	full := filepath.Clean(filepath.Join(absTarget, rel))

	// Belt and braces: even with traversal rejected above, confirm containment
	// on the resolved path. Compared with a separator appended so a sibling
	// directory sharing a name prefix — "server-old" against "server" — is not
	// treated as inside.
	if full != absTarget && !strings.HasPrefix(full, absTarget+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing path %q from model output: it resolves outside %s", rel, absTarget)
	}
	return full, nil
}

// withinAfterSymlinks re-checks containment once the parent exists, because a
// symlinked directory inside the target can point anywhere. Checked at write
// time rather than at resolve time: the directory may not exist until then.
func withinAfterSymlinks(absTarget, full string) error {
	parent := filepath.Dir(full)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		// Not yet created is fine — nothing has been followed.
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	resolvedTarget, err := filepath.EvalSymlinks(absTarget)
	if err != nil {
		resolvedTarget = absTarget
	}
	if resolvedParent != resolvedTarget &&
		!strings.HasPrefix(resolvedParent, resolvedTarget+string(filepath.Separator)) {
		return fmt.Errorf("refusing %q: a symlink in the path leads outside %s", full, resolvedTarget)
	}
	return nil
}
