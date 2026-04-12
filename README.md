# Weblisk CLI

Zero-dependency static site builder + AI agent dispatcher.

A standalone Go binary. No runtime, no package manager, no dependencies.
The CLI is a blueprint carrier — it describes how agents and servers should
work, dispatches to the user's AI model to generate implementations, and
verifies they comply with the protocol specification.

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
weblisk new my-site --template blog

# Start dev server
cd my-site && weblisk dev

# Build for production
weblisk build --minify --fingerprint

# Code generation (requires AI provider)
export WL_AI_PROVIDER=ollama WL_AI_MODEL=llama3
weblisk server init           # Generate orchestrator
weblisk agent create seo      # Generate SEO agent

# Start the agent system
weblisk server start
weblisk agent start seo --orch http://localhost:9800
```

## Architecture

```
weblisk-cli/
├── main.go                    Entry point — command dispatch
├── internal/
│   ├── config/                .env loader + WL_* resolution
│   ├── build/                 Build pipeline (copy, minify, fingerprint, sitemap)
│   ├── serve/                 Local dev server (overlays app/ + lib/)
│   ├── project/               Scaffold new projects from templates
│   │   └── templates/         Embedded HTML/CSS/JS templates
│   ├── dispatch/              AI dispatch pipeline
│   │   ├── blueprints.go      Blueprint loader (git clone + cache)
│   │   ├── dispatch.go        Prompt construction + response parsing
│   │   └── provider.go        LLM provider abstraction
│   ├── server/                Server subcommands (init, start, verify)
│   │   └── agent/             Agent subcommands (create, start, verify, list)
│   ├── protocol/              Protocol types, crypto, verification
│   ├── workspace/             Sandboxed file operations
│   └── pro/                   License activation + module downloads
└── go.mod
```

## Blueprint System

Blueprints are loaded from the [weblisk-blueprints](https://github.com/avaropoint/weblisk-blueprints) repository.
On first use, the repo is cloned into `.weblisk/blueprints/` and cached locally.

The CLI reads blueprint markdown, constructs prompts, and sends them to the
configured AI model to generate server and agent implementations.

## Environment Variables

| Variable | Description | Default |
|---|---|---|
| `WL_ORIGIN` | Production origin URL | `http://localhost:3000` |
| `WL_PORT` | Dev server port | `3000` |
| `WL_DIST` | Output directory | `dist` |
| `WL_CDN` | CDN base URL | — |
| `WL_LICENSE` | Pro license key | — |
| `WL_AI_PROVIDER` | AI backend | `openai` |
| `WL_AI_MODEL` | Model name | provider default |
| `WL_AI_BASE_URL` | Endpoint override | — |
| `WL_AI_KEY` | API key | — |

## Releasing

To publish a new version:

```bash
git tag v1.1.0
git push origin v1.1.0
```

The [release workflow](.github/workflows/release.yml) will cross-compile for all platforms and create a GitHub Release automatically.

## License

MIT
