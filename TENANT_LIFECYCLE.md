# Creating a tenant

> The subject is one thing: turning a name into a running hub that Studio is
> admitted to, in one command, identically from the CLI and from the GUI.
>
> **Built as of 2026-09-06.** `pkg/tenant` is the operation; `weblisk tenant
> create` and Studio's "New tenant with a hub" are both thin wrappers over it.
> What is done and what is not is recorded in "Where we are" below.

## The bar

```
$ weblisk tenant create avaropoint
```

One command. It ends with a hub running, a first grant issued, and an address to
connect to. Everything below exists to make that sentence true — and to make the
Studio form do exactly the same thing, because it calls the same code.

## Where we are

The CLI already has more of this than it looks. What is missing is not the parts —
it is the verb that puts them in order.

| Have | Command |
|---|---|
| Scaffold a project | `weblisk new` |
| Operator identity | `weblisk operator init` / `register` / `token` / `rotate` |
| Generate an orchestrator | `weblisk server init` |
| Run one | `weblisk server start` / `verify` |
| Agents, domains, gateway | `agent create`, `domain create`, `gateway create` |
| Secrets | `weblisk secret …` |
| Inspection | `status`, `agents`, `operators`, `audit` |

| Was missing | Now |
|---|---|
| **`tenant` verb** | **Done** — `weblisk tenant create <name>`, over `pkg/tenant`. Studio drives the same package through `--json` progress |
| **Bootstrap** | **Done** — `server provision` writes the secret, starts the hub and claims it in one action; `pkg/tenant` calls it as step 5 |
| **`connect`** | **Done** — `weblisk operator connect --orch <url>`, address-based, needs no local project |
| **Liveness with authority** | **Done** — reachable and admitted are asked and reported apart, in the CLI and in Studio's hub panel |
| **Which model** | **Done** — `weblisk providers` discovers what the machine has; the choice is per tenant over an installation default, and is never guessed when several exist |
| **Grants** | **Still missing.** No invite, list, or revoke. `patterns/principal-identity` specifies them; nothing implements them |

## What a tenant is on disk

Taken from `weblisk/.weblisk/`, which is a real instance, rather than invented:

```
avaropoint/              the tenant IS the root — of the directory and of the module
  .weblisk/
    config.yaml          hub name, orchestrator port, platform
    entity.json          what this tenant IS — name, type, description
    keys/
      orchestrator.key   ML-DSA-65 private key, argon2id at rest
      orchestrator.pub   the hub's identity, published
    grants/              who may enter, one file per grant
    bootstrap            one-time secret; deleted the moment it is claimed
  blueprints/            the blueprints this tenant has adopted

  go.mod                 the tenant is one module, rooted here
  cmd/                   one directory per binary
    orchestrator/        <- architecture/orchestrator
    admin/               <- architecture/admin
    <name>/              <- agents/<name>
  internal/              one directory per library, named after its blueprint
    protocol/  identity/  storage/  observability/
    orchestrator/  admin/
    agent/               the framework every agent imports
    domain/              the workflow engine
    agents/<name>/       one agent's own logic
  bin/                   build output, not source
```

Everything a tenant owns is scoped to that one directory, and it is **one
module**: shared code lives once in `internal/` and is imported, never copied
between components.

**Every package is named after the blueprint that specifies it.** That is what
makes the layout derivable rather than a matter of taste — and it is why there is
no `server/`: the artifact is an *orchestrator*, which is the blueprint's name
and the binary's name, while `server` corresponds to no blueprint and so traces
back to nothing.

A **domain controller is an agent** — `architecture/domain` registers it with the
same `AgentManifest`, distinguished by `type: "domain"`, serving the same six
protocol endpoints — so both import `internal/agent`. `domains/` sits beside
`agents/` because the two are operated differently, not because they are
different species.

The per-platform layout belongs to the platform blueprint; see
[`platforms/go.md`](../weblisk-blueprints/platforms/go.md). A tenant with no
agents has no `agents/` directory: the structure follows what has been adopted.

Nothing here is a template. Every file is either generated (keys), written from
answers (`config.yaml`, `entity.json`), or created empty (`grants/`). That is the
point of starting over on this: a tenant skeleton is not a directory to copy, it
is a directory to *produce*, and copying is what let the CLI and the templates
drift apart in the first place.

`weblisk new` remains what it is — scaffolding an application. Creating a tenant
is a different operation that happens to produce some of the same directories.

## The flow

Seven steps, in this order, because each depends on the one before:

| # | Step | Notes |
|---|---|---|
| 1 | **Identity exists** | `~/.weblisk/keys/operator.key`, created if absent. Identity precedes tenancy — a hub with nobody able to enter it is unreachable and unrevocable |
| 2 | **Directory** | Refuse a non-empty target rather than merging into it |
| 3 | **Hub identity** | Generate the orchestrator ML-DSA-65 keypair |
| 4 | **Config** | `config.yaml` and `entity.json` from answers, no placeholders left |
| 5 | **Bootstrap secret** | Written to `.weblisk/bootstrap`, printed once. Claiming it is the ONLY way to obtain the first grant |
| 6 | **Start** | The hub comes up and answers `/v1/health` |
| 7 | **Claim** | The operator presents the secret and the first grant is written. The secret file is deleted. The window never reopens |

Steps 5 and 7 are what make this safe to run on a machine with a network
interface. "First to reach the endpoint wins" is a land grab; holding a secret
that was printed on the operator's own terminal is not.

For a local `tenant create`, steps 5–7 collapse — the CLI holds the secret it
just wrote and claims it immediately. The operator sees one command. The
mechanism is identical either way, which is what keeps the local path from
being a special case that skips a check.

## The minimum hub

For Studio to consider a tenant alive, the generated hub needs seven endpoints —
not the full orchestrator:

| Endpoint | Purpose | Conformance |
|---|---|---|
| `GET /v1/health` | name, state, version, uptime | **L1-01** |
| `POST /v1/describe` | the manifest, with public key | **L1-02** |
| `POST /v1/bootstrap` | claim the secret, receive the first grant | — |
| `POST /v1/auth` | present a credential, receive a WLT | — |
| `GET /v1/grants` | who may enter | — |
| `GET /v1/services` | the directory, empty at first | — |
| `GET /v1/events` | SSE, per `architecture/gateway` | — |

That is a few hundred lines and it passes L1-01 and L1-02 on the first run, which
is the first conformance evidence this project has ever had.

`protocol/spec.md` fixes the transport: HTTP is the only communication layer, no
broker and no bus. `architecture/gateway.md` fixes real-time: polling by default,
SSE where it is needed. Neither is a decision left to make.

## One implementation, two front ends

The requirement is that the CLI and Studio achieve the same thing. The only way
that holds is if they are the same code — parity by construction, because two
implementations of one operation drift by default.

The CLI's logic is under `internal/`, which cannot cross a module boundary. So
the tenant lifecycle moves to a public package:

```
weblisk-cli/pkg/tenant
    Create(ctx, Spec) (<-chan Progress, error)      built
    Connect(ctx, addr, credential) (*Hub, error)    still the CLI's operator connect
    Status(ctx, *Hub) (Status, error)               still server status --json
```

- The CLI's `tenant` command is a thin wrapper that prints progress — built
- Studio drives the same package and renders progress in the UI — built
- Neither owns the operation

**Studio drives it over `--json`, not by importing the package.** The two are
separate Go modules and separate repositories; a compile-time dependency would
couple Studio's build to the CLI's internals, and the boundary that already
works everywhere else here is that Studio commands the CLI and never reaches
into a tenant's filesystem. The progress stream is the interface — the same
shape as `providers --json` and `server status --json` — so there is still
exactly one implementation of the operation.

The steps are a contract: `provider · directory · generate · skills · provision
· done`. Both front ends render those names, so renaming one breaks both at
once and is guarded against in `pkg/tenant`'s tests.

`Create` returns a **progress channel** rather than blocking. Hub generation is
minutes long; a GUI that cannot show what is happening during it will grow its
own parallel implementation to get progress, and then there are two.

## What each repository documents

| Repository | What it says about this |
|---|---|
| **weblisk-blueprints** | The specification: the seven steps, the bootstrap rule, the endpoint set. Both implementations conform to it rather than to each other |
| **weblisk-cli** | The commands, and `pkg/tenant` as a public API |
| **weblisk-studio** | That it drives `pkg/tenant` and never reimplements it |
| **weblisk-server** | The generated hub — currently an empty repository |
| **weblisk-templates** | That it scaffolds APPLICATIONS, and that tenants are produced rather than copied |
| **weblisk-public** | The quickstart: one command, what it produces, how to connect Studio |
| **weblisk** | The repository table, which does not yet mention weblisk-studio |

## Repeatability: what "the same outcome" has to mean

The philosophy is that blueprints drive the output and the AI writes the code.
For that to be trustworthy, the same blueprints must produce the same hub whoever
generates it. One reading of that is impossible and one is achievable, and the
difference is the whole design.

**Byte-identical source is not achievable.** Two models — or one model twice —
will name a variable differently, order functions differently, split a file
differently. Demanding textual identity would mean abandoning generation.

**Behavioural equivalence is achievable, and it is what repeatable must mean.**
Same endpoints, same request and response shapes, same error codes, same state
transitions, same conformance result. Two hubs generated by different AIs are the
same hub in every respect a caller can observe.

That is not a lowering of the bar. It is the bar that can be *verified* —
`architecture/testing.md` L1-01 through L3 either pass or they do not, and no
amount of stylistic difference changes that answer.

Four things make it hold. Three of them exist now.

### 1. The blueprint is the template

Not a directory of files to copy — a specification precise enough that any
conformant implementation behaves identically. This is what "templated" means
here, and it is why file templates were the wrong instinct: a copied file fixes
the text and says nothing about behaviour, while a blueprint fixes the behaviour
and leaves the text free.

### 2. The output contract is enforced, not requested — done

The generator used to ASK, in prose, for each file to be prefixed with
`// filename: <path>`, and a regex the model never saw decided whether it
complied. A convention in a paragraph is honoured differently by different
models, which is precisely the non-determinism this section exists to remove.

Generation now asks for exactly one file per call, and a rejected response is
retried with the reason. The failure mode it replaced — "AI returned no code
files", discovered after several minutes — cannot recur, because no single
response carries the whole hub.

### 3. Generation is per-file, from a plan validated against the blueprints — done

One call asking for a whole orchestrator has no checkpoint, no progress and no
attributable failure, and it failed twice here: once by timeout, once by
returning nothing usable.

The first design for the fix was a **declared manifest**: the blueprint naming
which files must exist. It was built, and then removed, because it was the wrong
instinct in the same way file templates were. A manifest listing four types where
`protocol/types.md` defines fifty-five does not constrain generation — it
*narrows* it, and the narrowing is invisible, because the pipeline reports
success against the manifest it was given rather than against the specification.
A tooling artifact had quietly become the contract.

What is there instead:

- The **model plans** the file set, and states for each file what it will declare
  and which endpoints it will serve
- The plan is **validated against requirements extracted from the blueprints** —
  every type, every endpoint, every checklist assertion must be claimed by some
  file before a line is generated
- The plan is **cached on those requirements**, so unchanged blueprints produce
  the same file set run after run. This is what makes per-file caching work at
  all: a model that plans ten files where it planned twelve invalidates every
  file's cache entry
- The file set is therefore neither the tooling's choice nor an unconstrained
  model's — it is whatever satisfies the specification

The other three consequences hold as before: failure is attributable to a file,
progress is real, and a failed file is retried alone.

### 4. Structure is checked before behaviour — done

Four layers, in cost order, each one authoritative over the one before:

| Layer | Checks | Authority |
|---|---|---|
| 1 | the response is the file, and declares what the plan said | a pre-filter only |
| 2 | it compiles | the compiler is the authority |
| 3 | the blueprints' Verification Checklists | drives the loop |
| 4 | conformance L1–L3 | not built yet |

Layer 3 is the one that changes what "done" means. It reports four outcomes —
`verified`, `failed`, `necessary`, `unchecked` — and the loop continues while
anything is `failed`. Before that, the loop stopped when the compiler was happy
and printed the checklist afterwards, which meant a hub could violate the
blueprints it was generated from and still report success.

`necessary` exists because the interesting assertions are only partly checkable.
"POST /v1/register enforces exclusive namespace ownership (409 on conflict)"
holds a structural claim a parser settles and a behavioural claim it cannot.
Calling that a pass would be a confident wrong answer; calling it unchecked would
throw away a cheap detection of a definite fault.

Layer 4 is the gap. Until the conformance suite exists, behavioural assertions —
"all stores survive process restart", "retries with exponential backoff" — are
reported as unchecked, and they are the majority.

### The resulting claim

> Generated by any model, from blueprints at a recorded version, structurally
> verified against a declared manifest, and passing conformance L1–L3.

That is a claim a governance product can make about its own substrate. "An AI
wrote it and it seemed fine" is not.

## Open questions

1. **Generation or hand-written skeleton?** Settled in favour of generation. The
   two early failures were both tooling faults, not evidence against the
   approach: one call for the whole hub with a prose output convention. Per-file
   generation from a validated plan reaches a build reliably. The hand-written
   reference is no longer the shorter path, and it would have to be maintained
   against the blueprints by hand — which is the coupling this project exists to
   remove.
2. **Where does Studio put a tenant?** `weblisk new` creates in the working
   directory. Studio has no working directory, so a parent path is an explicit
   input on the form and cannot be inherited.
3. **One hub per tenant, or per org?** Settled for tenants
   (`protocol/federation` establishes trust orchestrator-to-orchestrator). Not
   settled for organisations inside one.
