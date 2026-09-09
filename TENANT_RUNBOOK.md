# Standing up a tenant, by hand

Every command here was run against a real tenant before it was written down.
Nothing is inferred from help text.

Two words, used precisely, because they had drifted:

| Word | What it means |
|---|---|
| **tenant** | one organisation's deployment — Studio's word, and the SaaS one |
| **orchestrator** | the server a tenant runs (`cmd/orchestrator`) |
| **hub** | the FEDERATION layer, where tenants publish and discover across organisations — `architecture/hub.md`. **Not** a synonym for a tenant's orchestrator |

---

## 0. What can generate?

```
weblisk providers            # what this machine offers, and which it would pick
weblisk providers --json     # the same, for a console
```

Discovery probes for local coding-agent CLIs (a binary that exists **and**
runs — Claude Code, Grok, Codex), for local HTTP servers that answer **and**
have a model loaded (Ollama, LM Studio), and for hosted API keys (`XAI_API_KEY`,
`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, and the others in `weblisk providers`).
Weight is local first: `claude-code · grok · codex · ollama · lmstudio · …`.
When nobody pins a backend, a build takes the highest-weighted one this
workstation can actually run. Pin one with `--provider`, `WL_AI_PROVIDER`,
or per tenant in Studio.

---

## 1. One command

```
weblisk tenant create "Acme Corp" \
    --provider claude-code \
    --port 9860 \
    --json                          # one progress object per line
```

The passphrase is read from **stdin**, never argv:

```
printf '%s\n' "$PASSPHRASE" | weblisk tenant create "Acme Corp" --provider claude-code
```

Six steps, in an order where each depends on the one before:

```
provider   → settled BEFORE any work; no usable model discovered after ten
             minutes of generation would waste all of it
directory  → refused if it already holds a tenant, unless --resume
generate   → the orchestrator, from the blueprints, cached per file
skills     → .agents/skills (and .claude or .grok) for the tenant verb
             — blueprints, hubs, tenants, operators, plus go when the
             platform is Go. Agent/domain/gateway verbs add their own.
provision  → go build, start detached, wait for it to listen, write the
             bootstrap secret, claim the first operator
accept     → ask the running tenant whether it WORKS: health, and every
             method-and-path the CLI and Studio actually issue
```

`provision` starts the hub itself. It did not, and the step said it did: the
underlying `server provision` establishes a credential against a tenant that is
already running and refuses one that is not, so this command failed at its last
step on every fresh directory — after the whole generation had succeeded — with
`the orchestrator is not running / Start it first: weblisk server start --detach`.
Correct advice, which is why it read as a next step rather than as a defect.

`accept` is why "created" now means something. A non-fatal failure there does
not fail the build; it names a capability that tenant does not have, usually
because it was generated from an older specification.

Options: `--dir <path>` · `--platform go|cloudflare|node|rust` ·
`--model <name>` · `--operator <name>` · `--resume`

---

## 2. The same thing, one step at a time

Useful when a step fails and you want to repeat only that step.

```
mkdir acme && cd acme

# Which blueprints will this read? Check BEFORE generating.
weblisk doctor                       # names source, kind, revision, fetch age

# Generate. --resume reuses every cached file whose inputs have not changed.
weblisk server init --platform go --provider claude-code
weblisk server init --platform go --provider claude-code --resume

# Run it. --detach RECORDS where it bound, which is what makes status/logs/stop work.
weblisk server start --port 9860 --detach
weblisk server status                # recorded | running | stale, and the address
weblisk server status --json
weblisk server logs --tail 200 --follow
weblisk server stop

# Prove it.
weblisk server verify --url http://localhost:9860
```

**A tenant started by hand has no run record.** `./bin/orchestrator --port 9860`
works, but `status`, `logs` and `stop` will all report "not started" while it
serves happily. Use `--detach`.

---

## 3. The credential

```
weblisk operator connect --orch http://localhost:9860 --name lloyd
weblisk operator connect --orch http://localhost:9860 --name lloyd --json
```

- One identity per account, reused for **every** tenant. The passphrase is the
  one that identity was created with, not a per-tenant secret.
- The first operator of a tenant is auto-approved; every later one lands at
  `pending` and needs an existing admin to admit them:

  ```
  weblisk operators list                  # who is waiting
  weblisk operators approve alice         # admit them
  weblisk operators role alice operator   # viewer | auditor | operator | admin
  weblisk operators revoke alice --confirm
  ```

  The approved operator then runs `weblisk operator token` themselves — approval
  never hands a token to whoever ran it.
- Repeating a connect inside the same second is refused as a **replay** — the
  tenant will not accept the same signed request twice. Wait a moment.
- A tenant built before 2026-09-06 has **no approve route** and cannot admit
  anybody; `weblisk operators approve` will say so. Regenerate it with
  `weblisk tenant create … --resume` to pick the route up.

---

## 4. Reading a running tenant

```
curl http://localhost:9860/v1/health                 # the only endpoint needing no token
curl -H "Authorization: Bearer $TOKEN" http://localhost:9860/v1/services
curl -H "Authorization: Bearer $TOKEN" http://localhost:9860/v1/admin/overview
curl -H "Authorization: Bearer $TOKEN" http://localhost:9860/v1/audit
```

Expect `401` without a token and `405` for a wrong method — both are the tenant
behaving correctly. **`404` is not**: it means this tenant does not serve that
route at all, which is what `weblisk tenant create` now checks for itself at the
`accept` step rather than leaving to be found by a command months later.

---

## 5. In Studio

**Settings → Organizations** carries the two acts, and they are different:

| | |
|---|---|
| **Connect tenant** | one already runs — needs an address and a credential, nothing else |
| **Create tenant** | generate one from the blueprints, then start it |

Anything left blank on Create is **inherited from Studio**: its platform, its
model backend, its workspace directory.

The address identifies the tenant. Connecting to an address Studio already knows
**reconnects** to that tenant rather than creating a second one for the same
orchestrator.

Equivalent API, if you would rather drive it directly:

```
POST /api/tenants/connect   {address, operator, passphrase, name?}
POST /api/tenants/create    {name, operator, passphrase, platform?, provider?}
GET  /api/hub/lifecycle     # recorded / running / stale, and where
GET  /api/hub/status        # reachable, and admitted
GET  /api/hub/read/{view}   # overview | services | audit | operators | agents
GET  /api/providers         # discovered, and what this tenant would use
```

---

## 6. When it does not work

| Symptom | Cause |
|---|---|
| `WL_AI_KEY required for OpenAI` | no provider chosen and none discovered — run `weblisk providers` |
| `the passphrase does not open it` | the account's existing identity, wrong passphrase — it is one identity for every tenant |
| `already been seen` | a replay, inside the tenant's window. Wait a moment |
| `not started` while it is serving | started by hand rather than `--detach` |
| conformance repairs against rules you replaced | the shared cache is behind — `weblisk doctor` prints the revision |
