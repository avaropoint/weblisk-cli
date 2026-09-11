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

### 2. Federation: the CLI is complete; the gap is one step up

**This entry was wrong.** It listed `weblisk federation init` and
`weblisk federation peer add <url>` as absent verbs. They are absent, and
nothing asked for them: `architecture/cli.md`'s Federation Commands section
names seven — `peers`, `pending`, `accept`, `reject`, `revoke`, `describe`,
`contracts` — and this CLI implements all seven. Naming two verbs the
specification does not contain and then recording them as missing is the
tooling writing the specification, which is the fault this repo has now caught
three times (see item 1, corrected twice).

**The real gap is in the specification, not the implementation.**
`protocol/federation.md` describes peering as a mutual exchange whose step 1 is
*"Admin initiates peering with B's federation_url"*, followed by
`POST /v1/federation/peer`. Every CLI verb that exists handles the RECEIVING
side — see a request, accept it, reject it, revoke it. Nothing initiates one.

So an operator using this CLI can join a federation somebody else starts and
cannot start one. Whether they should be able to, and what the verb is called,
belongs in `architecture/cli.md`. It is not this repo's to invent.

### 3. One generation pipeline — done and verified

`agent create`, `domain create` and `gateway create` call
`SupervisedComponentInit`. The single-shot path that made one `provider.Chat`
call and split the reply on `// filename:` markers is deleted, with its three
system prompts and three prompt builders.

**Verified by a real generation on 2026-09-11**, which is the standard this
entry set after the first attempt "compiled, passed the whole suite, and was
wrong":

```
  Plan accepted: 24 files in ./
    cmd/alerting/main.go — The binary...
    internal/agents/alerting/*.go ...
    internal/protocol/, internal/identity/, internal/observability/, internal/agent/
  [ok] Generated 24 files in ./ (11m13s)
  [ok] builds with "go build -o bin/alerting ./cmd/alerting"

$ weblisk agent list
    alerting         [go]  cmd/alerting/
$ weblisk validate
  [ok] agent alerting: platform=go, in cmd/alerting/
  [ok] agent alerting: protocol markers present
$ weblisk agent start alerting
  Building agent alerting...
  {"ts":"...","level":"warn","msg":"private key is unencrypted — development only","component":"alerting"}
```

`go build ./...` run independently in the tenant also passes. The plan named
`cmd/alerting/main.go`, **not** `cmd/agent/main.go` — the exact failure that
caused the revert — and `internal/agent/` (the framework) was correctly
permitted alongside `internal/agents/alerting/` (this instance).

Conformance then found two real faults in the generated agent, which is the
layer doing its job rather than a pipeline failure: `L1-01 Health Check` and
`L1-07 Protected Endpoints Require Auth` both answered **405**, a
method-mismatch between what the agent routes and what the harness requests.
Recorded in item 4. L4 correctly reported 4 tests unrun — this tenant has no
orchestrator — and said out loud that unrun is NOT a pass.

**Why the first attempt failed, kept because the lesson is the expensive part.**
It set `plan.Root` to the component's directory, `agents/<name>`, and the whole
pipeline assumes `plan.Root` is `"."` — the build runs at the tenant root, the
module path has no `agents/<name>` segment, and manifests record tenant-root
paths. A real run planned eighteen files at tenant-root paths and reasoned
about importing the tenant's own packages, because it had been told it was part
of the tenant module.

Underneath sat what looked like a decision — is a component a package set or its
own module — and is not one. The platform blueprints answer it, differently:

| Platform | Where an agent goes | Own build manifest |
|---|---|---|
| `platforms/go.md` | `cmd/<name>` + `internal/agents/<name>` | no |
| `platforms/node.md` | `src/agents/<name>` | no |
| `platforms/cloudflare.md` | `agents/<name>` with its own `wrangler.toml` | yes |
| `platforms/rust.md` | `agents/<name>` as a workspace member | yes |

So `plan.Root` stays `"."` everywhere and the component's directories appear as
a PREFIX inside the plan's own paths. `internal/dispatch/layout.go` reads them
from the platform blueprint.

**What the switch needed beyond that**, each found by running it rather than
reading it:

- The per-instance blueprint reaches the graph. Without `agents/<name>.md`, two
  agents are planned from byte-identical prompts.
- `locate.go`, so a component can be found again. `agent start` and `agent list`
  read `agents/<name>/` and looked for a `go.mod` — the shape the deleted
  generator wrote. The switch would have produced Go agents that build and
  cannot be started or listed.
- `weblisk validate` walked `agents/` for a `go.mod` and would have reported
  every correctly generated Go agent as "no platform detected".
- The stop message named `--resume`, which only `server init` accepts.
- Every directory `ValidatePlan` rejects is now named in the prompt, so a guard
  cannot fire on something the instruction never mentioned.

**Known behaviour changes**

- `agent create <existing>` rebuilds rather than refusing.
- An agent on disk from the OLD generator — `agents/<name>/` with its own
  `go.mod` — is invisible to `agent list`, `start` and `validate`. Regenerate it.
- `domain create` and `gateway create` share the verified code path but were not
  themselves run end to end.

### 4. `test conformance` — honest now, still mostly unimplemented

**Settled: the harness was wrong, and it is fixed.** The 2026-09-11 run
reported a conformant agent as failing two L1 tests, both with **405**. The
blueprints are unambiguous — `architecture/agent.md` declares
`| POST | /v1/health |` and says in prose "POST /v1/health rather than GET is
deliberate and is the protocol's choice", while `protocol/spec.md` gives the
ORCHESTRATOR `GET /v1/health`. The harness issued GET to everything.

Three faults, one shape — a test asking a question the specification did not ask:

| Fault | Effect |
|---|---|
| `L1-01` hardcoded `get(base, "/v1/health")` | every conformant agent answered 405 and was failed |
| `ProtectedGETsFor` discarded every non-GET protected endpoint | an agent, whose `/v1/services` is POST, produced an empty list |
| the empty list fell back to the ORCHESTRATOR's surface, probed with GET | a conformant agent was reported as serving a protected endpoint without auth |

Each test now asks with the method that component's own blueprint declares, the
fallback list is gone (`protectedEndpoints` returns nothing when the corpus
declares nothing), and a probe of nothing is **unrun rather than failed** — the
same distinction the structural checks draw, since failing a component for want
of a list is failing a question never put to it.

Re-run against the same generated agent: `L1-07` and `L1-10` now pass. `L1-01`
now reports a REAL fault it had been masking — the agent answers `503` where
`architecture/agent` reserves 503 for shutdown ("stop accepting new tasks"), and
reports `"unhealthy"` where `L4-04` requires `"degraded"` with no orchestrator.
The failure detail now names both, because conformance results feed the repair
round.


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

### 5. Structural checks that verify what they never read — swept

`internal/dispatch/verify.go`'s "HTTP handlers do not panic" check returned
`OutcomeVerified` — the strongest positive verdict — when every Go file failed
to parse, or when the tenant contained no handler-shaped function at all. It
searched, found no panic, and reported that as proof. Fixed 2026-09-09.

That entry asked for a sweep of the rest. **Seven more had it**, all the same
shape: search an index, find nothing wrong, report that as proof. Fixed as a
precondition rather than eight edits — `structuralCheck.reads` names what a
check needs and reports what the context does not carry, and the runner turns
that into `OutcomeInconclusive` before the check runs.

Two were worse than the class:

| Check | What it did |
|---|---|
| `registered codes carry the status the protocol assigns` | returned a PASS when the blueprints carried no error-code table, under a comment reading *"No table to check against: say so rather than pass"* |
| `referenced endpoint is routed` | reported "no handler is registered" when NO route registration of any kind had been read. It already treated the readable-but-unresolvable case as inconclusive; the nothing-at-all case fell through to a refutation — the same failure `d51d9ab` records as having refuted twenty-one correct assertions in one run |

`TestNoCheckReachesAVerdictFromAnEmptyArtifact` walks every check with a probe
assertion it applies to, and fails if a check is added without one.

### 6. Marketplace: the two mismatched rows are closed

| Spec | Was | Now |
|------|-----|-----|
| `weblisk marketplace info <id>` | no `info` verb — it errored. The capability existed as `describe` | both spellings reach the same request; the usage line echoes whichever was typed |
| `weblisk marketplace list` | reads the LOCAL activation store and makes no HTTP request, while the help called it "List active purchases and subscriptions" | the help says "Products activated on this machine", and the output says it read the local store and made no request |

`list` was NOT changed to call the hub. Nothing in this package requests a
purchase list, so an endpoint for one would have been invented here rather than
read from the specification — and the local store is what `activate` and
`remove` write, so listing it is a real answer to a real question. Seller-side
state is `marketplace dashboard`.

What remains unestablished is that each verb round-trips against a real hub —
which is conformance coverage, and blocked on item 4.

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
