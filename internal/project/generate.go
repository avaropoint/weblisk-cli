package project

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// RePageName is the regex used to sanitize page names.
var RePageName = regexp.MustCompile(`[^a-z0-9/-]`)

var reIslandName = regexp.MustCompile(`[^a-z0-9-]`)

// AddPage generates a new standalone HTML page.
func AddPage(name, root string) error {
	safeName := sanitizeName(name)
	if safeName == "" {
		return fmt.Errorf("invalid page name")
	}

	title := toTitle(filepath.Base(safeName))
	filePath := filepath.Join(root, safeName+".html")

	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return err
	}

	content, err := ResolveFile(root, "pages/blank.html")
	if err != nil {
		return err
	}

	// Replace the default placeholder title/name with the actual page name.
	content = strings.ReplaceAll(content, "Page Title", title)
	content = strings.ReplaceAll(content, "Page description.", title+" page.")
	content = strings.ReplaceAll(content, "This is the page content.", "This is the "+strings.ToLower(title)+" page.")

	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return err
	}

	fmt.Printf("  [ok] %s.html\n", safeName)
	fmt.Printf("\n  Page ready: /%s.html\n\n", safeName)
	return nil
}

// AddIsland generates a new island script file.
func AddIsland(name, root string) error {
	safeName := sanitizeIslandName(name)
	if safeName == "" {
		return fmt.Errorf("invalid island name")
	}

	filePath := filepath.Join(root, "js", "islands", safeName+".js")
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return err
	}

	content, err := ResolveFile(root, "islands/starter.js")
	if err != nil {
		return err
	}

	// Replace the default placeholder island name.
	content = strings.ReplaceAll(content, "my-island", safeName)

	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return err
	}

	fmt.Printf("  [ok] js/islands/%s.js\n", safeName)
	fmt.Println()
	fmt.Println("  Island ready. Add to your HTML:")
	fmt.Printf("    <div data-island=\"%s\">\n", safeName)
	fmt.Println("      <!-- your content -->")
	fmt.Println("    </div>")
	fmt.Printf("    <script type=\"module\" src=\"/js/islands/%s.js\"></script>\n\n", safeName)
	return nil
}

// Helpers

func sanitizeName(name string) string {
	return RePageName.ReplaceAllString(strings.ToLower(name), "")
}

func sanitizeIslandName(name string) string {
	return reIslandName.ReplaceAllString(strings.ToLower(name), "")
}

func toTitle(s string) string {
	s = strings.ReplaceAll(s, "-", " ")
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
