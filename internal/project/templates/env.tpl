# {{.Name}} — Weblisk environment config
# These values are used by the CLI during dev and build.

# Origin URL used for sitemap generation and canonical links.
WL_ORIGIN=http://localhost:{{.Port}}

# Dev server port (default: 3000).
WL_PORT={{.Port}}

# Output directory for `weblisk build` (default: dist).
WL_DIST=dist

# CDN base URL — rewrites importmaps on build.
# Leave commented to serve framework from /lib/weblisk/.
# WL_CDN=

# Pro license key — enables `weblisk license` to download pro modules.
# Get yours at https://weblisk.dev/pro
# WL_LICENSE=
