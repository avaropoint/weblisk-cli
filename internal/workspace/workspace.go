package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Workspace is a sandboxed view of a project directory.
// Agents propose changes through the workspace rather than
// writing directly to disk.
type Workspace struct {
	Root    string
	changes []FileChange
}

// FileChange describes a proposed file modification.
type FileChange struct {
	Path    string       // relative to workspace root
	Action  string       // "create", "modify", "delete"
	Content string       // new content (create/modify)
	Mods    []Modification // line-level edits (modify only)
}

// Modification is a single edit within a file.
type Modification struct {
	Line    int    // 1-based line number
	Old     string // expected existing content
	New     string // replacement content
}

// NewWorkspace creates a workspace rooted at the given directory.
func NewWorkspace(root string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("workspace root not found: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory: %s", abs)
	}
	return &Workspace{Root: abs}, nil
}

// ReadFile reads a file relative to the workspace root.
func (w *Workspace) ReadFile(rel string) (string, error) {
	path := filepath.Join(w.Root, filepath.FromSlash(rel))
	if !strings.HasPrefix(path, w.Root) {
		return "", fmt.Errorf("path escapes workspace: %s", rel)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ScanFiles returns relative paths of all files matching the glob pattern.
func (w *Workspace) ScanFiles(pattern string) ([]string, error) {
	var matches []string
	err := filepath.WalkDir(w.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == ".weblisk" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(w.Root, path)
		rel = filepath.ToSlash(rel)
		if pattern == "" {
			matches = append(matches, rel)
			return nil
		}
		ok, _ := filepath.Match(pattern, filepath.Base(path))
		if ok {
			matches = append(matches, rel)
		}
		return nil
	})
	return matches, err
}

// ProposeChange adds a file change to the pending set.
func (w *Workspace) ProposeChange(change FileChange) {
	w.changes = append(w.changes, change)
}

// Changes returns all pending changes.
func (w *Workspace) Changes() []FileChange {
	return w.changes
}

// HasChanges returns true if there are pending changes.
func (w *Workspace) HasChanges() bool {
	return len(w.changes) > 0
}

// ApplyChanges writes all pending changes to disk.
func (w *Workspace) ApplyChanges() error {
	for _, c := range w.changes {
		path := filepath.Join(w.Root, filepath.FromSlash(c.Path))
		if !strings.HasPrefix(path, w.Root) {
			return fmt.Errorf("path escapes workspace: %s", c.Path)
		}

		switch c.Action {
		case "create", "modify":
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(c.Content), 0644); err != nil {
				return err
			}
		case "delete":
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		default:
			return fmt.Errorf("unknown action: %s", c.Action)
		}
	}
	w.changes = nil
	return nil
}

// Reset clears all pending changes without applying them.
func (w *Workspace) Reset() {
	w.changes = nil
}
