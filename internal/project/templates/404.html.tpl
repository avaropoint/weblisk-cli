<!DOCTYPE html>
<html lang="en" dir="ltr">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Page Not Found — {{.Name}}</title>
  <meta name="description" content="The requested page could not be found.">
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
  </nav>

  <main id="main-content">
    <section class="page page-404">
      <div class="error-block">
        <h1>404</h1>
        <p>The page you're looking for doesn't exist.</p>
        <a href="/index.html" class="btn">Go Home &rarr;</a>
      </div>
    </section>
  </main>

  <footer style="padding:1rem;text-align:center;color:#666;font-size:0.85rem">
    Built with Weblisk &middot; &copy; {{.Year}}
  </footer>

  <script type="module" src="/js/islands/shell.js"></script>
</body>
</html>
