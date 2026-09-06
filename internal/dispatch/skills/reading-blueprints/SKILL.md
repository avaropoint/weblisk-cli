---
name: reading-blueprints
description: How to read a Weblisk blueprint before changing code in this tenant — the declaration block, the required sections, and the rules that decide whether an edit is conformant. Use whenever you are about to edit generated code, add a component, or answer a question about why a file looks the way it does.
---

# Reading a blueprint in this tenant

This code was **generated from blueprints**. They are the specification; the
code is one conformant implementation of it. When the two disagree, the
blueprint is right and the code is a defect.

## Where they are

Resolved in order, first hit wins:

1. `blueprints/` in this tenant — a working checkout, if one exists here
2. custom sources named by `WL_BLUEPRINT_SOURCES`
3. the shared cache, `~/.weblisk/blueprints`

Most tenants have no local `blueprints/` and read from the cache. Do not assume
the directory exists; `weblisk blueprint update` refreshes whichever sources
apply, and `weblisk validate` checks this tenant against whatever resolved.

Read the blueprint **in full** before writing. Not the section that looks
relevant — the whole file. Every section constrains the others.

## The declaration block

A migrated blueprint carries one machine-readable `declaration:` block, and it
is the fastest way to understand what a component owes:

| Key | Means |
|---|---|
| `requires` | blueprints this one is derived from — read those too |
| `declares` | the names this blueprint OWNS. Nothing else may define them |
| `serves` | operations this component must expose |
| `checks` | expectations, each with an explicit subject |

A blueprint carries `## Declaration` **or** `## Dependencies`, never both. If you
find both, the file is mid-migration and the declaration wins.

## Declared names are binding

A name that appears in `declares` belongs to that blueprint. Do not invent a
synonym, do not rename it to match local style, and do not define it a second
time in another package. A generator holds no naming convention of its own —
naming belongs to the declaring blueprint.

## The verification checklist

`## Verification Checklist` is prose, one assertion per line, and every
identifier in it is in backticks. That is not decoration: the tooling reads
identifiers from backticked spans only, so **an unbackticked identifier silently
disables the check that would have verified it**.

When you add an assertion, backtick the identifiers.

## Section form

Each schema's Required Section Order table gives every section a form —
`narrative`, `table`, `yaml`, `yaml:<root>`, or `structured`. Write the form the
table declares. A section in the wrong form is not read by anything.

## What not to do

- **Do not restate a rule** that another blueprint already states. One question,
  one implementation — a second copy drifts and then two things disagree.
- **Do not put a mechanism in a specification.** A blueprint says *what* must be
  recorded and *how it must behave*, never which database. Storage mechanism is
  a platform decision and lives only in a platform or tool blueprint.
- **Do not "fix" a gap by inference.** If a blueprint is silent or contradicts
  itself, say so. A band-aid in the code hides a fault in the specification, and
  the specification is the thing that has to be right.

## Checking your work

```bash
weblisk validate          # blueprint conformance across this tenant
weblisk doctor            # project health and configuration
```
