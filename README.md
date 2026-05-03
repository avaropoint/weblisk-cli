# Weblisk CLI

Zero-dependency static site builder, AI code generator, and hub operations client.

A standalone Go binary. No runtime, no package manager, no dependencies.
The CLI scaffolds projects, serves them locally, builds for production,
dispatches to the user's AI model for code generation, and provides
operator-authenticated operations against a running orchestrator.

## Related Projects

| Repository | Description |
|---|---|
| [weblisk](https://github.com/avaropoint/weblisk) | Core framework — the client-side JS runtime |
| [weblisk-templates](https://github.com/avaropoint/weblisk-templates) | Project templates used by `weblisk new` |
| [weblisk-blueprints](https://github.com/avaropoint/weblisk-blueprints) | Agent, domain, gateway, and server blueprints |

## Install

**macOS / Linux** (recommended):

```bash
curl -fsSL https://cdn.weblisk.dev/install.sh | sh
```

**Windows** (PowerShell):

```powershell
irm https://cdn.weblisk.dev/install.ps1 | iex
```

**Go install**:

```bash
go install github.com/avaropoint/weblisk-cli@latest
```

**Download binary** — grab the latest release for your platform:

| Platform | Binary |
|---|---|
| macOS (Apple Silicon) | [weblisk-darwin-arm64](https://github.com/avaropoint/weblisk-cli/releases/latest/download/weblisk-darwin-arm64) |
| macOS (Intel) | [weblisk-darwin-amd64](https://github.com/avaropoint/weblisk-cli/releases/latest/download/weblisk-darwin-amd64) |
| Linux (x64) | [weblisk-linux-amd64](https://github.com/avaropoint/weblisk-cli/releases/latest/download/weblisk-linux-amd64) |
| Linux (ARM64) | [weblisk-linux-arm64](https://github.com/avaropoint/weblisk-cli/releases/latest/download/weblisk-linux-arm64) |
| Windows (x64) | [weblisk-windows-amd64.exe](https://github.com/avaropoint/weblisk-cli/releases/latest/download/weblisk-windows-amd64.exe) |

**Build from source**:

```bash
git clone https://github.com/avaropoint/weblisk-cli.git
cd weblisk-cli
make build
```

## Usage

```bash
# Create a new project
weblisk new my-site
weblisk new my-blog --template blog
weblisk new my-app --template dashboard --local

# Start dev server
cd my-site && weblisk dev

# Build for production
weblisk build --minify --fingerprint

# Add framework files to an existing project
weblisk vendor
weblisk vendor --dest js/vendor

# Code generation (requires AI provider)
export WL_AI_PROVIDER=ollama WL_AI_MODEL=llama3
weblisk server init             # Generate orchestrator
weblisk agent create seo        # Generate SEO agent
weblisk domain create billing   # Generate domain controller
weblisk gateway create          # Generate application gateway
weblisk pattern apply cqrs      # Apply cross-cutting pattern

# Start the system
weblisk server start
weblisk agent start seo --orch http://localhost:9800
weblisk domain start billing
weblisk gateway start

# Operator identity
weblisk operator init
weblisk operator register --orch http://localhost:9800

# Marketplace
weblisk marketplace activate --key WL-XXXX-XXXX-XXXX-XXXX
weblisk marketplace list

# Operations (requires operator registration)
weblisk status
weblisk agents
weblisk workflows
weblisk approvals
weblisk audit --since 1h
```

## Templates

Project templates are sourced from [weblisk-templates](https://github.com/avaropoint/weblisk-templates). The CLI resolves templates from multiple sources in priority order:

1. **Local** — `./templates/` in your project directory
2. **Custom** — repos listed in `WL_TEMPLATE_SOURCES`
3. **Core** — [weblisk-templates](https://github.com/avaropoint/weblisk-templates) (always present)

Add custom template sources via `.env`:

```bash
WL_TEMPLATE_SOURCES=https://github.com/your-org/your-templates.git
```

## Blueprints

Agent, domain, gateway, and server blueprints are sourced from [weblisk-blueprints](https://github.com/avaropoint/weblisk-blueprints). See that repository for the full specification and available blueprints.

The CLI resolves blueprints from multiple sources in priority order:

1. **Local** — `./blueprints/` in your project directory
2. **Custom** — repos listed in `WL_BLUEPRINT_SOURCES`
3. **Core** — [weblisk-blueprints](https://github.com/avaropoint/weblisk-blueprints) (always present)

Add custom blueprint sources via `.env`:

```bash
WL_BLUEPRINT_SOURCES=https://github.com/your-org/your-blueprints.git
```

## Environment Variables

| Variable | Description | Default |
|---|---|---|
| `WL_ORIGIN` | Production origin URL | `http://localhost:3000` |
| `WL_PORT` | Dev server port | `3000` |
| `WL_DIST` | Output directory | `dist` |
| `WL_CDN` | CDN base URL (rewrites importmaps on build) | — |
| `WL_LIB` | Local framework path | `lib/weblisk` |
| `WL_ORCH` | Orchestrator URL | `http://localhost:9800` |
| `WL_TEMPLATE_SOURCES` | Additional template repo URLs (comma-separated) | — |
| `WL_BLUEPRINT_SOURCES` | Additional blueprint repo URLs (comma-separated) | — |
| `WL_AI_PROVIDER` | AI backend: `openai`, `ollama`, `anthropic`, `cloudflare` | `openai` |
| `WL_AI_MODEL` | Model name | provider default |
| `WL_AI_BASE_URL` | Endpoint override | — |
| `WL_AI_KEY` | API key | — |

## Operator Identity

The `operator` command manages Ed25519 key-based identity for authenticating
with a running orchestrator's admin API:

```bash
weblisk operator init                    # Generate key pair (~/.weblisk/keys/)
weblisk operator register --orch <url>   # Register with orchestrator
weblisk operator token                   # Show current token
```

Once registered, all operations commands (`status`, `agents`, `workflows`, etc.)
authenticate automatically using the stored token.

## Marketplace

The marketplace replaces the old single-key licensing model. Each product has
its own license key, and multiple products can be activated simultaneously:

```bash
weblisk marketplace activate --key WL-XXXX-XXXX-XXXX-XXXX
weblisk marketplace list
weblisk marketplace remove <product>
weblisk marketplace update
```

Activated products are stored in `~/.weblisk/marketplace.json`. Modules are
downloaded from `cdn.weblisk.dev/marketplace/`.

## Releasing

```bash
git tag v1.2.0
git push origin v1.2.0
```

The [release workflow](.github/workflows/release.yml) cross-compiles for all platforms and creates a GitHub Release.

## License

MIT
