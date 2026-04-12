/* Starter styles — replace with your own */

/* MPA view transitions — smooth crossfade between pages */
@view-transition { navigation: auto; }
::view-transition-old(root) { animation: 200ms ease both wl-fade-out; }
::view-transition-new(root) { animation: 200ms ease both wl-fade-in; }
@keyframes wl-fade-out { to { opacity: 0; } }
@keyframes wl-fade-in { from { opacity: 0; } }

*, *::before, *::after { box-sizing: border-box; margin: 0; }
body { font-family: system-ui, -apple-system, sans-serif; line-height: 1.6; color: #333; }
a { color: #3B4F7C; text-decoration: none; }
a:hover { text-decoration: underline; }
.page { max-width: 48rem; margin: 2rem auto; padding: 0 1rem; }
h1 { font-size: 2rem; margin-bottom: 1rem; }
code { background: #f1f5f9; padding: 0.2em 0.4em; border-radius: 4px; font-size: 0.9em; }
.wl-skip-link { position: absolute; top: -100%; left: 0; background: #fff; color: #333; padding: 0.5rem 1rem; z-index: 10000; font-size: 0.875rem; }
.wl-skip-link:focus { top: 0; }
