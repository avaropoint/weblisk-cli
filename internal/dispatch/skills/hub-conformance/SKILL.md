---
name: hub-conformance
description: How to verify this tenant's hub actually works — the endpoints it must serve, the conformance levels, and how to run and inspect it. Use when a build finishes, when an endpoint misbehaves, or before claiming a change is done.
---

# Verifying this hub

A hub that compiles is not a hub that works. Conformance is the evidence, and it
either passes or it does not.

## Running it

```bash
weblisk server start --detach   # starts it and RECORDS where it bound
weblisk server status           # recorded / running / stale, and the address
weblisk server logs --follow    # structured JSON, one object per line
weblisk server stop
```

Start it with `--detach`, not by running the binary directly. A hub started by
hand has no run record, so `status`, `logs` and `stop` all report "not started"
while it serves happily — and Studio's lifecycle panel stays blank.

## The endpoints

`GET /v1/health` is the only one requiring no authentication. Everything else
needs a token, which is how reachability and admission stay separate questions.

| Endpoint | Auth | Purpose |
|---|---|---|
| `GET /v1/health` | no | name, status, version, uptime |
| `POST /v1/register` | identity | agent registration |
| `GET /v1/services` | yes | signed service directory |
| `POST /v1/channel` | yes | direct agent-to-agent channel |
| `POST /v1/rotate-key` | yes | dual-signed key rotation |
| `GET /v1/audit` | yes | the hash-chained audit log |
| `/v1/admin/*` | capability | operators, agents, overview |

## Verifying

```bash
weblisk server verify --url http://localhost:9800
```

L1-01 and L1-02 are the floor: the hub answers `/v1/health` and describes itself
with its public key. Below that nothing else is worth checking.

## Reading the evidence

The audit log is a hash chain. `GET /v1/admin/overview` reports
`chain_checked` and `chain_valid` — a chain that has not been checked is not a
chain that passed, and the two must never be collapsed into one green light.

## When something is unavailable

The hub reports **why** — "no component publishing the `workflow` namespace is
registered with this orchestrator". Keep that sentence. A blank panel says the
same thing while telling the operator nothing, and a confident zero is worse
than an honest gap.
