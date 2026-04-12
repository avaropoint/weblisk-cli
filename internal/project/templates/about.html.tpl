<!DOCTYPE html>
<html lang="en" dir="ltr">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>About — {{.Name}}</title>
  <meta name="description" content="About this application.">
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
    <section class="page">
      <h1>About</h1>
      <p>This app was built with <strong>Weblisk</strong> — a zero-dependency, zero-build-step framework.</p>
    </section>
  </main>

  <footer style="padding:1rem;text-align:center;color:#666;font-size:0.85rem">
    Built with Weblisk &middot; &copy; {{.Year}}
  </footer>

  <script type="module" src="/js/islands/shell.js"></script>
</body>
</html>
