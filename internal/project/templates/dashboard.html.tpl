<!DOCTYPE html>
<html lang="en" dir="ltr">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Dashboard — {{.Name}}</title>
  <meta name="description" content="Application dashboard.">
  <meta name="robots" content="noindex">
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
    <a href="/dashboard.html">Dashboard</a>
    <a href="/settings.html">Settings</a>
  </nav>

  <main id="main-content">
    <section class="page">
      <h1>Dashboard</h1>
      <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:1rem;padding:1rem 0">
        <div style="padding:1.5rem;border:1px solid #ddd;border-radius:8px">
          <h2>Users</h2>
          <p style="font-size:2rem;font-weight:700">1,234</p>
        </div>
        <div style="padding:1.5rem;border:1px solid #ddd;border-radius:8px">
          <h2>Revenue</h2>
          <p style="font-size:2rem;font-weight:700">$45,678</p>
        </div>
        <div style="padding:1.5rem;border:1px solid #ddd;border-radius:8px">
          <h2>Active Sessions</h2>
          <p style="font-size:2rem;font-weight:700">89</p>
        </div>
      </div>
    </section>
  </main>

  <footer style="padding:1rem;text-align:center;color:#666;font-size:0.85rem">
    Built with Weblisk &middot; &copy; {{.Year}}
  </footer>

  <script type="module" src="/js/islands/shell.js"></script>
</body>
</html>
