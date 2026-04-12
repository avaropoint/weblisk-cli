<!DOCTYPE html>
<html lang="en" dir="ltr">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Settings — {{.Name}}</title>
  <meta name="description" content="Application settings.">
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
      <h1>Settings</h1>
      <form style="max-width:500px">
        <label style="display:block;margin-bottom:1rem">
          <span style="display:block;margin-bottom:0.25rem;font-weight:600">Display Name</span>
          <input type="text" name="displayName" style="width:100%;padding:0.5rem;border:1px solid #ddd;border-radius:4px" />
        </label>
        <button type="submit" style="padding:0.5rem 1.5rem;background:#3B4F7C;color:#fff;border:none;border-radius:4px;cursor:pointer">Save</button>
      </form>
    </section>
  </main>

  <footer style="padding:1rem;text-align:center;color:#666;font-size:0.85rem">
    Built with Weblisk &middot; &copy; {{.Year}}
  </footer>

  <script type="module" src="/js/islands/shell.js"></script>
</body>
</html>
