# go-ctl2

`go-ctl2` is an experimental actor/message modeling tool with:

- a small Lisp reader
- a compiled actor IR with explicit named control states
- model-level actor-role declarations backed by actor behavior templates
- runtime execution with mailbox semantics
- CTL model checking
- Mermaid diagrams
- UML-like class diagrams for actor-local variable reads and writes
- event-log-driven metric plots

The intended workflow is:

1. an LLM drafts a model in Lisp
2. a human reviews the states, transitions, predicates, and diagrams
3. the same artifact is used for execution, CTL checks, and documentation

The design bias is toward inspectability rather than a large specification language.

## Current Model

The current repository models:

- one mailbox per actor
- actor behavior templates declared as `(actor RoleName ...)`
- runtime actors declared as `(instance ActorName RoleName (PeerRole InstanceName)...)`
- model-wide control steps declared once as `(step Name)`
- no implicit actor instantiation inside `(model ...)`
- explicit named control states
- guarded transitions
- one floating-point `Dice` sample in `[0,1]` per attempted step
- successor sets derived from `become` calls in transition bodies
- buffered or zero-capacity rendezvous mailbox semantics
- atomic transitions whose communication readiness is checked before execution
- boolean control flow with `if`
- tail-recursive control flow with `become`

CTL and diagrams use successor sets derived from `become` calls, and runtime execution still checks that an actor does not jump into an undeclared state.

## Current Features

- Lisp reader with s-expr support
- quote shorthand
- pure Lisp helpers:
  - `cons`
  - `car`
  - `cdr`
  - `def`
- logical operators:
  - `not`
  - `and`
  - `or`
  - `implies`/`->`
- CTL operators:
  - `ex`, `ax`
  - `ef`, `af`
  - `eg`, `ag`
  - `eu`, `au`
- basic atomic predicates:
  - `in-state`
  - `data=`
  - `mailbox-has`
- structured runtime event logging for:
  - transitions
  - sends
  - receives
- random guards:
  - `dice`
  - `dice-range`
  - `dice<`
  - `dice>=`
- mixed deterministic/probabilistic scenarios suitable for decision-process style modeling
- simple generated XY plots from event data
- Mermaid state and sequence diagrams in the docs
- low-level protocol-oriented built-ins:
  - `md5`
  - `rsa-raw`
  - `cryptorandom`
  - `sample-exponential`

## Repository Layout

- [main.go](/home/rfielding/code/go-ctl2/main.go): reader, runtime, CTL engine, event log, serialization
- [main_test.go](/home/rfielding/code/go-ctl2/main_test.go): executable semantics tests and examples
- [docs/ir.md](/home/rfielding/code/go-ctl2/docs/ir.md): design document
- [docs/mermaid](/home/rfielding/code/go-ctl2/docs/mermaid): Mermaid sources
- [docs/generated](/home/rfielding/code/go-ctl2/docs/generated): generated SVGs and plots
- [scripts](/home/rfielding/code/go-ctl2/scripts): documentation helper scripts
- [Makefile](/home/rfielding/code/go-ctl2/Makefile): build, docs, and serving commands

## Quick Start

Requirements:

- Go 1.22+
- `pandoc` for HTML docs
- Mermaid CLI (`mmdc`) for Mermaid SVG rendering

Run tests:

```bash
go test ./...
```

Build docs:

```bash
make docs
```

Serve the generated HTML locally:

```bash
make serve-docs
```

Then open:

```text
http://127.0.0.1:8000/ir.html
```

## Documentation

The main design document is:

- [docs/ir.md](/home/rfielding/code/go-ctl2/docs/ir.md)
- [docs/build/ir.html](/home/rfielding/code/go-ctl2/docs/build/ir.html)

Use the Markdown version on GitHub for repository browsing, and the built HTML version for the full rendered package with generated diagrams and plots.

It explains:

- the actor IR
- explicit successor semantics
- scheduling and communication rules
- CTL strategy
- Mermaid generation
- event-log-driven plotting

## Status

This is still a skeleton, not a finished verifier.

What is already solid:

- the core reader/runtime/tests are consistent
- CTL works over the current model
- diagrams and plots are generated into the docs
- the semantics are narrow enough to read closely

What is still incomplete:

- plots are still example-driven rather than generated from arbitrary models
- Mermaid generation is still doc-oriented rather than fully compiler-driven
- the proposition language is still small
- protocol examples are still early

## Why This Exists

The practical thesis of the project is:

- most people will not learn a large formal language just to check one protocol or design
- many people can still read diagrams and predicates
- an LLM can draft the model, but the human still needs a representation they can audit

This repository is trying to make that workflow plausible.
