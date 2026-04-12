<!DOCTYPE html>
<html lang="en" dir="ltr">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>{{.Title}}</title>
  <meta name="description" content="{{.Title}} page.">
  <meta name="theme-color" content="#3B4F7C">
  <link rel="stylesheet" href="/css/styles.css">
  <script>document.documentElement.dataset.theme=localStorage.getItem('wl-theme')||'light'</script>
  <script type="importmap">
  { "imports": { "weblisk": "{{.CDNBase}}weblisk.js", "weblisk/": "{{.CDNBase}}" } }
  </script>
</head>
<body>

  <main id="main-content">
    <section class="page">
      <h1>{{.Title}}</h1>
      <p>This is the {{.TitleLower}} page.</p>
    </section>
  </main>

  <script type="module" src="/js/islands/shell.js"></script>
</body>
</html>
