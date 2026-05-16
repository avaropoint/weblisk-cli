package serve

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Serve starts the local dev server.
// It mirrors the production gateway's routing:
//   - /api/blueprint/<path> → reads from blueprints/ directory
//   - /api/health → returns dev server health
//   - /* → serves from public/ (or root if no public/ dir)
//
// It also provides live-reload via SSE at /__livereload.
func Serve(root string, port int) error {
	publicDir := filepath.Join(root, "public")
	if info, err := os.Stat(publicDir); err != nil || !info.IsDir() {
		publicDir = root
	}

	blueprintDir := filepath.Join(root, "blueprints")

	reload := newReloader(root)
	go reload.watch()

	mux := http.NewServeMux()

	// Live-reload SSE endpoint
	mux.HandleFunc("/__livereload", reload.handler)

	// Blueprint API — mirrors production /api/blueprint/:path
	mux.HandleFunc("/api/blueprint/", func(w http.ResponseWriter, r *http.Request) {
		handleBlueprintAPI(w, r, blueprintDir)
	})

	// Health endpoint
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"name":"weblisk-dev","status":"healthy","mode":"development"}`))
	})

	// Static files with security headers and live-reload injection
	fs := http.FileServer(http.Dir(publicDir))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleStatic(w, r, fs, publicDir)
	})

	addr := fmt.Sprintf(":%d", port)
	fmt.Println()
	fmt.Println("  \033[1mWeblisk Dev Server\033[0m")
	fmt.Println()
	fmt.Printf("  Local:      http://localhost:%d\n", port)
	fmt.Printf("  Public:     %s\n", publicDir)
	fmt.Printf("  Blueprints: %s\n", blueprintDir)
	fmt.Println()
	fmt.Println("  Live-reload enabled — watching for changes")
	fmt.Println("  Press Ctrl+C to stop.")
	fmt.Println()

	return http.ListenAndServe(addr, mux)
}

// ── Blueprint API ────────────────────────────────────────────────────────────

var blueprintPrefixes = []string{"pages/", "components/"}

func isValidBlueprintPath(path string) bool {
	valid := false
	for _, p := range blueprintPrefixes {
		if strings.HasPrefix(path, p) {
			valid = true
			break
		}
	}
	if !valid {
		return false
	}
	if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
		return false
	}
	if strings.Contains(path, "..") || strings.Contains(path, "//") {
		return false
	}
	return true
}

func handleBlueprintAPI(w http.ResponseWriter, r *http.Request, blueprintDir string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("allow", "GET, HEAD")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/blueprint/")

	if !isValidBlueprintPath(path) {
		setSecurityHeaders(w, false)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Invalid blueprint path"}`))
		return
	}

	filePath := filepath.Join(blueprintDir, filepath.FromSlash(path))
	data, err := os.ReadFile(filePath)
	if err != nil {
		setSecurityHeaders(w, false)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"Blueprint not found"}`))
		return
	}

	setSecurityHeaders(w, false)
	w.Header().Set("content-type", "text/yaml; charset=utf-8")
	w.Header().Set("cache-control", "no-cache")
	w.Write(data)
}

// ── Static file serving ──────────────────────────────────────────────────────

const liveReloadScript = `<script>(function(){var es=new EventSource("/__livereload");es.onmessage=function(){location.reload()};es.onerror=function(){es.close();setTimeout(function(){location.reload()},1000)}})()</script>`

func handleStatic(w http.ResponseWriter, r *http.Request, fs http.Handler, publicDir string) {
	// Block dotfiles (match production gateway)
	if strings.Contains(r.URL.Path, "/.") {
		http.NotFound(w, r)
		return
	}
	// Block path traversal
	if strings.Contains(r.URL.Path, "..") {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	reqPath := r.URL.Path
	if reqPath == "/" || strings.HasSuffix(reqPath, "/") {
		reqPath += "index.html"
	}
	isHTML := strings.HasSuffix(reqPath, ".html")

	if isHTML {
		filePath := filepath.Join(publicDir, filepath.FromSlash(reqPath))
		data, err := os.ReadFile(filePath)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		content := string(data)
		injected := strings.Replace(content, "</body>", liveReloadScript+"\n</body>", 1)

		setSecurityHeaders(w, true)
		w.Header().Set("content-type", "text/html; charset=utf-8")
		w.Header().Set("cache-control", "no-cache")
		w.Write([]byte(injected))
		return
	}

	setSecurityHeaders(w, false)
	w.Header().Set("cache-control", "no-cache")
	fs.ServeHTTP(w, r)
}

// ── Security headers (mirrors production gateway) ────────────────────────────

func setSecurityHeaders(w http.ResponseWriter, isHTML bool) {
	w.Header().Set("x-content-type-options", "nosniff")
	w.Header().Set("x-frame-options", "DENY")
	w.Header().Set("referrer-policy", "strict-origin-when-cross-origin")
	w.Header().Set("permissions-policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), interest-cohort=()")
	w.Header().Set("x-dns-prefetch-control", "off")
	w.Header().Set("cross-origin-opener-policy", "same-origin")
	w.Header().Set("cross-origin-resource-policy", "same-origin")
	if isHTML {
		csp := strings.Join([]string{
			"default-src 'self'",
			"script-src 'self' 'unsafe-inline' https:",
			"style-src 'self' 'unsafe-inline'",
			"img-src 'self' data: blob:",
			"connect-src 'self' https: ws: wss:",
			"worker-src 'self' blob:",
			"manifest-src 'self'",
			"frame-ancestors 'none'",
			"base-uri 'self'",
			"form-action 'self'",
		}, "; ")
		w.Header().Set("content-security-policy", csp)
	}
}

// ── Live-reload (SSE + polling file watcher) ─────────────────────────────────

type reloader struct {
	root    string
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
	modmap  map[string]time.Time
}

func newReloader(root string) *reloader {
	return &reloader{
		root:    root,
		clients: make(map[chan struct{}]struct{}),
		modmap:  make(map[string]time.Time),
	}
}

func (rl *reloader) handler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache")
	w.Header().Set("connection", "keep-alive")
	w.Header().Set("access-control-allow-origin", "*")
	flusher.Flush()

	ch := make(chan struct{}, 1)
	rl.mu.Lock()
	rl.clients[ch] = struct{}{}
	rl.mu.Unlock()

	defer func() {
		rl.mu.Lock()
		delete(rl.clients, ch)
		rl.mu.Unlock()
	}()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			fmt.Fprintf(w, "data: reload\n\n")
			flusher.Flush()
		}
	}
}

func (rl *reloader) notify() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for ch := range rl.clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (rl *reloader) watch() {
	watchDirs := []string{"public", "blueprints", ".weblisk"}
	extensions := map[string]bool{
		".html": true, ".css": true, ".js": true, ".mjs": true,
		".yaml": true, ".yml": true, ".json": true, ".svg": true,
	}

	for {
		time.Sleep(500 * time.Millisecond)
		changed := false

		for _, dir := range watchDirs {
			absDir := filepath.Join(rl.root, dir)
			filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil
				}
				ext := strings.ToLower(filepath.Ext(path))
				if !extensions[ext] {
					return nil
				}
				mod := info.ModTime()
				if prev, ok := rl.modmap[path]; ok {
					if mod.After(prev) {
						changed = true
						rl.modmap[path] = mod
					}
				} else {
					rl.modmap[path] = mod
				}
				return nil
			})
		}

		if changed {
			fmt.Printf("  \033[36m↻\033[0m File changed — reloading browsers\n")
			rl.notify()
		}
	}
}
