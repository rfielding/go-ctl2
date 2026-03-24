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

The design goal is not to infer hidden control flow from simulation alone. Instead, transitions expose control flow through explicit `become` calls, and the compiler walks those action trees to recover possible successor control states.

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
  a guarded atomic step whose successor states are derived from `become`
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
- an atomic action block

Example:

```lisp
(edge true
  (recv msg)
  (become got-ping))
```

The compiler walks the action block, collects reachable `become` targets, and uses that successor set for graph construction and CTL. Runtime execution validates that the post-step control state is one of those derived successor states.

An `edge` must contain at least one `become`. Omitting `become` is a compile error; the language does not use implicit self-loops.

The compiler also walks the action block for communication operations.
If `send` or `recv` appear later in the body, the compiler inserts internal wait substates such as `wait__0`, `wait__1`, and rewrites the edge into a chain of explicit control states where each communication step appears at the front of its compiled substate.
That keeps the user-facing source compact while still giving the runtime and the proof layer a clean explicit control graph.

## Why Derived Next States Matter

The central compromise in this repository is that transitions still expose successor control states structurally through `become`, rather than leaving control flow hidden in simulation traces.

That buys several things immediately:

- CTL has a clear successor relation
- control-state diagrams can be rendered without simulation
- sequence and communication diagrams can be generated from the same declared model
- runtime execution can detect mismatches when an actual step lands outside the derived set

It does not remove the need for correct modeling. A wrong `become` structure can still make the proof layer wrong. The point is that the control-flow obligation remains visible and reviewable.

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
(edge (dice-range 0.0 0.5 "route to branch a")
  (become a))

(edge (dice-range 0.5 1.0 "route to branch b")
  (become b))
```

This is enough to express:

- purely random branching
- Markov-chain style behavior
- mixed control + random behavior where some decisions are scheduled and others are probabilistic

Full M/M/1/5-style example:

```lisp
(actor Client
  (state loop
    (edge (dice-range 0.0 0.5)
      (set last "sleep")
      (become loop))
    (edge (dice-range 0.5 1.0)
      (send Queue req)
      (set last "arrival")
      (become loop))))

(actor Queue
  (state wait
    (edge (and (mailbox req) (data= count 0))
      (recv msg)
      (add count 1)
      (set elapsed 0)
      (become wait))
    (edge (and (mailbox req) (data> count 0) (not (data= count 5)))
      (recv msg)
      (add count 1)
      (become wait))
    (edge (and (mailbox req) (data= count 5))
      (recv dropped)
      (add dropped_count 1)
      (become wait))
    (edge (and (data> count 0) (dice-range 0.0 0.5))
      (sub count 1)
      (set last_departure "service-complete")
      (become wait))
    (edge (and (data> count 0) (dice-range 0.5 1.0))
      (set last_departure "busy")
      (become wait))))
```

Interpretation:

- `Client` models arrivals
  `dice-range` makes the sleep/arrival split explicit
- `Queue` models a single-server queue with capacity `5`
  `count` is the current system size and `dropped_count` records blocked arrivals
- departures are the random side
  when `count > 0`, `dice-range` decides whether service completes on that step
- arrivals are client-driven and probabilistic
  scheduler choice still decides when the client gets to act
- the `count = 5` branch is the finite-capacity part
  arrivals are consumed and recorded as drops instead of increasing the queue
- the self-loops are written explicitly with `become`
  the example does not rely on implicit stay-in-place behavior

This is not a continuous-time simulator. It is a small executable control model that captures the same queueing shape:

- probabilistic client arrivals
- one server
- finite capacity `5`
- blocked arrivals counted as losses
- random service completions

That is usually enough for inspectable CTL properties such as:

- eventually the queue becomes non-empty
- the queue can reach saturation
- some executions accumulate drops
- if arrivals stop, the system can drain

Representative predicates for this queue model:

- `(ef (data> Queue count 0) "eventually the queue can become non-empty")`
- `(ef (data= Queue count 5) "the finite-capacity queue can saturate")`
- `(ef (data> Queue dropped_count 0) "some execution can observe blocked arrivals")`
- `(ag (implies (data= Queue count 0) (not (data> Queue dropped_count 0))) "drops only occur after the system has filled at some earlier point")`

The Mermaid artifacts below are a useful companion view for this example:

- a queue state rendition showing explicit self-loops
- a queue message/service rendition showing arrival and service-completion flows

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

The compiler normalizes communication-heavy bodies into explicit wait substates.

At the source level, the user can write local work and communication in one edge body.
At the compiled level, the actor is split so that each `send` or `recv` sits at the front of a generated substate transition, with explicit `become` hops connecting the pieces.

Conceptually, something like this:

```lisp
(edge true
  (set before 1)
  (send B ping)
  (set after 1)
  (become done))
```

becomes an internal shape more like:

```lisp
(edge true
  (set before 1)
  (become start__wait_0))

(state start__wait_0
  (edge true
    (send B ping)
    (set after 1)
    (become done)))
```

That removes a lot of source-language noise while preserving explicit compiled control flow.

## What Counts As A State Change

This repository treats a transition firing as a state change even when the actor remains in the same named control state. In other words:

- changing a local variable is a state change
- consuming or sending a message is a state change
- staying in the same named state after the step does not mean “nothing happened”

The graph used by execution and model checking is therefore over full runtime states, while the derived control graph remains the human-facing skeleton.

# Control Flow

The intended structured control tools are:

- tail recursion through `become`
- boolean conditionals through `if`
- loops represented by explicit control states and self-recursive `become`

This is closer to a small structured machine IR than to an arbitrary scripting language.

# CTL Strategy

CTL needs an exact successor relation. In this design, the successor relation is derived from `become` calls in each transition body.

Runtime execution exists to validate that:

- transitions only land in derived successor states
- control state updates are not inconsistent with the model

This means CTL does not need to wait for simulation to discover the control graph.

## What CTL Means

CTL is Computation Tree Logic.

The important idea is that execution does not produce one future. It produces a tree of possible futures:

- scheduler choice can produce different next steps
- random guards can produce different next steps
- mailbox contents can enable or disable different edges

CTL lets us ask whether a property holds on some future branch or on all future branches.

There are two dimensions in every temporal CTL operator:

- path quantifier:
  `E` means "there exists a path"
  `A` means "for all paths"
- time modality:
  `X` means "in the next step"
  `F` means "eventually in the future"
  `G` means "globally, always in the future"
  `U` means "until"

That is the core connection:

- `E` corresponds to existential choice
  some reachable branch works
- `A` corresponds to universal choice
  every reachable branch must work

In ordinary language:

- `EF p`
  there exists some execution path on which `p` eventually becomes true
  "possibly, at some point, `p` happens"
- `AF p`
  on every execution path, `p` eventually becomes true
  "necessarily, sooner or later, `p` happens"
- `EG p`
  there exists some execution path on which `p` remains true forever
  "possibly, the system can stay in a `p`-good region forever"
- `AG p`
  on every execution path, `p` remains true forever
  "necessarily, `p` is always preserved"
- `EX p`
  there exists an immediate successor state where `p` holds
  "possibly on the next step"
- `AX p`
  for every immediate successor state, `p` holds
  "necessarily on the next step"
- `E[p U q]`
  there exists a path where `p` keeps holding until `q` eventually holds
- `A[p U q]`
  on every path, `p` keeps holding until `q` eventually holds

This is why the pairs matter:

- `EF` vs `AF`
  possible eventuality vs guaranteed eventuality
- `EG` vs `AG`
  possible invariant along some branch vs invariant along all branches
- `EX` vs `AX`
  one-step possibility vs one-step necessity
- `EU` vs `AU`
  possible progress condition vs guaranteed progress condition

Examples:

- `(ef (in-state Server accepted))`
  there is some way for the server to reach `accepted`
- `(af (in-state Server accepted))`
  every possible future must eventually reach `accepted`
- `(ag (not (mailbox-has Relay ping)))`
  along all futures, the relay mailbox is always free of `ping`
- `(eg (data> Queue count 0))`
  there exists some path where the queue can stay non-empty forever

`EF` is often the right operator for reachability or possibility.
`AF` is stronger: it means no matter how scheduling and randomness resolve, the desired state is unavoidable.
That difference is exactly why users need the quantifier explanation up front.

## Mu-Calculus Tutorial

The modal mu-calculus is the lower-level fixpoint logic underneath this checker.
CTL is now treated as a readable special case of that more general logic.

At first glance, the implementation of `mu` and `nu` can look almost suspiciously small.
That is because the power comes from the combination of just three ideas:

- boolean structure
- modal next-step structure
- fixpoints over a finite successor graph

The modal operators are:

- `diamond p`
  there exists a successor state where `p` holds
  this is the same branching idea as CTL's existential next-step operator
- `box p`
  for all successor states, `p` holds
  this is the universal next-step version

The fixpoint operators are:

- `mu X body`
  least fixpoint
  think "the smallest set of states that satisfies this recursive equation"
- `nu X body`
  greatest fixpoint
  think "the largest set of states that satisfies this recursive equation"

Intuition:

- `mu` is usually how you express eventuality, progress, or finite escape
- `nu` is usually how you express invariance, persistence, or the ability to remain inside a region forever

That is the core mental model:

- least fixpoint = build upward from nothing
- greatest fixpoint = prune downward from everything

### Small Examples

Reachability:

```lisp
(mu X
  (or (in-state Server accepted)
      (diamond X)))
```

Read it as:

- a state satisfies this formula if
  it is already an `accepted` state
  or it can move in one step to another state that satisfies the same formula

That is exactly "there exists a path that eventually reaches `accepted`".
So this is the mu-calculus form of `EF`.

Invariant preservation:

```lisp
(nu X
  (and (not (mailbox-has Relay ping))
       (box X)))
```

Read it as:

- a state satisfies this formula if
  it is safe now
  and all of its successors also satisfy the same invariant

That is exactly "on all paths, always safe".
So this is the mu-calculus form of `AG`.

Possible persistence:

```lisp
(nu X
  (and (data> Queue count 0)
       (diamond X)))
```

Read it as:

- the queue is non-empty now
- and there exists some next step that keeps you inside the same region

That captures the idea that there is some execution branch along which the queue can remain non-empty forever.
That is the mu-calculus form of `EG`.

### Why The Algorithm Is So Small

The evaluator looks small because it is doing repeated set refinement on a finite graph.

For `mu`:

- start with the empty set
- evaluate the body assuming `X` means that current set
- keep adding states until nothing changes

For `nu`:

- start with the set of all states
- evaluate the body assuming `X` means that current set
- keep removing states until nothing changes

That is enough to express a large amount of temporal reasoning because recursive temporal properties are exactly what fixpoints are good at.

For example:

- "eventually reach a good state"
  means repeated predecessor expansion until the set stabilizes
- "always remain safe"
  means repeated pruning of states that have bad successors until the set stabilizes
- "stay inside this region forever on some branch"
  means repeatedly removing states that cannot remain inside the region

### CTL As A Special Case

The reason this is such a good foundation is that standard CTL operators translate directly into mu-calculus:

- `EX p`
  becomes `(diamond p)`
- `AX p`
  becomes `(box p)`
- `EF p`
  becomes `(mu X (or p (diamond X)))`
- `AF p`
  becomes `(mu X (or p (box X)))`
- `EG p`
  becomes `(nu X (and p (diamond X)))`
- `AG p`
  becomes `(nu X (and p (box X)))`
- `E[p U q]`
  becomes `(mu X (or q (and p (diamond X))))`
- `A[p U q]`
  becomes `(mu X (or q (and p (box X))))`

So CTL is not being replaced here.
It becomes the user-facing fragment with the friendlier names, while the semantic engine underneath is the more general fixpoint logic.

### Why This Feels Like LTL Territory

It is reasonable to look at these formulas and think:
"these are the kinds of things I usually use LTL for".

That instinct is right in a practical sense.
Many properties people ask for in system design sound like:

- eventually something good happens
- always something bad is prevented
- once a request arrives, eventually a response appears
- some loop can continue forever

Those are the same kinds of temporal intuitions that lead people to LTL.

The important distinction is semantic:

- LTL talks about a single path at a time
- CTL and the mu-calculus talk directly about branching futures

In this repository, branching matters a lot:

- scheduler choice creates alternative next steps
- random guards create alternative next steps
- mailbox contents create alternative next steps

So a branching-time logic is a better fit than plain linear-time reasoning.

The nice surprise is that the mu-calculus is expressive enough to capture many of the "eventually", "always", and "until" properties people informally think of as LTL-style requirements, while still speaking directly about the branching graph your runtime actually explores.

### Practical Reading Guide

When reading a raw mu-calculus formula:

1. identify the atomic predicate
   what is the local fact you care about?
2. identify the modal direction
   `diamond` means "some next branch", `box` means "every next branch"
3. identify the fixpoint
   `mu` usually means eventuality or progress
   `nu` usually means invariance or persistence
4. read the recursion as "keep applying this condition until it stabilizes"

That is all the checker is doing in code.
It is simple enough to implement compactly, but general enough to cover a large part of practical temporal reasoning.

## Present Operators

The implementation currently supports:

- `not`, `and`, `or`, `implies`
- `ex`, `ax`
- `ef`, `af`
- `eg`, `ag`
- `eu`, `au`

Raw mu-calculus operators currently include:

- `true`, `false`
- `not`, `and`, `or`
- `diamond`, `box`
- `mu`, `nu`
- atomic predicates such as `in-state`, `data=`, and `mailbox-has`

Atomic predicates currently include:

- `(in-state A done "actor A is in done")`
- `(data= A key value "actor A has the expected value")`
- `(mailbox-has A msg "actor A currently holds the message")`

Predicate forms may carry a final string argument used only as human-readable documentation. The evaluator ignores that trailing string semantically.

## How The Checker Works

The algorithm in this repository is a standard finite-state CTL model-checking pattern over the explored runtime graph.

Step 1: build the reachable graph.

- start from the initial runtime
- execute one enabled actor step at a time
- clone successor runtimes
- deduplicate states by a serialized runtime key
- record the successor relation between runtime states

If a state has no enabled successors, the implementation records a self-loop labeled `deadlock`.
That makes deadlock states explicit in the graph and keeps the temporal operators total.

Step 2: evaluate formulas by computing the set of satisfying states.

For each subformula, the checker computes:

- the set of explored states where the subformula is true

Atomic predicates are evaluated directly on each runtime state.
Boolean operators are evaluated by ordinary set operations:

- `not`
  complement
- `and`
  intersection
- `or`
  union
- `implies`
  `not left or right`

The temporal operators are computed from the successor graph:

- `EX p`
  mark any state with at least one successor already marked by `p`
- `AX p`
  mark any state whose successors are all marked by `p`
- `EF p`
  reduce to `E[true U p]`
- `AF p`
  reduce to `A[true U p]`
- `AG p`
  reduce to `not EF not p`
- `EG p`
  compute a greatest fixpoint:
  start from states satisfying `p`, then repeatedly remove any state that cannot stay inside that set on at least one successor
- `EU(p, q)`
  compute a least fixpoint:
  start from states satisfying `q`, then repeatedly add states satisfying `p` that can move to an already accepted state
- `AU(p, q)`
  compute a least fixpoint:
  start from states satisfying `q`, then repeatedly add states satisfying `p` whose successors are all already accepted

So the checker is not proving arbitrary mathematics.
It is doing graph analysis on the explored transition system induced by the actor model.

That is exactly why this works well here:

- the graph is explicit
- the control flow is explicit
- the user can inspect both the model and the formulas
- the result can be explained in ordinary "possibly/necessarily, eventually/always" language

## What CTL Is Proving Here

At the current stage, CTL is proving properties over the repository’s induced model, not solving theorem-proving problems over arbitrary infinite mathematics.

That means:

- if the reachable state space is finite and fully explored, the model-checking result is exact for that model
- if the model is bounded or abstracted, the result applies to that abstraction
- if the derived `become` successor relation is wrong, the result is only as good as that model

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

Pure value-level helpers:

- `(cons a b)`
  - builds a simple list by prepending `a` to `b`
- `(car xs)`
  - returns the first element of a list
- `(cdr xs)`
  - returns the tail of a list
- `(def name (params...) body)`
  - defines an actor-local pure helper function whose body can be used inside value positions such as `set` and `send`

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
- successor states derived from `become`
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

The document example should not be a single actor in isolation. The intended workflow is to describe a set of interacting actors, generate diagrams from that same input, and check temporal requirements over the same derived control graph.

This small message-chain example is intentionally simple, but it already exercises:

- multiple actors
- message passing
- explicit control states
- successor states derived from `become`
- CTL predicates over the resulting model

```lisp
(actor Client
  (state start
    (edge true
      (send Relay '(message (type ping)))
      (become done)))
  (state done))

(actor Relay
  (state relay
    (edge true
      (recv msg)
      (send Server msg)
      (become relay))))

(actor Server
  (state idle
    (edge true
      (recv received)
      (become accepted)))
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
- the relay forwards that value unchanged to the server
- if a `recv` has no message available, that edge is simply not ready

## Walkthrough Of The Example

The example should be read as three small control machines composed by message passing.

`Client`

- starts in `start`
- sends one `ping`-typed message to `Relay`
- becomes `done`

`Relay`

- has one explicit source state, `relay`
- when a message is available, it consumes it, forwards it to `Server`, and becomes `relay` again
- the compiler inserts an internal wait substate so the compiled control graph still begins each communication step with `recv` or `send`

`Server`

- waits in `idle`
- when a message is available, it consumes it, records it, and becomes `accepted`

This example is deliberately small, but it is enough to show:

- explicit control states
- successor extraction from `become`
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
