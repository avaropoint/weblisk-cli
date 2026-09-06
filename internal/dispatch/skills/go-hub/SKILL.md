---
name: go-hub
description: The Go-specific rules that this tenant's hub is generated under, and the two that panic at startup if broken. Use before editing routing, handlers, or anything under internal/protocol in a Go tenant.
---

# Writing Go in this tenant

The tenant is **one module**, rooted at the tenant directory. Shared code lives
once in `internal/` and is imported — never copied between components. Every
package is named after the blueprint that specifies it, which is what makes the
layout derivable rather than a matter of taste.

There is no `server/` package: the artifact is an *orchestrator*, which is the
blueprint's name and the binary's name.

## Two rules that panic at startup

These are not style. `net/http`'s router enforces them, at registration time, by
panicking — so breaking one produces a hub that builds and dies on launch.

**1. No method-less pattern beside a wildcard sibling.**

```go
mux.HandleFunc("/v1/admin/operators/{name}", h)   // wildcard sibling
mux.HandleFunc("/v1/admin/operators", h)          // PANICS: no method
mux.HandleFunc("GET /v1/admin/operators", h)      // correct
```

Always register with an explicit method.

**2. No path literals reach routing.**

Paths come from `protocol.Path*` constants, never from a `"/v1/..."` string at
the call site. A literal is a second definition of a name the protocol blueprint
already declares, and the two drift silently.

## Errors say what to do

An error returned to an operator names the thing that is wrong and the action
that fixes it. `"not found"` is not an error message; `"no hub answered at
http://localhost:9820 — is it running?"` is.

## `omitempty` does not omit a struct

`time.Time` with `,omitempty` is serialised as `"0001-01-01T00:00:00Z"` — a
non-empty string, which is truthy in a browser. Either make the field
`*time.Time` so absence is representable, or drop the tag so zero is honestly
the sentinel. Do not leave the tag on a value type.

## Before you say it works

```bash
go build ./... && go vet ./... && gofmt -l .
weblisk server verify
```

`gofmt -l` returning any filename is a failure, not a suggestion.
