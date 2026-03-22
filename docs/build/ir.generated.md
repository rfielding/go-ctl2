% Actor IR, CTL, and Diagram Strategy
% go-ctl2
% 2026-03-21

# Goal

This document records the current design intent for the actor IR, the declared control graph, the CTL checking model, and the documentation/diagram pipeline. The target user is someone who can inspect diagrams and predicates, but does not want to learn a large formal language before getting value.

The central idea is:

- an LLM emits actor definitions in Lisp
- the Lisp compiles into a small actor/message IR
- the same input drives:
  - runtime execution
  - CTL checking
  - Mermaid generation
  - SVG diagrams

The design goal is not to infer hidden control flow from simulation alone. Instead, transitions declare their possible next control states explicitly, and runtime execution validates that the declared next-state set is not wrong.

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

# Scheduling Semantics

The scheduler is single-threaded but concurrent in effect. At each step:

1. choose an actor
2. find the current control state
3. consider transitions in order
4. select the first transition that is fully ready
5. execute it atomically

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

- `not`, `and`, `or`
- `ex`, `ax`
- `ef`, `af`
- `eg`, `ag`
- `eu`, `au`

Atomic predicates currently include:

- `(in-state A done)`
- `(data= A key value)`
- `(mailbox-has A msg)`

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

These are intentionally low-level. They are not safe protocol APIs. They are protocol-modeling primitives.

Planned built-ins:

- SHA families
- keyed MAC constructions
- raw RSA decrypt/sign variants
- byte concatenation and parsing helpers
- comparison helpers for protocol checks

# Why This IR Is Sensible

The IR is sensible if the following remain true:

- one mailbox per actor
- explicit named control states
- explicit transition names
- explicit declared next states
- actor-local state is only mutated by the actor itself
- communication readiness gates transition selection
- CTL consumes the same declared control graph that diagrams do

This gives a coherent story for:

- execution
- proof
- diagram generation
- LLM-assisted authoring

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

- `(ef (in-state Server accepted))`
- `(af (data= Server received '(message (type ping))))`
- `(ag (¬ (mailbox-has Relay '(message (type ping)))))`

# Message Plot

Because transitions, sends, and receives are now logged as structured events, the same example can also drive simple XY plots. This is the beginning of the metrics side of the tool: message counts, throughput, queue growth, and related time-series views should come from the event log rather than from ad hoc parsing.

![Message Counts By Step](../generated/message_xyplot.svg)

```lisp
(ef (in-state Server accepted))
(af (data= Server received '(message (type ping))))
(ag (not (mailbox-has Relay '(message (type ping)))))
```

The first two are intended to hold for the example model.

The third is intentionally useful as a negative example: it should fail at the initial state because there is a reachable moment where `Relay` is holding a `ping` message before it forwards it.

The operational intent is:

- the client declares a message value whose `type` is `ping`
- intermediaries only accept messages of type `ping`
- if an intermediary does not have such a message available, it remains in its waiting loop until one arrives

# Mermaid Artifacts

The build expects Mermaid sources under `docs/mermaid/` and renders SVGs into `docs/generated/`.

Planned diagram set:

- actor control-state graph
- global communication overview
- sequence diagram for a concrete protocol trace

Example includes:

### A To B Sequence

![A To B Sequence](../generated/a_to_b_sequence.svg)

<details>
<summary>Mermaid Source: <code>a_to_b_sequence.mmd</code></summary>
<pre><code class="language-mermaid">
sequenceDiagram
    participant Client
    participant Relay
    participant Server
    Client--&gt;&gt;Relay: message(type=ping)
    Relay--&gt;&gt;Server: message(type=ping)
</code></pre>
</details>

### A To B State

![A To B State](../generated/a_to_b_state.svg)

<details>
<summary>Mermaid Source: <code>a_to_b_state.mmd</code></summary>
<pre><code class="language-mermaid">
flowchart TD
    subgraph Client
        direction TB
        C_start([start]) --&gt;|&quot;sent = ping&lt;br/&gt;become done&quot;| C_done([done])
    end

    subgraph Relay
        direction TB
        R_wait([wait]) --&gt;|&quot;received = ping&lt;br/&gt;forwarded = ping&lt;br/&gt;become forwarded&quot;| R_forwarded([forwarded])
        R_wait --&gt;|&quot;wrong type or no ping&lt;br/&gt;become wait&quot;| R_wait
    end

    subgraph Server
        direction TB
        S_idle([idle]) --&gt;|&quot;received = ping&lt;br/&gt;become accepted&quot;| S_accepted([accepted])
        S_idle --&gt;|&quot;wrong type or no ping&lt;br/&gt;become idle&quot;| S_idle
    end

    C_done -. send ping .-&gt; R_wait
    R_forwarded -. send ping .-&gt; S_idle
</code></pre>
</details>



# Build

The `Makefile` provides:

- `make test`
- `make docs`
- `make diagrams`
- `make clean`

Current assumptions:

- `pandoc` is installed for document generation
- `mmdc` is installed for Mermaid-to-SVG generation

The document and Mermaid build are intentionally kept separate so the same generated Mermaid source can be inspected directly.
