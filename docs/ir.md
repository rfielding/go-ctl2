% Actor IR, CTL, and Diagram Strategy
% go-ctl2
% 2026-03-22

# Goal

This document records the current design intent for the actor IR, the declared control graph, the CTL checking model, the metric/event pipeline, and the documentation build. The target user is someone who can inspect diagrams and predicates, but does not want to learn a large formal language before getting value.

The central idea is:

- an LLM emits actor definitions in Lisp
- the Lisp compiles into a small actor/message IR
- the same input drives:
  - runtime execution
  - CTL checking
  - Mermaid generation
  - SVG diagrams
  - event logs and plots

The design goal is not to infer hidden control flow from simulation alone. Instead, transitions declare their possible next control states explicitly, and runtime execution validates that the declared next-state set is not wrong.

# Audience

This repository is for readers who want the benefits of temporal logic and executable models without first committing to a large bespoke specification language.

The intended workflow is:

1. describe a requirement in actor/message terms
2. let an LLM draft the Lisp model
3. inspect the generated control states, transitions, predicates, and diagrams
4. run execution, exploration, and CTL checks on the same artifact

The important constraint is inspectability. The user should be able to reject a bad model by reading the states, guards, transitions, predicates, and generated diagrams.

# Design Principles

- explicit control states instead of implicit graph inference
- actor-local ownership of mutable state
- communication as a readiness condition, not as hidden side effect
- one semantic source feeding execution, proof, plots, and diagrams
- a small enough core language that the generated output is reviewable line by line

# Terminology

The document uses the following terms consistently:

- actor
  an independently scheduled state-owning component with one mailbox
- state
  a named control location inside an actor
- transition
  a guarded atomic step with an explicit `(next ...)` control contract
- mailbox
  the actor-local queue or rendezvous endpoint through which messages arrive
- runtime
  the current collection of actors, local data, mailboxes, and scheduler context
- explored model
  the graph artifact produced by running the runtime semantics over reachable states
- CTL formula
  a temporal requirement evaluated over the induced transition system

# Core Model

## Actor

An actor has:

- one mailbox
- one current named control state
- local data
- a set of named states

Each actor owns its own state. Messages do not mutate the actor directly; they accumulate in the mailbox until the actor reaches a receive-ready transition.

## State

A state is a named control location. For compiled models, the control location is explicit, not inferred from guard overlap.

A state contains transitions. A transition is selectable only if:

- the actor is currently in the state
- the transition guard holds
- all communication in the atomic block is ready

## Transition

A transition contains:

- a guard
- an explicit `(next ...)` declaration
- an atomic action block

Example:

```lisp
(on (mailbox ping)
  (next relay)
  (recv ping)
  (send C ping))
```

The `(next ...)` declaration is the control-flow contract used for graph construction and CTL. Runtime execution validates that the post-step control state is one of the declared next states.

## Why Explicit Next States Matter

The central compromise in this repository is that transitions declare the set of possible successor control states directly.

That buys several things immediately:

- CTL has a clear successor relation
- control-state diagrams can be rendered without simulation
- sequence and communication diagrams can be generated from the same declared model
- runtime execution can detect mismatches when an actual step lands outside the declared set

It does not remove the need for correct modeling. A wrong `(next ...)` declaration can still make the proof layer wrong. The point is that the correctness obligation becomes visible and reviewable.

# Scheduling Semantics

The scheduler is single-threaded but concurrent in effect. At each step:

1. choose an actor
2. roll a floating-point `Dice` value in `[0,1]`
3. find the current control state
4. consider transitions in order
5. select the first transition that is fully ready
6. execute it atomically

If the chosen actor has no ready transition, it yields. This is not deadlock. Deadlock means no actor in the whole runtime has any ready transition.

# Communication Semantics

## Buffered Channels

Each actor mailbox can be treated as a bounded or unbounded queue.

- `recv` is ready if a matching message is present
- `send` is ready if the target mailbox has space

## Zero-Capacity Channels

A mailbox capacity of `0` means synchronous rendezvous semantics.

- `send` is ready only if the receiver has a matching ready `recv`
- `recv` is ready only if a sender is ready to rendezvous

The runtime currently models this by checking receiver readiness before a zero-capacity send is allowed to execute.

## Random Guards

Before attempting a step, the runtime samples a floating-point value called `Dice` in `[0,1]`.

That value can be used in guards to express random branching, for example:

```lisp
(on (dice-range 0.0 0.5 "route to branch a")
  (next a)
  (become a))

(on (dice-range 0.5 1.0 "route to branch b")
  (next b)
  (become b))
```

This is enough to express:

- purely random branching
- Markov-chain style behavior
- mixed control + random behavior where some decisions are scheduled and others are probabilistic

## Decision Processes

When the only source of branching is `Dice`, the operational picture is close to a Markov-chain style model.

When both of these are present:

- scheduler choice over which actor gets the next turn
- `Dice`-driven branching inside actor guards

the operational picture is closer to a decision process: some choices are external or controlled, and some are probabilistic.

That is the important mixed case for systems such as:

- clients competing for service while service outcomes are random
- retry logic with random backoff
- deterministic protocol logic interacting with lossy or probabilistic environments

The current unit tests include both:

- pure random branching through `dice-range`
- a mixed deterministic/probabilistic scenario where a client deterministically sends a request and the server randomly accepts or rejects after receipt

# Atomic Blocks

Communication is part of transition readiness. There is no partial transition semantics.

That means:

- if a `recv` is not ready, the transition is not enabled
- if a `send` is not ready, the transition is not enabled
- no local updates before blocked communication are committed

This makes an atomic transition a real scheduler unit.

The intended normalized form is:

- boolean guard
- optional communication readiness
- local atomic work
- `become` / next-state update

Because communication readiness is checked before execution, a `send` may appear textually in the middle of an action block without creating partial updates.

## What Counts As A State Change

This repository treats a transition firing as a state change even when the actor remains in the same named control state. In other words:

- changing a local variable is a state change
- consuming or sending a message is a state change
- staying in the same named state after the step does not mean “nothing happened”

The graph used by execution and model checking is therefore over full runtime states, while the declared control graph remains the human-facing skeleton.

# Control Flow

The intended structured control tools are:

- tail recursion through `become`
- boolean conditionals through `if`
- loops represented by explicit control states and self-recursive `become`

This is closer to a small structured machine IR than to an arbitrary scripting language.

# CTL Strategy

CTL needs an exact successor relation. In this design, the declared `(next ...)` relation is the control successor relation used by the proof layer.

Runtime execution exists to validate that:

- transitions only land in declared next states
- control state updates are not inconsistent with the model

This means CTL does not need to wait for simulation to discover the control graph.

## Present Operators

The implementation currently supports:

- `¬`, `∧`, `∨`, `→`
- `not`, `and`, `or`, `implies`
- `ex`, `ax`
- `ef`, `af`
- `eg`, `ag`
- `eu`, `au`

Atomic predicates currently include:

- `(in-state A done "actor A is in done")`
- `(data= A key value "actor A has the expected value")`
- `(mailbox-has A msg "actor A currently holds the message")`

Predicate forms may carry a final string argument used only as human-readable documentation. The evaluator ignores that trailing string semantically.

## What CTL Is Proving Here

At the current stage, CTL is proving properties over the repository’s induced model, not solving theorem-proving problems over arbitrary infinite mathematics.

That means:

- if the reachable state space is finite and fully explored, the model-checking result is exact for that model
- if the model is bounded or abstracted, the result applies to that abstraction
- if the declared `(next ...)` relation is wrong, the result is only as good as that declaration

This is still useful. The tool is intended to make control flow, messaging, and temporal requirements inspectable enough that a human can review the generated model rather than trusting a hidden formalization.

# Event Log And Metrics

Transitions, sends, and receives are recorded as structured runtime events. This is the base layer for line graphs and performance-style metrics.

At the current stage, the runtime can already derive:

- cumulative event counts
- filtered counts, for example “sends only to Server”
- simple rate series over scheduler steps

This is the beginning of the metrics side of the tool. The goal is that message rates, queue growth, latency, throughput, and retry behavior should come from the event log rather than from ad hoc parsing of trace strings.

# Built-ins

The language needs protocol-oriented built-ins so examples like authentication and key exchange do not require embedding large arithmetic sublanguages.

Current built-ins:

- `(md5 out source)`
  - computes an MD5 digest of the resolved source value
  - stores a hex string in `out`

- `(rsa-raw out modulus exponent message)`
  - performs raw modular exponentiation
  - stores the numeric result in `out`

- `(cryptorandom out bytes)`
  - generates cryptographic randomness
  - stores a hex string in `out`

- `(sample-exponential out rate)`
  - samples from an exponential distribution with the given positive rate
  - stores the sampled floating-point value in `out`

These are intentionally low-level. They are not safe protocol APIs. They are protocol-modeling primitives.

Planned built-ins:

- SHA families
- keyed MAC constructions
- raw RSA decrypt/sign variants
- byte concatenation and parsing helpers
- comparison helpers for protocol checks

## Protocol Modeling Direction

The current crypto built-ins are intentionally raw. The purpose of the built-ins is not to provide production-safe cryptographic APIs. The purpose is to let protocol examples express byte-level and arithmetic-level steps without bloating the core language.

The long-term direction is to support examples such as:

- challenge-response protocols
- request/acknowledgement handshakes
- key transport and key confirmation flows
- message authentication checks
- timeout and retry logic

# Why This IR Is Sensible

The IR is sensible if the following remain true:

- one mailbox per actor
- explicit named control states
- explicit transition names
- explicit declared next states
- actor-local state is only mutated by the actor itself
- communication readiness gates transition selection
- CTL consumes the same declared control graph that diagrams do
- event plots are derived from the same runtime semantics

This gives a coherent story for:

- execution
- proof
- metric plots
- diagram generation
- LLM-assisted authoring

## Relation To Other Formalisms

This project is not trying to replicate TLA+, CSP, PRISM, or Erlang exactly.

Instead, it borrows selected strengths:

- from actor systems:
  mailbox ownership and explicit message passing
- from Erlang:
  receive-driven control and guarded mailbox inspection
- from ASM thinking:
  explicit state updates and executable semantic steps
- from FSM/CFG thinking:
  named control locations and declared successor structure
- from model checking:
  CTL over a precise transition relation

The value is in the combination: a readable actor/message IR that an LLM can draft and a human can still audit.

# Reverse Mermaid Direction

The intended future workflow is:

1. LLM emits actor Lisp
2. the tool serializes the runtime/model as Lisp
3. a single Mermaid generator reads that Lisp
4. the same generated Mermaid source is rendered into SVG
5. the document embeds those SVGs

This allows:

- state machine diagrams without simulation
- sequence diagrams without simulation
- the same input feeding both proof and presentation

# Repository Layout

The current repository is intentionally small. The most important files are:

- `main.go`
  the reader, runtime, CTL implementation, event log, and serialization code
- `main_test.go`
  executable examples that pin down the semantics
- `docs/ir.md`
  this document
- `docs/mermaid/`
  Mermaid sources rendered into SVG for publication
- `docs/generated/`
  generated diagrams and plots
- `scripts/`
  helper scripts used by the documentation pipeline

The tests are not secondary. They are the clearest executable specification currently in the repository.

# Example Model

The document example should not be a single actor in isolation. The intended workflow is to describe a set of interacting actors, generate diagrams from that same input, and check temporal requirements over the same declared control graph.

This small message-chain example is intentionally simple, but it already exercises:

- multiple actors
- message passing
- explicit control states
- declared next states
- CTL predicates over the resulting model

```lisp
(actor Client
  (state start
    (on true
      (next done)
      (send Relay '(message (type ping)))
      (become done)))
  (state done))

(actor Relay
  (state wait
    (on (mailbox '(message (type ping)))
      (next forwarded)
      (recv '(message (type ping)))
      (send Server '(message (type ping)))
      (become forwarded))
    (on true
      (next wait)
      (become wait)))
  (state forwarded))

(actor Server
  (state idle
    (on (mailbox '(message (type ping)))
      (next accepted)
      (recv '(message (type ping)))
      (set received '(message (type ping)))
      (become accepted))
    (on true
      (next idle)
      (become idle)))
  (state accepted))
```

Representative CTL requirements:

- `(ef (in-state Server accepted "possibly server accepts"))`
- `(af (data= Server received '(message (type ping)) "eventually server records the ping message"))`
- `(ag (not (mailbox-has Relay '(message (type ping)) "relay still holds ping")) "always relay mailbox is empty of ping")`

The first two are intended to hold for the example model.

The third is intentionally useful as a negative example: it should fail at the initial state because there is a reachable moment where `Relay` is holding a `ping` message before it forwards it.

The operational intent is:

- the client declares a message value whose `type` is `ping`
- intermediaries only accept messages of type `ping`
- if an intermediary does not have such a message available, it remains in its waiting loop until one arrives

## Walkthrough Of The Example

The example should be read as three small control machines composed by message passing.

`Client`

- starts in `start`
- sends one `ping`-typed message to `Relay`
- becomes `done`

`Relay`

- waits in `wait`
- if a `ping`-typed message is present, it consumes it, forwards it, and becomes `forwarded`
- otherwise it stays in its waiting loop

`Server`

- waits in `idle`
- if a `ping`-typed message is present, it consumes it, records it, and becomes `accepted`
- otherwise it stays in `idle`

This example is deliberately small, but it is enough to show:

- explicit control states
- explicit next-state declarations
- queued communication
- CTL requirements over the same model
- generated state and sequence diagrams
- generated metric plots from runtime events

# Message Plot

Because transitions, sends, and receives are now logged as structured events, the same example can also drive simple XY plots.

![Message Counts By Step](../generated/message_xyplot.svg)

The present plot is intentionally simple. It is showing the kind of visualization the structured event log can support, not claiming to be the final metrics UI.

Natural follow-on plots include:

- sends per step
- receives per step
- moving-average throughput
- queue length by actor
- service latency between matching send and receive events
- timeout and retry counters

# Mermaid Artifacts

The build expects Mermaid sources under `docs/mermaid/` and renders SVGs into `docs/generated/`.

Example includes:

{{DIAGRAM_SECTIONS}}

The important constraint is that the diagrams are not decorative extras. They are another view over the same declared control structure.

# Build

The `Makefile` provides:

- `make test`
- `make docs`
- `make diagrams`
- `make serve-docs`
- `make clean`

Current assumptions:

- `pandoc` is installed for document generation
- `mmdc` is installed for Mermaid-to-SVG generation

The document and Mermaid build are intentionally kept separate so the same generated Mermaid source can be inspected directly.

# Serving The HTML

After running `make docs`, the generated document lives at:

- `docs/build/ir.html`

For local review, the repository also provides a simple static server target:

- `make serve-docs`

That serves `docs/build/` over a local HTTP server so the generated HTML, SVG diagrams, and plots can be reviewed together in a browser.

# Current Limits

This repository is still a skeleton. Important things are intentionally incomplete:

- the example plots are generated from a fixed example, not yet from arbitrary models
- the Mermaid generation is still mostly document-oriented rather than language-integrated
- CTL formulas are present, but there is not yet a full proposition language over every part of runtime state
- the documentation explains the intended semantics more completely than the current implementation exposes through tooling

That is acceptable for this stage. The repository is already good enough to show the core thesis:

- actor/message models can be generated in a compact Lisp
- the same artifact can feed execution, diagrams, CTL checks, and simple metric plots
- the result is inspectable enough to review rather than merely trust
