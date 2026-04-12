<!DOCTYPE html>
<html lang="en" dir="ltr">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Documentation — {{.Name}}</title>
  <meta name="description" content="Getting started with {{.Name}}.">
  <meta name="theme-color" content="#3B4F7C">
  <link rel="stylesheet" href="/css/styles.css">
  <script>document.documentElement.dataset.theme=localStorage.getItem('wl-theme')||'light'</script>
  <script type="importmap">
  { "imports": { "weblisk": "{{.CDNBase}}weblisk.js", "weblisk/": "{{.CDNBase}}" } }
  </script>
</head>
<body>

  <a href="#main-content" class="wl-skip-link">Skip to main content</a>
  <nav style="padding:1rem;border-bottom:1px solid #ddd;display:flex;gap:1rem" aria-label="Main navigation">
    <a href="/index.html"><strong>{{.Name}}</strong></a>
    <a href="/about.html">About</a>
    <a href="/docs.html">Docs</a>
  </nav>

  <main id="main-content">
    <section class="page" style="display:grid;grid-template-columns:220px 1fr;gap:2rem">
      <aside style="border-right:1px solid #ddd;padding-right:1rem">
        <nav aria-label="Documentation">
          <h3 style="font-size:0.85rem;text-transform:uppercase;color:#666;margin-bottom:0.5rem">Getting Started</h3>
          <a href="/docs.html" style="display:block;padding:0.25rem 0">Introduction</a>
        </nav>
      </aside>
      <article>
        <h1>Introduction</h1>
        <p>Welcome to the <strong>{{.Name}}</strong> documentation.</p>
        <p>Use the sidebar to navigate between sections.</p>
      </article>
    </section>
  </main>

  <footer style="padding:1rem;text-align:center;color:#666;font-size:0.85rem">
    Built with Weblisk &middot; &copy; {{.Year}}
  </footer>

  <script type="module" src="/js/islands/shell.js"></script>
</body>
</html>
