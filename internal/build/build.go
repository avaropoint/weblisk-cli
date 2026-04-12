package build

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/avaropoint/weblisk-cli/internal/config"
)

// Options controls the build pipeline.
type Options struct {
	Minify      bool
	Fingerprint bool
}

// Directories and files to skip when copying to dist.
var skipNames = map[string]bool{
	// Build output
	"dist": true, "node_modules": true, "bin": true,
	// Source directories
	"cli": true, "server": true, "tests": true, "workers": true,
	// Config & metadata
	".git": true, ".github": true, ".vscode": true, ".weblisk": true, ".wrangler": true,
	".env": true, "weblisk.json": true,
	"Makefile": true, "README.md": true, "LICENSE": true, ".gitignore": true,
	"install.sh": true, "install.ps1": true,
}

// Build copies the project to the dist directory, minifies framework
// modules, and optionally minifies user code and fingerprints assets.
func Build(root string, opts Options) error {
	cfg := config.Resolve()
	distDir := filepath.Join(root, cfg.Dist)

	fmt.Println()
	fmt.Println("  ⚡ Weblisk build")
	fmt.Println()
	fmt.Printf("  Origin: %s\n", cfg.Origin)
	fmt.Printf("  Output: %s\n", distDir)
	if cfg.CDN != "" {
		fmt.Printf("  CDN:    %s\n", cfg.CDN)
	}
	if opts.Minify {
		fmt.Println("  Minify: enabled")
	}
	if opts.Fingerprint {
		fmt.Println("  Fingerprint: enabled")
	}
	fmt.Println()

	// Clean and create dist
	os.RemoveAll(distDir)
	os.MkdirAll(distDir, 0755)

	// Copy assets
	if err := copyAssets(root, distDir, root); err != nil {
		return fmt.Errorf("copy assets: %w", err)
	}

	// Rewrite importmaps to CDN origin if configured
	if cfg.CDN != "" {
		count := rewriteImportMaps(distDir, cfg.CDN)
		if count > 0 {
			fmt.Printf("  ✓ %d pages rewritten → %s\n", count, cfg.CDN)
		}
	}

	// Always minify framework modules
	fwDir := filepath.Join(distDir, "lib", "weblisk")
	fwCount, err := minifyDir(fwDir)
	if err == nil && fwCount > 0 {
		fmt.Printf("  ✓ %d framework modules minified\n", fwCount)
	}

	// Opt-in: minify user pages and app code
	if opts.Minify {
		htmlCount := minifyHTMLFiles(distDir)
		if htmlCount > 0 {
			fmt.Printf("  ✓ %d pages minified\n", htmlCount)
		}
		appCount := minifyUserAssets(distDir)
		if appCount > 0 {
			fmt.Printf("  ✓ %d app assets minified\n", appCount)
		}
	}

	// Collect routes and print them
	routes := collectRoutes(distDir, "")

	// Opt-in: fingerprint assets
	if opts.Fingerprint {
		count, err := fingerprint(distDir)
		if err == nil && count > 0 {
			fmt.Printf("  ✓ %d assets fingerprinted\n", count)
		}
	}

	// Generate sitemap.xml
	sitemapRoutes := filterRoutes(routes, "404")
	writeSitemap(filepath.Join(distDir, "sitemap.xml"), cfg.Origin, sitemapRoutes)
	fmt.Println("  ✓ sitemap.xml")

	// Generate robots.txt
	writeRobots(filepath.Join(distDir, "robots.txt"), cfg.Origin)
	fmt.Println("  ✓ robots.txt")

	if cfg.CDN != "" {
		fmt.Printf("\n  Done. Deploy %s/lib/weblisk/ → CDN, everything else → site host.\n\n", cfg.Dist)
	} else {
		fmt.Printf("\n  Done. Deploy %s/ to any static host.\n\n", cfg.Dist)
	}
	return nil
}

// ─── Asset Copier ────────────────────────────────────────────

func copyAssets(src, dest, projectRoot string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()

		if skipNames[name] {
			continue
		}
		srcPath := filepath.Join(src, name)
		destPath := filepath.Join(dest, name)

		// Flatten app/ contents into dist root
		rel, _ := filepath.Rel(projectRoot, srcPath)
		if rel == "app" && entry.IsDir() {
			if err := copyAssets(srcPath, dest, projectRoot); err != nil {
				return err
			}
			continue
		}

		if entry.IsDir() {
			if err := os.MkdirAll(destPath, 0755); err != nil {
				return err
			}
			if err := copyAssets(srcPath, destPath, projectRoot); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, destPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// ─── CDN import-map rewriter ─────────────────────────────────

func rewriteImportMaps(dir, cdn string) int {
	count := 0
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".html" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		src := string(data)
		if !strings.Contains(src, `"importmap"`) {
			return nil
		}
		updated := strings.Replace(src, `"/lib/weblisk/weblisk.js"`, `"`+cdn+`/weblisk.js"`, -1)
		updated = strings.Replace(updated, `"/lib/weblisk/"`, `"`+cdn+`/"`, -1)
		if updated != src {
			os.WriteFile(path, []byte(updated), 0644)
			count++
		}
		return nil
	})
	return count
}

// ─── Minification helpers ────────────────────────────────────

type fileEntry struct {
	abs string
	rel string
}

func minifyDir(dir string) (int, error) {
	count := 0
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		ext := filepath.Ext(path)
		if ext != ".js" && ext != ".css" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var minified string
		if ext == ".css" {
			minified = MinifyCSS(string(data))
		} else {
			minified = MinifyJS(string(data))
		}
		os.WriteFile(path, []byte(minified), 0644)
		count++
		return nil
	})
	return count, err
}

// skipRouteDirs are directories that contain assets, not pages.
var skipRouteDirs = map[string]bool{
	"css": true, "js": true, "images": true, "lib": true,
}

func minifyHTMLFiles(dir string) int {
	count := 0
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if skipRouteDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".html" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		os.WriteFile(path, []byte(MinifyHTML(string(data))), 0644)
		count++
		return nil
	})
	return count
}

func minifyUserAssets(distDir string) int {
	count := 0
	entries, err := os.ReadDir(distDir)
	if err != nil {
		return 0
	}
	for _, entry := range entries {
		abs := filepath.Join(distDir, entry.Name())
		if entry.IsDir() {
			if entry.Name() == "lib" {
				continue
			}
			c, _ := minifyDir(abs)
			count += c
		} else {
			ext := filepath.Ext(entry.Name())
			if ext == ".css" || ext == ".js" {
				data, err := os.ReadFile(abs)
				if err != nil {
					continue
				}
				var minified string
				if ext == ".css" {
					minified = MinifyCSS(string(data))
				} else {
					minified = MinifyJS(string(data))
				}
				os.WriteFile(abs, []byte(minified), 0644)
				count++
			}
		}
	}
	return count
}

// ─── Route collection ────────────────────────────────────────

func collectRoutes(dir, prefix string) []string {
	var routes []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return routes
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if skipRouteDirs[entry.Name()] {
				continue
			}
			sub := collectRoutes(
				filepath.Join(dir, entry.Name()),
				prefix+"/"+entry.Name(),
			)
			routes = append(routes, sub...)
		} else if filepath.Ext(entry.Name()) == ".html" {
			route := prefix + "/" + entry.Name()
			fmt.Printf("  ✓ %s\n", route[1:])
			routes = append(routes, route)
		}
	}
	return routes
}

func filterRoutes(routes []string, exclude string) []string {
	var out []string
	for _, r := range routes {
		if !strings.Contains(r, exclude) {
			out = append(out, r)
		}
	}
	return out
}

// ─── Sitemap / Robots ────────────────────────────────────────

func writeSitemap(path, origin string, routes []string) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, r := range routes {
		loc := origin + r
		b.WriteString("  <url>\n")
		b.WriteString("    <loc>" + loc + "</loc>\n")
		b.WriteString("    <changefreq>weekly</changefreq>\n")
		b.WriteString("  </url>\n")
	}
	b.WriteString("</urlset>\n")
	os.WriteFile(path, []byte(b.String()), 0644)
}

func writeRobots(path, origin string) {
	content := fmt.Sprintf("User-agent: *\nAllow: /\nSitemap: %s/sitemap.xml\n", origin)
	os.WriteFile(path, []byte(content), 0644)
}
