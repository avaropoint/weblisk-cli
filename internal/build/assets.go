package build

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// fingerprint content-hashes CSS and JS files in the dist directory,
// renames them to include the hash, and rewrites all HTML/JS references.
// Skips lib/ (framework files loaded via import map need stable names).
func fingerprint(distDir string) (int, error) {
	var assets []fileEntry
	err := filepath.Walk(distDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			rel, _ := filepath.Rel(distDir, path)
			if rel == "lib" || strings.HasPrefix(rel, "lib"+string(filepath.Separator)) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext == ".css" || ext == ".js" {
			rel, _ := filepath.Rel(distDir, path)
			assets = append(assets, fileEntry{abs: path, rel: rel})
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	rewrites := map[string]string{}
	for _, a := range assets {
		content, err := os.ReadFile(a.abs)
		if err != nil {
			continue
		}
		hash := fmt.Sprintf("%x", md5.Sum(content))[:8]
		ext := filepath.Ext(a.rel)
		base := strings.TrimSuffix(filepath.Base(a.rel), ext)
		newBase := fmt.Sprintf("%s.%s%s", base, hash, ext)
		newAbs := filepath.Join(filepath.Dir(a.abs), newBase)

		if err := os.Rename(a.abs, newAbs); err != nil {
			continue
		}
		rewrites[base+ext] = newBase
	}

	if len(rewrites) == 0 {
		return 0, nil
	}

	err = filepath.Walk(distDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		ext := filepath.Ext(path)
		if ext != ".html" && ext != ".js" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(content)
		changed := false
		for oldName, newName := range rewrites {
			if strings.Contains(text, oldName) {
				text = strings.ReplaceAll(text, oldName, newName)
				changed = true
			}
		}
		if changed {
			os.WriteFile(path, []byte(text), 0644)
		}
		return nil
	})

	return len(rewrites), err
}
