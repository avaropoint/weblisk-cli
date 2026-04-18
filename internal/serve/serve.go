package serve

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

// Serve starts a local dev server from the project root.
// All files (HTML, CSS, JS, lib/) are served directly from root.
func Serve(root string, port int) error {
	if _, err := os.Stat(filepath.Join(root, "index.html")); err != nil {
		return fmt.Errorf("index.html not found in %s — run 'weblisk new' first", root)
	}

	mux := http.NewServeMux()

	// Serve everything from the project root.
	mux.Handle("/", http.FileServer(http.Dir(root)))

	addr := fmt.Sprintf(":%d", port)
	fmt.Println()
	fmt.Println("  Weblisk Dev Server")
	fmt.Println()
	fmt.Printf("  Local: http://localhost:%d\n", port)
	fmt.Printf("  Root:  %s\n", root)
	fmt.Println()
	fmt.Printf("  Serving %s\n", root)
	fmt.Println("  Press Ctrl+C to stop.")
	fmt.Println()

	return http.ListenAndServe(addr, mux)
}
