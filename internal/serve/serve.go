package serve

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

// Serve starts a local dev server that overlays app/ and lib/ into a single
// virtual root, so HTML, /css/…, /js/… resolve from app/ while
// /lib/weblisk/… resolves from lib/. Works on all platforms (no symlinks).
func Serve(root string, port int) error {
	appDir := filepath.Join(root, "app")
	libDir := filepath.Join(root, "lib")

	if _, err := os.Stat(appDir); err != nil {
		return fmt.Errorf("app/ directory not found in %s", root)
	}

	mux := http.NewServeMux()

	// /lib/ serves from <root>/lib/
	mux.Handle("/lib/", http.StripPrefix("/lib/", http.FileServer(http.Dir(libDir))))

	// Everything else serves from <root>/app/
	mux.Handle("/", http.FileServer(http.Dir(appDir)))

	addr := fmt.Sprintf(":%d", port)
	fmt.Println()
	fmt.Println("  Weblisk Dev Server")
	fmt.Println()
	fmt.Printf("  Local: http://localhost:%d\n", port)
	fmt.Printf("  Root:  %s\n", root)
	fmt.Println()
	fmt.Println("  Serving app/ + lib/ on a single origin.")
	fmt.Println("  Press Ctrl+C to stop.")
	fmt.Println()

	return http.ListenAndServe(addr, mux)
}
