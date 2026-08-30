# Creating a tenant

> Design note, not yet implemented. The subject is one thing: turning a name into
> a running hub that Studio is admitted to, in one command, identically from the
> CLI and from the GUI.

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

| Missing | Why it matters |
|---|---|
| **`tenant` / `hub` verb** | There is no single operation that creates a tenant. A person must know to run five commands in the right order, and get the ordering right themselves |
| **Bootstrap** | Nothing issues a first grant. `operator register` assumes an orchestrator is already running and will accept you |
| **`connect`** | No way to attach to a hub this machine did not create |
| **Grants** | No invite, list, or revoke. `patterns/principal-identity` specifies them; nothing implements them |
| **Liveness with authority** | `status` says a hub answers. Nothing says "and I am admitted to it" |

## What a tenant is on disk

Taken from `weblisk/.weblisk/`, which is a real instance, rather than invented:

```
avaropoint/
  .weblisk/
    config.yaml        hub name, orchestrator port, platform
    entity.json        what this tenant IS — name, type, description
    keys/
      orchestrator.key ML-DSA-65 private key, argon2id at rest
      orchestrator.pub the hub's identity, published
    grants/            who may enter, one file per grant
    bootstrap          one-time secret; deleted the moment it is claimed
  domains/
  agents/
  blueprints/
```

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
    Create(ctx, Spec) (<-chan Progress, error)
    Connect(ctx, addr, credential) (*Hub, error)
    Status(ctx, *Hub) (Status, error)
```

- The CLI's `tenant` command is a thin wrapper that prints progress
- Studio imports the same package and renders progress in the UI
- Neither owns the operation

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

Four things make it hold. Only the first exists today.

### 1. The blueprint is the template

Not a directory of files to copy — a specification precise enough that any
conformant implementation behaves identically. This is what "templated" means
here, and it is why file templates were the wrong instinct: a copied file fixes
the text and says nothing about behaviour, while a blueprint fixes the behaviour
and leaves the text free.

### 2. The output contract must be enforced, not requested

Today the generator is ASKED, in prose, to prefix each file with
`// filename: <path>`, and a regex the model never sees decides whether it
complied. A convention in a paragraph is honoured differently by different
models, which is precisely the non-determinism this section exists to remove.

The contract must be machine-checkable and retried on violation, rather than
discovered as "AI returned no code files" after several minutes.

### 3. Generation is per-file, driven by a declared manifest

One call asking for a whole orchestrator has no checkpoint, no progress, and no
attributable failure — and it failed twice here, once by timeout and once by
returning nothing usable.

The blueprint should declare WHICH files must exist. Then generation is a loop
over that manifest, one file per call. Four consequences, all of them the ones we
need:

- The file set stops being the AI's choice and becomes the blueprint's
- Failure is attributable to a file rather than to "the hub"
- Progress is real, which is what the GUI needs anyway
- A failed file is retried without regenerating the other nineteen

### 4. Structure is checked before behaviour

With a declared manifest, output is verifiable before anything runs: are all the
required files present, do they compile, does each expose what the blueprint said
it would. Only then is the conformance suite worth running. A structural failure
diagnosed in seconds beats the same failure diagnosed as a conformance error
minutes later.

### The resulting claim

> Generated by any model, from blueprints at a recorded version, structurally
> verified against a declared manifest, and passing conformance L1–L3.

That is a claim a governance product can make about its own substrate. "An AI
wrote it and it seemed fine" is not.

## Open questions

1. **Generation or hand-written skeleton?** `weblisk server init` failed twice —
   once on timeout, once returning nothing parseable. The seven-endpoint hub is
   small enough to write by hand, and a working reference makes generation more
   likely to succeed afterwards because there is something to diff against.
2. **Where does Studio put a tenant?** `weblisk new` creates in the working
   directory. Studio has no working directory, so a parent path is an explicit
   input on the form and cannot be inherited.
3. **One hub per tenant, or per org?** Settled for tenants
   (`protocol/federation` establishes trust orchestrator-to-orchestrator). Not
   settled for organisations inside one.
