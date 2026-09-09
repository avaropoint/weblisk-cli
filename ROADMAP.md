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

### 1. Operator invite

Narrower than it looked. `TENANT_LIFECYCLE.md` claimed invite, list and revoke
were all missing and that a second operator had no way in. Two of the three are
implemented, and the way in exists:

| Step | Command | State |
|------|---------|-------|
| newcomer makes an identity | `weblisk operator init` | implemented |
| newcomer asks to join | `weblisk operator register --orch <url> [--role]` | implemented — posts name + ML-DSA-65 public key |
| admin sees the request | `weblisk operators list` / `describe` | implemented |
| admin admits them | `weblisk operators approve <name>` | implemented |
| newcomer gets a token | `weblisk operator token` | implemented |
| admin changes their role | `weblisk operators role <name> <role>` | implemented |
| admin removes them | `weblisk operators revoke <name> --confirm` | implemented |
| **admin initiates** | **`weblisk operator invite`** | **absent** |

So the only missing verb is the one that lets an admin start the exchange: a
pre-authorised invite token or URL, which `patterns/principal-identity`
specifies. Without it, admission is newcomer-initiated and the admin has to
notice an unsolicited pending registration and approve it out of band — which
is workable for two operators who are talking to each other and not much
beyond that.

`grep -rn "invite" --include=*.go` finds nothing outside unrelated comments.

### 2. Federation setup verbs

`federation` is implemented for everything that operates on an existing
federation, and missing the two that create one:

| Command | Status |
|---------|--------|
| `weblisk federation init` | absent |
| `weblisk federation peer add <url>` | absent |

### 3. One generation pipeline, not two

`server init` and `tenant create` generate through plan → per-file → repair,
with a per-file cache so a run that dies at file 32 has banked 31.
`AgentCreate`, `DomainCreate` and `GatewayCreate` each make exactly ONE
`provider.Chat` call and split the reply on `// filename:` markers
(`internal/dispatch/dispatch.go:429`, `:487`, `:546`). That is the shape that
used to time out with nothing to show: a failed `agent create` banks nothing.

**Smaller than it looks.** The good path is already target-generic —
`ServerInit` is a one-line call to `SupervisedComponentInit(root,
"orchestrator", platform)`, and `ComponentInit(root, target, platform)` takes
the target as a parameter throughout: `ResolveGraph(root, target, platform)`,
`GatherRequirements(graph, target)`, `ReadTenantState(root, target)`. And
`GenerationRoots` (`internal/dispatch/requires.go:208`) already has arms for
`agent`, `domain`, `gateway` and `content`, not just `orchestrator`. The plan
even carries its own output directory in `plan.Root`, and `AcquireTargetLock`
is already threaded with it.

So this is not "port three commands onto a new pipeline". What is actually
missing is a **name** dimension:

- `agent` and `domain` are named instances — there can be many. `orchestrator`
  and `gateway` are singletons. `ComponentInit` keys everything off `target`
  alone, so two agents would share a plan cache key, a tenant-state record and
  a set of prior records.
- `plan.Root` must resolve to `agents/<name>/` rather than the target's default.
- `AgentCreate` additionally loads a per-domain blueprint (`DomainBlueprint(name)`)
  that `ComponentInit` does not.

Add the name to `ComponentInit`'s signature (or an options struct), thread it
into `planKey`/`ReadTenantState`/`PriorRecords`, and the three commands become
calls to `SupervisedComponentInit`. An afternoon, not a rewrite.

### 4. `test conformance` passes tests it does not run

The sharpest thing found while checking this file, and it is worse than a
stale row: `weblisk test conformance` declares 24 test IDs and only four of
them (`L1-04`, `L1-05`, `L1-09`, `L1-12`) issue a request. Every other ID
reaches `internal/test/test.go`'s `default:` arm —

```go
default:
    // Tests that require complex setup — pass by default
    // until full test harness is implemented
    return true
```

— so 20 of 24 print a tick without contacting the hub. A conformance suite
that reports 24/24 against an orchestrator that answers nothing is not an
incomplete feature, it is a misleading one. It also runs unauthenticated:
`operator.LoadToken`'s result is discarded at `test.go:121`.

Either implement the tests or mark the unimplemented IDs as skipped, so the
output distinguishes "passed" from "not checked".

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
| `codex` | flags measured against codex-cli 0.153.4 and applied by the binary. A generation has NOT been observed end to end — the measuring account returns HTTP 401 |
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
