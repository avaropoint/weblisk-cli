# CLI Roadmap — Blueprint Specification Alignment

This document tracks the gap between the
[architecture/cli.md](https://github.com/avaropoint/weblisk-blueprints/blob/main/architecture/cli.md)
specification and the current weblisk-cli implementation.

The CLI spec is the authoritative source. Every command listed there MUST
be implemented here.

## Currently Implemented (v1.1.0)

| Command | Status |
|---------|--------|
| `weblisk new <name> [--template] [--local] [--lib]` | ✅ |
| `weblisk dev [--port]` | ✅ |
| `weblisk build [--minify] [--fingerprint]` | ✅ |
| `weblisk vendor [--dest]` | ✅ |
| `weblisk version` | ✅ |
| `weblisk server init [--platform]` | ✅ |
| `weblisk server start` | ✅ |
| `weblisk server verify [--url]` | ✅ |
| `weblisk server status` | ✅ |
| `weblisk agent create <name> [--platform]` | ✅ |
| `weblisk agent start <name> [--port] [--orch]` | ✅ |
| `weblisk agent verify` | ✅ |
| `weblisk agent list` | ✅ |

## Tier 1 — Core Workflow (next release)

These commands complete the critical developer path.

| Command | Spec Section | Notes |
|---------|-------------|-------|
| `weblisk new --template client/* --template server/*` | Project Commands | Multi-template merge |
| `weblisk domain create <name> [--platform]` | Server Commands | Generate domain controller from domain.yaml via LLM |
| `weblisk domain start <name>` | Server Commands | Build + run domain controller |
| `weblisk gateway create [--platform]` | Server Commands | Generate gateway from config.yaml via LLM |
| `weblisk gateway start` | Server Commands | Build + run gateway |
| `weblisk blueprints update` | Blueprint Commands | Re-fetch cached blueprint sources |
| `weblisk validate [file]` | Blueprint Commands | Blueprint compliance validation |
| `weblisk status [--watch] [--json]` | Status Commands | System overview via admin API |

## Tier 2 — Operations (v1.3)

Runtime management of a running hub.

| Command | Spec Section |
|---------|-------------|
| `weblisk operator init [--force] [--name]` | Identity Commands |
| `weblisk operator register --orch <url> [--role]` | Identity Commands |
| `weblisk operator token [--refresh]` | Identity Commands |
| `weblisk agents list [--type] [--status] [--json]` | Agent Commands |
| `weblisk agents describe <name> [--json] [--metrics-range]` | Agent Commands |
| `weblisk agents deregister <name> [--confirm]` | Agent Commands |
| `weblisk domains list` | Domain Commands |
| `weblisk domains describe <name>` | Domain Commands |
| `weblisk audit [--follow] [--actor] [--action] [--since] [--export]` | Audit Commands |
| `weblisk pattern apply <pattern> [--resource]` | Blueprint Commands |

## Tier 3 — Advanced Features (v1.4+)

| Command | Spec Section |
|---------|-------------|
| `weblisk workflows list [--domain] [--status]` | Workflow Commands |
| `weblisk workflows describe <id>` | Workflow Commands |
| `weblisk approvals list [--priority] [--agent]` | Approval Commands |
| `weblisk approvals show <id>` | Approval Commands |
| `weblisk approvals accept <id...> [--all] [--priority]` | Approval Commands |
| `weblisk approvals reject <id> --reason <reason>` | Approval Commands |
| `weblisk strategies list` | Strategy Commands |
| `weblisk strategies create [--json]` | Strategy Commands |
| `weblisk strategies describe <id>` | Strategy Commands |
| `weblisk federation init` | Federation Commands |
| `weblisk federation peer add <url>` | Federation Commands |
| `weblisk federation peers` | Federation Commands |
| `weblisk federation pending` | Federation Commands |
| `weblisk federation accept <id>` | Federation Commands |
| `weblisk federation reject <id>` | Federation Commands |
| `weblisk federation revoke <peer>` | Federation Commands |

## Tier 4 — Marketplace (v2.0+)

| Command | Spec Section |
|---------|-------------|
| `weblisk marketplace search "<query>"` | Hub Architecture |
| `weblisk marketplace info <id>` | Hub Architecture |
| `weblisk marketplace buy <id> --accept-contract --accept-pricing` | Hub Architecture |
| `weblisk marketplace install <id>` | Hub Architecture |
| `weblisk marketplace publish --type <t> --config <f>` | Hub Architecture |
| `weblisk marketplace update <id> --price <p>` | Hub Architecture |
| `weblisk marketplace delist <id> --reason <r>` | Hub Architecture |
| `weblisk marketplace dashboard` | Hub Architecture |
| `weblisk marketplace reviews <id>` | Hub Architecture |
| `weblisk marketplace review <id> --rating <n> --title <t>` | Hub Architecture |
| `weblisk marketplace list` | Hub Architecture |
| `weblisk marketplace collaborations` | Hub Architecture |
| `weblisk marketplace usage <id>` | Hub Architecture |
| `weblisk marketplace terminate <id>` | Hub Architecture |

## Testing

| Command | Spec Section |
|---------|-------------|
| `weblisk test conformance --orch <url>` | Testing Architecture |
| `weblisk test conformance --level <n>` | Testing Architecture |
| `weblisk test conformance --test <id>` | Testing Architecture |
| `weblisk test mock-orchestrator --port <n>` | Testing Architecture |

## Implementation Notes

The CLI is intentionally thin:

1. **Scaffolding** — Copy files from weblisk-templates, do string replacements
2. **Code generation** — Read YAML specs + platform blueprint, dispatch to LLM
3. **Dev server** — Serve files, watch for changes, restart on change
4. **Operations** — HTTP client to orchestrator admin API, format responses

The LLM does the heavy lifting for code generation. The CLI's job is to
orchestrate the inputs (specs, blueprints, platform bindings) and manage
the outputs (generated source files).

Blueprint resolution: local `./blueprints/` → `WL_BLUEPRINT_SOURCES` → core.
Template resolution: local `./templates/` → `WL_TEMPLATE_SOURCES` → core.
