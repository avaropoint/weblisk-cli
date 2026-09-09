# CLI Roadmap — Blueprint Specification Alignment

This document tracks the gap between the
[architecture/cli.md](https://github.com/avaropoint/weblisk-blueprints/blob/main/architecture/cli.md)
specification and the current weblisk-cli implementation.

The CLI spec is the authoritative source. Every command listed there MUST
be implemented here.

## Why this file was rewritten

It listed `domain`, `gateway`, `validate`, `status` and the `operator` verbs as
"Tier 1 — next release", and the whole marketplace as "Tier 4 — v2.0+". All of
them are in `main.go` and have been for some time. A roadmap that understates
what exists is worse than no roadmap: it sends somebody to build a second
`weblisk status`, and it hides the rows that are genuinely still open by
burying them among dozens that are not.

Every row below was checked against `main.go`'s command table and the package
that handles it, not against the last time this file was edited. The date of
that check is 2026-09-09.

## Implemented

Registered in `main.go` and wired to a package that does the work — an HTTP
call against the admin API, or a model call plus a file write. "Wired" is the
claim; it is not a claim that every path is complete, and `test conformance`
is deliberately NOT in this table for that reason (see Open item 4).

| Command | Handler |
|---------|---------|
| `weblisk new <name> [--template...] [--local] [--lib]` | `internal/project` — `--template` is repeatable, so multi-template merge is done |
| `weblisk dev [--port]` | `internal/serve` |
| `weblisk build [--minify] [--fingerprint]` | `internal/build` |
| `weblisk vendor [--dest]` | `internal/project` |
| `weblisk component` | `internal/server` |
| `weblisk version` | `main.go` |
| `weblisk server init \| start \| verify \| status` | `internal/server` |
| `weblisk agent create \| start \| verify \| list` | `internal/server/agent` |
| `weblisk domain create \| start` | `internal/domain` |
| `weblisk gateway create \| start` | `internal/gateway` |
| `weblisk blueprints update` | `dispatch.UpdateBlueprints` |
| `weblisk validate [file]` | `dispatch.Validate` |
| `weblisk status [--watch] [--json]` | `admin.Status` |
| `weblisk tenant create` | `internal/tenantcmd` → `pkg/tenant` |
| `weblisk providers [--json]` | `dispatch.PrintProviders` |
| `weblisk doctor` | `internal/doctor` |
| `weblisk secrets` | `internal/secrets` |
| `weblisk pattern apply` | `internal/dispatch` |
| `weblisk operator init \| register \| token \| rotate \| connect` | `internal/operator` |
| `weblisk agents list \| describe \| deregister` | `internal/admin` |
| `weblisk domains list \| describe` | `internal/admin` |
| `weblisk operators list \| describe \| approve \| revoke \| role` | `internal/admin` |
| `weblisk audit` | `internal/admin` |
| `weblisk observations list \| trends` | `internal/admin` |
| `weblisk workflows list \| describe` | `internal/admin` |
| `weblisk approvals list \| describe \| accept \| reject` | `internal/admin` |
| `weblisk strategies list \| describe \| create \| update \| delete` | `internal/admin` |
| `weblisk federation peers \| pending \| accept \| reject \| revoke \| describe \| contracts` | `internal/admin` |
| `weblisk marketplace search \| describe \| buy \| install \| publish \| update \| delist \| dashboard \| reviews \| review \| collaborations \| usage \| terminate \| activate \| remove` | `internal/marketplace` |

| `weblisk deploy \| deploy rollback` | `internal/deploy` |
| `weblisk deps \| deps audit` | `internal/deps` |
| `weblisk policy validate \| test` | `internal/policy` |

## Open — the actual list

### 1. Operator admission — complete, and this entry was wrong twice

Worth keeping the history, because the row was wrong in both directions
within a day.

`TENANT_LIFECYCLE.md` said invite, list and revoke were all missing and a
second operator had no way in. That was stale: register → pending → approve
works end to end. The correction then claimed the one remaining gap was
`weblisk operator invite`. Checked against the specs rather than inferred, that
is wrong too.

- `architecture/cli.md` — authoritative for this repo — names no invite
  command. Its operator verbs are init, register, connect, token, rotate, and
  operators list/describe/role/revoke. All implemented.
- `architecture/admin.md`'s endpoint table defines seven operator routes.
  `pkg/tenant/routes.go` declares seven. They correspond one to one.
- `architecture/admin.md` designs admission deliberately: a second operator
  registers, lands at `status: "pending"`, and an admin calls
  `POST /v1/admin/operators/:name/approve`. It also argues against redundant
  verbs — "There is no separate reject verb, because a rejected registration
  and a removed operator leave the deployment in the same state, and two routes
  to one state drift."

The invitation contract lives in `patterns/principal-identity`, which is a
different and larger thing: a three-layer model — identity, credential, grant —
for a subject working across SEVERAL hubs. It says so itself: "`architecture/
admin` specifies operator registration against a single orchestrator... Neither
answers what happens when one subject works across several hubs." It defines no
HTTP endpoints at all.

So an invite verb here would be a client for a server nobody has built, in a
model no hub implements. That is precisely the failure `pkg/tenant/routes.go`
was written to stop: three commands "written from memory of the specification
rather than from it", one of them posting to a route no tenant has ever served.

**Nothing to do in this repo.** The work, if it is wanted, starts upstream:
`patterns/principal-identity` needs an endpoint surface in
`architecture/admin.md` and commands in `architecture/cli.md` before a CLI can
implement anything.

### 2. Federation setup verbs

`federation` is implemented for everything that operates on an existing
federation, and missing the two that create one:

| Command | Status |
|---------|--------|
| `weblisk federation init` | absent |
| `weblisk federation peer add <url>` | absent |

### 3. One generation pipeline — done

`agent create`, `domain create` and `gateway create` each made ONE
`provider.Chat` call and split the reply on `// filename:` markers. That is the
path that timed out with nothing to show: one call either returns every file or
returns nothing, so a build that died at minute nine banked zero, while
`server init` had a per-file cache that let it resume from the file it reached.

They now call `SupervisedComponentInit`, the same entry point as `server init`.
The single-shot prompts and their three system prompts are deleted — 4,590
bytes of a path nothing takes.

It was smaller than it looked, because `ComponentInit` was already generic over
the target and `GenerationRoots` already had `agent`, `domain` and `gateway`
arms. Two things were genuinely missing.

**A name.** A tenant has one orchestrator and one gateway, and any number of
agents and domains. Everything — the plan cache, the tenant-state read, the
prior-records lookup, and the written manifest — was keyed by KIND. Two agents
therefore shared all four, and the manifest is what `DecideRebuild` reads to
decide which files the current plan no longer lists and may delete. So building
`agents/billing` after `agents/shipping` could delete shipping's files.
`Component{Kind, Name}` in `component.go` carries both, and the two are not
interchangeable: kind chooses the blueprint and the assertions, key identifies
this instance's state.

**A directory that is decided, not guessed.** `plan.Root` came from the model's
JSON, and the plan prompt tells it root is `"."`. It is now set from
`Component.Dir()` — `agents/billing`, `domains/x`, `gateway`, `.` — the same
paths the old commands wrote to, for the same reason `Module` and `Target`
already were: a fact two files must agree on should not be guessed twice.

`Plan.Owner` was split from `Plan.Target` in the process. They were briefly one
field read for opposite purposes — validation wants the kind, so two agents
both get `cmd/agent/main.go` rather than a path containing a colon; the
manifest wants the instance. Setting one, validating, then overwriting it
worked only until somebody reordered the two steps.

**Found by running it:** `weblisk agent create` died with
`planning: fork/exec .../claude: argument list too long`, before the model was
reached. Claude Code was passed its prompt on argv while grok had been given
`--prompt-file` for exactly this, measured, and claude has no such flag — it
reads stdin instead. A planning prompt carries the target's whole blueprint
corpus and a single argv entry is capped at 128 KiB whatever `ARG_MAX` says.
This was not agent-specific; it is the size of the prompt, so it was reachable
from `server init` too.

### 4. `test conformance` — honest now, still mostly unimplemented

Fixed 2026-09-09. It declared 24 assertions and issued a request for four; the
other 20 reached a `default: return true` marked "pass by default until full
test harness is implemented". So a run printed 24 ticks against a hub that had
answered one request, and called it conformant.

There are three outcomes now — passed, failed, **not checked** — and the
summary says outright that a run with unchecked assertions does not establish
conformance. Each unchecked line names what it would need.

Implemented for real, against the spec rather than against HTTP 200:

| ID | Check |
|----|-------|
| L1-02 | an unsigned manifest must be rejected (`spec.md` POST /v1/register) |
| L1-04 | POST /v1/health — it was doing a GET, and the spec defines both paths as different endpoints |
| L1-05 | GET /v1/services answers |
| L1-09 | GET /v1/admin/overview refuses an unauthenticated caller |
| L1-12 | the five HealthStatus fields `types.md` marks required, and the three legal states |

The mock orchestrator was made conformant at the same time — it accepted every
registration unsigned and answered health without `name`, `uptime` or
`timestamp`, so it could not have failed the assertions it is offered as a
target for.

**Still open:** 19 assertions have no check. L1-01/03 need a signed ML-DSA-65
manifest the harness cannot yet mint; L1-06/07/08 need WLT token minting
including expired and mis-signed ones; the L2 and L3 rows need a registered
agent, a live event exchange, a workflow, or a peer hub.

### 5. Structural checks that verify what they never read

`internal/dispatch/verify.go`'s "HTTP handlers do not panic" check returned
`OutcomeVerified` — the strongest positive verdict — when every Go file failed
to parse, or when the tenant contained no handler-shaped function at all. It
searched, found no panic, and reported that as proof. Fixed 2026-09-09 to
return `markInconclusive` in both cases, which the file already had a mechanism
for.

The class is worth a sweep rather than one fix: `d51d9ab` corrected the mirror
image of this ("I cannot read this" reported as "this is wrong") for route
resolution. Every other entry in `structuralChecks` should be read for the same
question — *can this return a verdict when it examined nothing?*

### 6. Marketplace: two rows are not what the spec asked for

Nearly every verb exists. Two do not match the spec:

| Spec | Reality |
|------|---------|
| `weblisk marketplace info <id>` | no `info` verb — `weblisk marketplace info X` errors. The capability exists as `describe` |
| `weblisk marketplace list` | reads the LOCAL activation store (`loadStore`, `marketplace.go:194`) and makes no HTTP request, while `main.go`'s help calls it "List active purchases and subscriptions" |

Beyond those two, what is unestablished is that each verb round-trips against a
real hub — which is conformance coverage, and blocked on item 4.

### 7. Fixed in passing, recorded so it is not re-broken

Two defects found while checking the items above, both fixed 2026-09-09, both
worth knowing about because the code looked correct:

- **`AcquireTargetLock` was not a lock.** It read the lock file, decided, then
  wrote it — two syscalls with a window between them. A test that races 24
  concurrent acquisitions let **11** of them through. It is now an atomic
  claim (write a complete record to a temp file, then `os.Link` it into place,
  which fails with `EEXIST` if the name is taken), and `release()` checks that
  the record still names this process before deleting it — otherwise a stalled
  run deletes the lock of whoever legitimately took over from it.
- **`--tools ""` is a no-op in grok**, which ignores the empty value and keeps
  all 27 built-in tools including `write` and `run_terminal_command`. The same
  flag IS honoured by Claude Code. The fix needed an allowlist AND a
  subtraction (`search_tool` and `use_tool` survive an allowlist alone). The
  same two-line defect was live in `modelgw_src/model_gateway.go.txt`, which
  ships into every generated hub — a fix applied only to the CLI would have
  left it running in every tenant already built.

## Provider backends

Not part of the CLI spec, but it is what decides whether any generating
command works at all, so it is tracked here.

| Backend | State |
|---------|-------|
| `claude-code` | verified end to end |
| `grok` | flags measured against grok 1.0.24, including the zero-tool set that a `--tools ""` no-op used to defeat. A generation has NOT been observed end to end — the measuring account returns HTTP 402 |
| `codex` | **exercised end to end** against codex-cli 0.153.4 with a mocked model endpoint (a custom `model_providers` entry pointing at a local Responses API). The account is not authenticated, so nothing was asked of OpenAI — but codex accepted every flag, read a 200 KB prompt from stdin, wrote `--output-last-message`, and weblisk returned the answer and recorded the model. The remaining unknown is the model's own output quality, not the integration |
| `ollama`, `lmstudio` | probed by asking for a model list; not exercised for generation |
| hosted APIs | keyed; `anthropic`, `xai`, `openai`, `gemini`, `groq`, `mistral`, `deepseek`, `openrouter`, `cloudflare` |

`weblisk providers` lists what this machine has. Which one a build actually
uses is settled by `dispatch.ResolveReady`, which asks each candidate to answer
once before accepting it — a CLI that is installed but not logged in runs
perfectly and generates nothing, so presence is not evidence.

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
