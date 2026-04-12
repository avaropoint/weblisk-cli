package pro

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/avaropoint/weblisk-cli/internal/config"
)

const frameworkCDNBase = "https://cdn.weblisk.dev/"

// FrameworkFiles is the canonical list of free framework modules to update.
var FrameworkFiles = []string{
	"weblisk.js",
	"weblisk.d.ts",
	"core/signal.js", "core/signal.d.ts",
	"core/island.js", "core/island.d.ts",
	"core/hydrate.js", "core/hydrate.d.ts",
	"core/lazy-island.js", "core/lazy-island.d.ts",
	"core/error.js", "core/error.d.ts",
	"core/worker.js", "core/worker.d.ts",
	"core/scheduler.js", "core/scheduler.d.ts",
	"core/devtools.js", "core/devtools.d.ts",
	"nav/router.js", "nav/router.d.ts",
	"nav/guard.js", "nav/guard.d.ts",
	"nav/scroll.js", "nav/scroll.d.ts",
	"nav/transition.js", "nav/transition.d.ts",
	"net/fetch.js", "net/fetch.d.ts",
	"net/ws.js", "net/ws.d.ts",
	"net/sse.js", "net/sse.d.ts",
	"net/transport.js", "net/transport.d.ts",
	"perf/marks.js", "perf/marks.d.ts",
	"perf/reporter.js", "perf/reporter.d.ts",
	"perf/vitals.js", "perf/vitals.d.ts",
	"perf/prefetch.js", "perf/prefetch.d.ts",
	"state/idb.js", "state/idb.d.ts",
	"state/sync.js", "state/sync.d.ts",
	"state/history.js", "state/history.d.ts",
	"state/form.js", "state/form.d.ts",
	"data/store.js", "data/store.d.ts",
	"security/csp.js", "security/csp.d.ts",
	"security/trusted.js", "security/trusted.d.ts",
	"security/sanitize.js", "security/sanitize.d.ts",
	"security/permissions.js", "security/permissions.d.ts",
	"security/csrf.js", "security/csrf.d.ts",
	"pwa/manifest.js", "pwa/manifest.d.ts",
	"pwa/push.js", "pwa/push.d.ts",
	"pwa/offline.js", "pwa/offline.d.ts",
	"a11y/aria.js", "a11y/aria.d.ts",
	"a11y/focus.js", "a11y/focus.d.ts",
	"a11y/motion.js", "a11y/motion.d.ts",
	"a11y/locale.js", "a11y/locale.d.ts",
	"ui/virtual-list.js", "ui/virtual-list.d.ts",
	"ui/img.js", "ui/img.d.ts",
	"ui/component.js", "ui/component.d.ts",
	"ui/clipboard.js", "ui/clipboard.d.ts",
	"ui/resize.js", "ui/resize.d.ts",
	"ui/animate.js", "ui/animate.d.ts",
	"ui/dialog.js", "ui/dialog.d.ts",
	"ui/fullscreen.js", "ui/fullscreen.d.ts",
	"ui/share.js", "ui/share.d.ts",
	"ui/drag.js", "ui/drag.d.ts",
	"ui/media-session.js", "ui/media-session.d.ts",
	"ui/wakelock.js", "ui/wakelock.d.ts",
	"ui/geo.js", "ui/geo.d.ts",
	"test/assert.js", "test/assert.d.ts",
}

// Update re-downloads framework modules from CDN.
// If a pro license is configured, also refreshes pro modules.
func Update(root, version string) error {
	fmt.Println()
	fmt.Println("  ⚡ Weblisk Update")
	fmt.Println()

	libDir := filepath.Join(root, "lib", "weblisk")
	if _, err := os.Stat(libDir); os.IsNotExist(err) {
		return fmt.Errorf("no lib/weblisk/ found — are you in a --local project?")
	}

	fmt.Println("  Downloading latest framework modules...")
	fmt.Println()

	client := &http.Client{Timeout: 15 * time.Second}
	updated := 0
	failed := 0

	for _, file := range FrameworkFiles {
		dest := filepath.Join(libDir, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s — %v\n", file, err)
			failed++
			continue
		}

		if err := downloadFile(client, frameworkCDNBase+file, dest); err != nil {
			if strings.HasSuffix(file, ".js") {
				fmt.Fprintf(os.Stderr, "  ✗ %s — %v\n", file, err)
				failed++
			}
			continue
		}
		fmt.Printf("  ✓ %s\n", file)
		updated++
	}

	cfg := config.Resolve()
	if cfg.License != "" {
		fmt.Println()
		fmt.Println("  Updating pro modules...")
		fmt.Println()
		proDir := filepath.Join(libDir, "pro")
		if err := os.MkdirAll(proDir, 0755); err == nil {
			for _, mod := range Modules {
				if err := DownloadModule(cfg.License, mod, proDir, version); err != nil {
					fmt.Fprintf(os.Stderr, "  ✗ pro/%s — %v\n", mod, err)
					failed++
					continue
				}
				fmt.Printf("  ✓ pro/%s\n", mod)
				updated++

				dts := strings.TrimSuffix(mod, ".js") + ".d.ts"
				if err := DownloadModule(cfg.License, dts, proDir, version); err == nil {
					fmt.Printf("  ✓ pro/%s\n", dts)
					updated++
				}
			}
		}
	}

	fmt.Println()
	if failed > 0 {
		fmt.Printf("  Updated %d files (%d failed).\n\n", updated, failed)
	} else {
		fmt.Printf("  Updated %d files.\n\n", updated)
	}

	return nil
}

func downloadFile(client *http.Client, url, dest string) error {
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return fmt.Errorf("not found")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	return os.WriteFile(dest, data, 0644)
}
