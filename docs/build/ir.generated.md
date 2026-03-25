% Actor IR, CTL, and Diagram Strategy
% go-ctl2
% 2026-03-22

# Goal

This document records the current design intent for the actor IR, the declared control graph, the CTL checking model, the metric/event pipeline, and the documentation build. The target user is someone who can inspect diagrams and predicates, but does not want to learn a large formal language before getting value.

The central idea is:

- an LLM emits actor-role definitions and explicit actor declarations in Lisp
- the Lisp compiles into a small actor/message IR
- the same input drives:
  - runtime execution
  - CTL checking
  - Mermaid generation
  - rendered Markdown diagrams
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

# Authoring Prompt And Language Reference

The exact information an LLM needs to author models is also the information a human reviewer needs. The documentation therefore includes the literal authoring prompt plus a generated reference for every core form, guard, action, value helper, and branching-time logic form:

# LLM Authoring Prompt

```text
Write a go-ctl2 model as Lisp.
Use exactly one top-level (model ...).
Declare reusable behavior with (actor RoleName ...).
Declare concrete runtime actors with (instance Name Role (queue n) (PeerRole Target...)...).
Every instance must declare its mailbox length with `(queue n)`. Use `0` for rendezvous and a positive integer for buffered mailboxes.
There is no implicit actor creation, implicit topology, or implicit next state.
Model the situation as a state machine, not as loose propositions.
For real-world scenarios such as wars, elections, outages, or negotiations: define actors for the participants, explicit states for phases, and messages or random branches for external events.
Only assert propositions that are grounded in named states, mailbox contents, or actor-local data.
Every send target is written as a peer role in the actor definition and must resolve through the instance bindings.
Use (send Role msg) only when that role resolves to exactly one concrete actor.
Use (send-any Role msg) when a role may resolve to several concrete actors.
State is actor-local. The only cross-actor effect is messaging.
Each transition is (edge guard action...) inside a declared (state ...).
Every edge must reach at least one (become State).
Use (recv var) to consume a message. recv also writes the sender name into local variable sender.
Use (print value) when you want a value to appear in the runtime trace.
Use (cons head tail), (car xs), and (cdr xs) for list structure instead of inventing custom list helpers.
Use quoted literals for structured messages, for example '(message (type strike) (target refinery)).
Prefer short named states such as idle, mobilizing, negotiating, retaliating, ceasefire, failed.
Put branching-time requirements in (assert ...).
If you return Lisp in chat, it may be executed locally. Return a full `(model ...)` when proposing a model, or return one CTL / raw modal mu-calculus formula to evaluate against a referenced prior model such as `A12` or against the current Model tab by default.
Use only the forms documented below.
```

# Language Reference

Documentation metavariables start with `$` and include a type tag, for example `$count:int` or `$msg:value`. Actual models do not include the `$`; write `count` and `msg` in the Lisp itself.

## Core Model Forms

| Form | Metavariables | Operational Semantics |
| --- | --- | --- |
| `(model $item:form...)` | `$item := actor | instance | assert | xyplot` | Top-level container. Nothing is created implicitly. |
| `(actor $role:symbol $item:form...)` | `$item := data | state` | Declares a reusable actor-role template. |
| `(data $key:symbol $value:value)` | `$key` actor-local name | Initializes actor-local data before execution starts. |
| `(state $name:symbol $edge:form...)` | `$name` control-state name | Declares one named control location. The first state is initial. |
| `(edge $guard:guard $action:action...)` | `$guard` readiness condition | One atomic transition. Every branch must reach a `become`. |
| `(instance $name:symbol $role:symbol (queue $n:int) ($peerRole:symbol $target:symbol...)...)` | `$n` mailbox capacity; `0` means rendezvous | Creates one runtime actor, sets its mailbox length, and fills its peer-role bindings. |
| `(assert $p:ctl)` | `$p` CTL formula | Checks a branching-time requirement over the explored model. |
| `(xyplot $name:symbol (title $title:string) (steps $n:int) (metric $m:symbol))` | `$m := sent-minus-received | send-rate | receive-rate` | Requests a runtime-derived line chart. Use rate-style metrics for monotone event counts. |

## Guard Forms

| Form | Metavariables | Operational Semantics |
| --- | --- | --- |
| `true` | none | Always enabled. |
| `dice` | none | Shorthand for a 50/50 branch when no bounds are needed. |
| `(mailbox $msg:value)` | `$msg` literal or quoted message | True when the mailbox contains a matching message. |
| `(data= $key:symbol $value:value)` | `$key` actor-local variable | True when the local value equals the resolved right-hand side. |
| `(data> $key:symbol $value:int)` | `$value` integer threshold | True when the local integer is strictly greater. |
| `(dice-range $low:float $high:float)` | `0.0 ≤ $low ≤ $high ≤ 1.0` | True when `Dice ∈ [$low,$high]`. |
| `(dice< $high:float)` | `$high` upper bound | True when `Dice < $high`. |
| `(dice>= $low:float)` | `$low` lower bound | True when `Dice ≥ $low`. |
| `(and $g:guard...)`, `(or $g:guard...)`, `(not $g:guard)`, `(implies $p:guard $q:guard)` | guard composition | Boolean structure over guards. |

## Action Forms

| Form | Metavariables | Operational Semantics |
| --- | --- | --- |
| `(send $role:symbol $msg:value)` | `$role` must bind to exactly one target | Sends one message to the bound concrete actor. |
| `(send-any $role:symbol $msg:value)` | `$role` may bind to several targets | Sends to the first ready target in that peer-role set. |
| `(recv $var:symbol)` | `$var` local variable name | Consumes one incoming message and also stores the sender in `sender`. |
| `(become $state:symbol)` | `$state` declared control state | Sets the next control location. |
| `(set $key:symbol $value:value)` | `$key` actor-local variable | Writes a resolved value into actor-local data. |
| `(print $value:value)` | resolved value form | Appends the resolved value to the runtime trace for inspection and debugging. |
| `(add $key:symbol $delta:int)`, `(sub $key:symbol $delta:int)` | `$key` integer variable | Integer arithmetic on actor-local data. |
| `(if $guard:guard $then:action [$else:action])` | guard plus action branches | Conditional execution inside one atomic transition. |
| `(do $action:action...)` | explicit action sequence | Groups nested actions when one form is required. |
| `(def $name:symbol ($param:symbol...) $body:value)` | actor-local pure helper | Defines a value helper used from value positions. `send`, `recv`, `become`, and other action forms are forbidden inside the body. |
| `(md5 $out:symbol $source:value)` | `$out` destination variable | Stores an MD5 hex digest. |
| `(rsa-raw $out:symbol $modulus:int $exponent:int $message:int)` | big-integer inputs | Stores `message^exponent mod modulus`. |
| `(cryptorandom $out:symbol $bytes:int)` | `$bytes ≥ 0` | Stores a random hex string. |
| `(sample-exponential $out:symbol $rate:float)` | `$rate > 0` | Stores an exponential variate sample. |

## Value Forms

| Form | Metavariables | Operational Semantics |
| --- | --- | --- |
| bare symbol | `$var:symbol` in docs; actual model omits `$` | Resolves to actor-local data when present; otherwise stays a symbol literal. |
| `'$x`, `'(a b)` | quoted literal | Prevents evaluation and injects a literal symbol or list. |
| `(cons $head:value $tail:value)` | value forms | Prepends `$head` onto list `$tail`. |
| `(car $xs:value)` | `$xs` list value | Returns the first list element, or empty/invalid when absent. |
| `(cdr $xs:value)` | `$xs` list value | Returns the tail of a list. |

## Branching-Time Logic Forms

### CTL Surface Forms

| Form | Metavariables | Operational Semantics |
| --- | --- | --- |
| `(in-state $actor:symbol $state:symbol)` | atomic state predicate | Asserts `$actor.state = $state`. |
| `(data= $actor:symbol $key:symbol $value:value)` | atomic data predicate | Asserts one actor-local value equals `$value`. |
| `(mailbox-has $actor:symbol $msg:value)` | atomic mailbox predicate | Asserts the mailbox currently contains `$msg`. |
| `(ex $p:ctl)`, `(ax $p:ctl)` | next-step modalities | `EX` and `AX`. |
| `(ef $p:ctl)`, `(af $p:ctl)` | eventual modalities | `EF` and `AF`. |
| `(eg $p:ctl)`, `(ag $p:ctl)` | global modalities | `EG` and `AG`. |
| `(eu $p:ctl $q:ctl)`, `(au $p:ctl $q:ctl)` | until modalities | `E[p U q]` and `A[p U q]`. |
| `(not $p:ctl)`, `(and $p:ctl $q:ctl)`, `(or $p:ctl $q:ctl)`, `(implies $p:ctl $q:ctl)` | Boolean CTL structure | Boolean composition over CTL formulas. |

### Raw Modal μ-Calculus Forms

| Form | Metavariables | Operational Semantics |
| --- | --- | --- |
| `true`, `false` | none | Boolean constants for the raw modal μ-calculus layer. |
| `(diamond $p:mu)`, `(box $p:mu)` | next-step modalities | Existential and universal modal operators. |
| `(mu $X:symbol $body:mu)`, `(nu $X:symbol $body:mu)` | fixpoint variable plus body | Least and greatest fixpoints. |
| `(not $p:mu)`, `(and $p:mu $q:mu)`, `(or $p:mu $q:mu)` | Boolean mu structure | Boolean composition over formulas. |
| `(in-state $actor:symbol $state:symbol)`, `(data= $actor:symbol $key:symbol $value:value)`, `(mailbox-has $actor:symbol $msg:value)` | same atoms as CTL | State predicates shared with the CTL surface syntax. |



# Terminology

The document uses the following terms consistently:

- actor
  a runtime instance bound to a name and one actor-role definition
- actor role
  a reusable behavior template declared by `(actor RoleName ...)`
- peer role fill
  an instance-level mapping from a referenced role to one or more concrete instances that play it
- state
  a named control location declared directly inside an actor role
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

An actor role has:

- one mailbox
- one current named control state
- local data
- a set of named states

Each runtime actor is created explicitly with `(instance ActorName RoleName (queue N) (PeerRole InstanceName...)...)`.
`N` is the mailbox length for that concrete actor.
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
- `send-any` is ready if any filled target mailbox has space

## Zero-Capacity Channels

A mailbox capacity of `0` means synchronous rendezvous semantics.

- `send` is ready only if the receiver has a matching ready `recv`
- `send-any` is ready only if at least one filled receiver has a matching ready `recv`
- `recv` is ready only if a sender is ready to rendezvous

The runtime currently models this by checking receiver readiness before a zero-capacity send is allowed to execute.

On a successful `recv`, the payload is stored in the declared variable and the local variable `sender` is also set to the sending actor name. That gives the receiver a built-in return address.

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
(model
  (actor ClientRole
    (state loop
      (edge (dice-range 0.0 0.5)
        (set last "sleep")
        (become loop))
      (edge (dice-range 0.5 1.0)
        (send QueueRole req)
        (set last "arrival")
        (become loop))))

  (actor QueueRole
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

  (instance Client ClientRole (queue 1) (QueueRole Queue))
  (instance Queue QueueRole (queue 5))

  (xyplot outstanding
    (title "Outstanding Messages By Step")
    (steps 100)
    (metric sent-minus-received)))
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
- the `xyplot` declaration says which runtime-derived chart should be rendered for this model

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

```mermaid
flowchart TD
    subgraph Client
        direction TB
        C_loop([loop]) -->|"dice&lt;0.5<br/>last = sleep"| C_loop
        C_loop -->|"dice&gt;=0.5<br/>send req<br/>last = arrival"| C_loop
    end

    subgraph Queue
        direction TB
        Q_wait([wait]) -->|"req and count = 0<br/>count += 1<br/>elapsed = 0"| Q_wait
        Q_wait -->|"req and 0 &lt; count &lt; 5<br/>count += 1"| Q_wait
        Q_wait -->|"req and count = 5<br/>dropped_count += 1"| Q_wait
        Q_wait -->|"count &gt; 0 and dice&lt;0.5<br/>count -= 1<br/>last_departure = service-complete"| Q_wait
        Q_wait -->|"count &gt; 0 and dice&gt;=0.5<br/>last_departure = busy"| Q_wait
    end

    C_loop -. arrival req .-> Q_wait
```

```mermaid
sequenceDiagram
    participant Client
    participant Queue
    Note over Client: dice branch
    alt arrival
        Client-->>Queue: req
        alt count < 5
            Queue->>Queue: count += 1
        else count = 5
            Queue->>Queue: dropped_count += 1
        end
    else sleep
        Client->>Client: last = sleep
    end
    Note over Queue: service branch when count > 0
    alt service-complete
        Queue->>Queue: count -= 1
        Queue->>Queue: last_departure = service-complete
    else busy
        Queue->>Queue: last_departure = busy
    end
```

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
  (become start__wait))

(state start__wait
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

# Branching-Time Logic

CTL needs an exact successor relation. In this design, the relation comes from visible `become` targets, then execution validates that runtime steps stay inside that declared control graph.

## One State, Many Futures

The explored model is a transition system `(S, →)`. We write `s ⊨ φ` when runtime state `s ∈ S` satisfies formula `φ`.

The key point is branching: one state can have several possible successors because of scheduler choice, random guards, or mailbox readiness.

![CTL branching tree](../static/ctl_tree.svg)

`E` quantifies over some branch. `A` quantifies over all branches.

- `EX p`: there exists an immediate successor `t` with `s → t` and `t ⊨ p`
- `AX p`: for every immediate successor `t`, if `s → t` then `t ⊨ p`
- `EF p`: there exists a path on which `p` eventually holds
- `AF p`: on every path, `p` eventually holds
- `EG p`: there exists a path on which `p` holds forever
- `AG p`: on every path, `p` holds forever
- `E[p U q]`: there exists a path where `p` holds until `q` holds
- `A[p U q]`: on every path, `p` holds until `q` holds

That is the practical reading users need:

- `EF`: possible reachability
- `AF`: guaranteed reachability
- `EG`: possible persistence
- `AG`: universal invariant

Examples:

- `(ef (in-state Negotiator ceasefire))`
- `(af (in-state CivilianSupply stabilized))`
- `(ag (not (mailbox-has EarlyWarning false-alarm)))`
- `(eg (data= Frontline status mobilizing))`

## CTL And μ-Calculus

The implementation lowers CTL into the modal μ-calculus:

- `EX p = ◇p`
- `AX p = □p`
- `EF p = μX.(p ∨ ◇X)`
- `AF p = μX.(p ∨ □X)`
- `EG p = νX.(p ∧ ◇X)`
- `AG p = νX.(p ∧ □X)`
- `E[p U q] = μX.(q ∨ (p ∧ ◇X))`
- `A[p U q] = μX.(q ∨ (p ∧ □X))`

The fixpoint intuition is standard:

- `μ` is least fixpoint: build upward from the empty set
- `ν` is greatest fixpoint: prune downward from the full set

Small examples:

```lisp
(mu X
  (or (in-state Server accepted)
      (diamond X)))
```

This is `EF (in-state Server accepted)`.

```lisp
(nu X
  (and (not (mailbox-has Relay ping))
       (box X)))
```

This is `AG (not (mailbox-has Relay ping))`.

## What The Checker Proves

The checker explores the reachable runtime graph, adds an explicit self-loop on deadlock states, and evaluates each formula as a set of satisfying states.

The result is exact for the explored finite model. It is not a theorem about all imaginable implementations. If the model is wrong, the proof is wrong. The value is that the state machine, message topology, diagrams, and temporal formulas are all inspectable in the same artifact.

# Event Log And Metrics

Transitions, sends, and receives are recorded as structured runtime events. This is the base layer for line graphs and performance-style metrics.

At the current stage, the runtime can already derive:

- cumulative event counts
- filtered counts, for example “sends only to Server”
- simple rate series over scheduler steps

This is the beginning of the metrics side of the tool. The goal is that message rates, queue growth, latency, throughput, and retry behavior should come from the event log rather than from ad hoc parsing of trace strings.

# Built-ins

The canonical builtin inventory now lives in the generated authoring reference near the top of this document so the human-facing documentation and the LLM-facing prompt share one source of truth.

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
4. the generated Markdown embeds Mermaid directly
5. GitHub or the local HTML renderer turns that Mermaid into diagrams

This allows:

- state machine diagrams without simulation
- sequence diagrams without simulation
- UML-like class diagrams showing actor-local control states and data
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
  optional local Mermaid sources for preview diagrams
- `docs/generated/`
  ignored local build intermediates
- `scripts/`
  helper scripts used by the documentation pipeline

The tests are not secondary. They are the clearest executable specification currently in the repository.

# Worked Examples

The canonical examples are generated as exact input/output pairs: the literal Lisp source first, then the literal Markdown emitted from that same source. That keeps the diagrams, CTL outcomes, and plot references adjacent to the example instead of scattering them across later sections.

## Message Chain Example

### Input Lisp

```lisp
(model
		(actor ClientRole
			(state start
				(edge true
					(send RelayRole '(message (type ping)))
					(become done)))
			(state done))

		(actor RelayRole
			(state relay
				(edge true
					(recv msg)
					(send ServerRole msg)
					(become done)))
			(state done))

		(actor ServerRole
			(state idle
				(edge true
					(recv received)
					(become done)))
			(state done))

		(instance Client ClientRole (queue 1) (RelayRole Relay))
		(instance Relay RelayRole (queue 1) (ServerRole Server))
		(instance Server ServerRole (queue 1))

		(assert (ef (data= Server received '(message (type ping)))))
		(assert (af (data= Server received '(message (type ping)))))

		(xyplot message_outstanding
			(title "Message Chain Backlog By Step")
			(steps 4)
			(metric sent-minus-received))
		(xyplot message_sends
			(title "Message Chain Send Rate")
			(steps 4)
			(metric send-rate))
		(xyplot message_receives
			(title "Message Chain Receive Rate")
			(steps 4)
			(metric receive-rate)))
```

### Rendered Output

#### State Diagram

```mermaid
flowchart TD
    subgraph Client
        direction TB
        Clientstart([start]) -->|"(send Relay (quote (message (type ping))))"| Clientdone([done])
    end
    subgraph Relay
        direction TB
        Relayrelay([relay]) -->|"(recv msg)"| Relayrelay__wait([relay__wait])
        Relayrelay__wait([relay__wait]) -->|"(send Server msg)"| Relaydone([done])
    end
    subgraph Server
        direction TB
        Serveridle([idle]) -->|"(recv received)"| Serverdone([done])
    end
```

#### Message Diagram

```mermaid
sequenceDiagram
    participant Client
    participant Relay
    participant Server
    Client-->>Relay: (quote (message (type ping)))
    Relay-->>Server: msg
```

#### Class Diagram

```mermaid
classDiagram
    class Client {
        +start : state
        +done : state
        +state : data
    }
    <<ClientRole>> Client
    class Relay {
        +relay : state
        +relay__wait : state
        +done : state
        +msg : data
        +sender : data
        +state : data
    }
    <<RelayRole>> Relay
    class Server {
        +idle : state
        +done : state
        +received : data
        +sender : data
        +state : data
    }
    <<ServerRole>> Server
```

#### CTL Outcomes

- `PASS` `(ef (data= Server received (quote (message (type ping)))))`
- `PASS` `(af (data= Server received (quote (message (type ping)))))`

#### Line Graphs

##### Message Chain Backlog By Step

```lisp
(xyplot message_outstanding (title "Message Chain Backlog By Step") (steps 4) (metric sent-minus-received))
```

```mermaid
xychart-beta
    title "Message Chain Backlog By Step"
    x-axis "applied runtime step" [0, 1, 2, 3, 4]
    y-axis "sent - received" 0 --> 1
    line [0, 1, 0, 1, 0]
```

##### Message Chain Send Rate

```lisp
(xyplot message_sends (title "Message Chain Send Rate") (steps 4) (metric send-rate))
```

```mermaid
xychart-beta
    title "Message Chain Send Rate"
    x-axis "applied runtime step" [0, 1, 2, 3, 4]
    y-axis "sends per step" 0 --> 1
    line [0, 1, 0, 1, 0]
```

##### Message Chain Receive Rate

```lisp
(xyplot message_receives (title "Message Chain Receive Rate") (steps 4) (metric receive-rate))
```

```mermaid
xychart-beta
    title "Message Chain Receive Rate"
    x-axis "applied runtime step" [0, 1, 2, 3, 4]
    y-axis "receives per step" 0 --> 1
    line [0, 0, 1, 0, 1]
```

#### Channel Sizes

##### Client Channel Size

```mermaid
xychart-beta
    title "Client Channel Size"
    x-axis "applied runtime step" [0, 1, 2, 3, 4]
    y-axis "queued messages" 0 --> 1
    line [0, 0, 0, 0, 0]
```

##### Relay Channel Size

```mermaid
xychart-beta
    title "Relay Channel Size"
    x-axis "applied runtime step" [0, 1, 2, 3, 4]
    y-axis "queued messages" 0 --> 1
    line [0, 1, 0, 0, 0]
```

##### Server Channel Size

```mermaid
xychart-beta
    title "Server Channel Size"
    x-axis "applied runtime step" [0, 1, 2, 3, 4]
    y-axis "queued messages" 0 --> 1
    line [0, 0, 0, 1, 0]
```

## Queue Example

### Input Lisp

```lisp
(model
		(actor ClientRole
			(state loop
				(edge (dice-range 0.0 0.5)
					(set last "sleep")
					(become loop))
				(edge (dice-range 0.5 1.0)
					(send QueueRole req)
					(set last "arrival")
					(become loop))))

		(actor QueueRole
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

		(instance Client ClientRole (queue 1) (QueueRole Queue))
		(instance Queue QueueRole (queue 5))

		(xyplot queue_outstanding
			(title "Queue Backlog By Step")
			(steps 100)
			(metric sent-minus-received)))
```

### Rendered Output

#### State Diagram

```mermaid
flowchart TD
    subgraph Client
        direction TB
        Clientloop([loop]) -->|"(dice-range 0.0 0.5)<br/>(set last &#34;sleep&#34;)"| Clientloop([loop])
        Clientloop([loop]) -->|"(dice-range 0.5 1.0)<br/>(send Queue req)<br/>(set last &#34;arrival&#34;)"| Clientloop([loop])
    end
    subgraph Queue
        direction TB
        Queuewait([wait]) -->|"(and (mailbox req) (data= count 0))<br/>(recv msg)<br/>(add count 1)<br/>(set elapsed 0)"| Queuewait([wait])
        Queuewait([wait]) -->|"(and (mailbox req) (data&gt; count 0) (not (data= count 5)))<br/>(recv msg)<br/>(add count 1)"| Queuewait([wait])
        Queuewait([wait]) -->|"(and (mailbox req) (data= count 5))<br/>(recv dropped)<br/>(add dropped_count 1)"| Queuewait([wait])
        Queuewait([wait]) -->|"(and (data&gt; count 0) (dice-range 0.0 0.5))<br/>(sub count 1)<br/>(set last_departure &#34;service-complete&#34;)"| Queuewait([wait])
        Queuewait([wait]) -->|"(and (data&gt; count 0) (dice-range 0.5 1.0))<br/>(set last_departure &#34;busy&#34;)"| Queuewait([wait])
    end
```

#### Message Diagram

```mermaid
sequenceDiagram
    participant Client
    participant Queue
    Client-->>Queue: req
```

#### Class Diagram

```mermaid
classDiagram
    class Client {
        +loop : state
        +last : data
        +state : data
    }
    <<ClientRole>> Client
    class Queue {
        +wait : state
        +count : data
        +dropped : data
        +dropped_count : data
        +elapsed : data
        +last_departure : data
        +msg : data
        +sender : data
        +state : data
    }
    <<QueueRole>> Queue
```

#### Line Graphs

##### Queue Backlog By Step

```lisp
(xyplot queue_outstanding (title "Queue Backlog By Step") (steps 100) (metric sent-minus-received))
```

```mermaid
xychart-beta
    title "Queue Backlog By Step"
    x-axis "applied runtime step" [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63, 64, 65, 66, 67, 68, 69, 70, 71, 72, 73, 74, 75, 76, 77, 78, 79, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89, 90, 91, 92, 93, 94, 95, 96, 97, 98, 99, 100]
    y-axis "sent - received" 0 --> 5
    line [0, 1, 1, 2, 2, 2, 2, 2, 2, 3, 4, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5]
```

#### Channel Sizes

##### Client Channel Size

```mermaid
xychart-beta
    title "Client Channel Size"
    x-axis "applied runtime step" [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11]
    y-axis "queued messages" 0 --> 1
    line [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]
```

##### Queue Channel Size

```mermaid
xychart-beta
    title "Queue Channel Size"
    x-axis "applied runtime step" [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11]
    y-axis "queued messages" 0 --> 5
    line [0, 1, 1, 2, 2, 2, 2, 2, 2, 3, 4, 5]
```

## Bakery Role-Reuse Example

### Input Lisp

```lisp
(model
		(actor ProductionRole
			(data baked 0)
			(state start
				(edge true
					(send-any TruckRole batch)
					(add baked 1)
					(become done)))
			(state done))

		(actor TruckRole
			(data deliveries 0)
			(state wait
				(edge true
					(recv cargo)
					(add deliveries 1)
					(send StoreRole cargo)
					(become done)))
			(state done))

		(actor StoreRole
			(data inventory 0)
			(data sold 0)
			(state idle
				(edge true
					(recv shipment)
					(add inventory 1)
					(become stocked)))
			(state stocked
				(edge true
					(send CustomerBaseRole sale)
					(sub inventory 1)
					(add sold 1)
					(become stocked))))

		(actor CustomerBaseRole
			(data served 0)
			(state ready
				(edge true
					(recv sale)
					(add served 1)
					(become ready))))

		(instance Production ProductionRole (queue 1) (TruckRole TruckNorth TruckSouth))
		(instance TruckNorth TruckRole (queue 1) (StoreRole StoreA))
		(instance TruckSouth TruckRole (queue 1) (StoreRole StoreB))
		(instance StoreA StoreRole (queue 1) (CustomerBaseRole CustomerA))
		(instance StoreB StoreRole (queue 1) (CustomerBaseRole CustomerB))
		(instance StoreC StoreRole (queue 1) (CustomerBaseRole CustomerC))
		(instance CustomerA CustomerBaseRole (queue 1))
		(instance CustomerB CustomerBaseRole (queue 1))
		(instance CustomerC CustomerBaseRole (queue 1)))
```

### Rendered Output

#### State Diagram

```mermaid
flowchart TD
    subgraph Production
        direction TB
        Productionstart([start]) -->|"(send-any TruckNorth TruckSouth batch)<br/>(add baked 1)"| Productiondone([done])
    end
    subgraph TruckNorth
        direction TB
        TruckNorthwait([wait]) -->|"(recv cargo)<br/>(add deliveries 1)"| TruckNorthwait__wait([wait__wait])
        TruckNorthwait__wait([wait__wait]) -->|"(send StoreA cargo)"| TruckNorthdone([done])
    end
    subgraph TruckSouth
        direction TB
        TruckSouthwait([wait]) -->|"(recv cargo)<br/>(add deliveries 1)"| TruckSouthwait__wait([wait__wait])
        TruckSouthwait__wait([wait__wait]) -->|"(send StoreB cargo)"| TruckSouthdone([done])
    end
    subgraph StoreA
        direction TB
        StoreAidle([idle]) -->|"(recv shipment)<br/>(add inventory 1)"| StoreAstocked([stocked])
        StoreAstocked([stocked]) -->|"(send CustomerA sale)<br/>(sub inventory 1)<br/>(add sold 1)"| StoreAstocked([stocked])
    end
    subgraph StoreB
        direction TB
        StoreBidle([idle]) -->|"(recv shipment)<br/>(add inventory 1)"| StoreBstocked([stocked])
        StoreBstocked([stocked]) -->|"(send CustomerB sale)<br/>(sub inventory 1)<br/>(add sold 1)"| StoreBstocked([stocked])
    end
    subgraph StoreC
        direction TB
        StoreCidle([idle]) -->|"(recv shipment)<br/>(add inventory 1)"| StoreCstocked([stocked])
        StoreCstocked([stocked]) -->|"(send CustomerC sale)<br/>(sub inventory 1)<br/>(add sold 1)"| StoreCstocked([stocked])
    end
    subgraph CustomerA
        direction TB
        CustomerAready([ready]) -->|"(recv sale)<br/>(add served 1)"| CustomerAready([ready])
    end
    subgraph CustomerB
        direction TB
        CustomerBready([ready]) -->|"(recv sale)<br/>(add served 1)"| CustomerBready([ready])
    end
    subgraph CustomerC
        direction TB
        CustomerCready([ready]) -->|"(recv sale)<br/>(add served 1)"| CustomerCready([ready])
    end
```

#### Message Diagram

```mermaid
sequenceDiagram
    participant Production
    participant TruckNorth
    participant TruckSouth
    participant StoreA
    participant StoreB
    participant StoreC
    participant CustomerA
    participant CustomerB
    participant CustomerC
    Production-->>TruckNorth: batch
    Production-->>TruckSouth: batch
    TruckNorth-->>StoreA: cargo
    TruckSouth-->>StoreB: cargo
    StoreA-->>CustomerA: sale
    StoreB-->>CustomerB: sale
    StoreC-->>CustomerC: sale
```

#### Class Diagram

```mermaid
classDiagram
    class Production {
        +start : state
        +done : state
        +baked : data
        +state : data
    }
    <<ProductionRole>> Production
    class TruckNorth {
        +wait : state
        +wait__wait : state
        +done : state
        +cargo : data
        +deliveries : data
        +sender : data
        +state : data
    }
    <<TruckRole>> TruckNorth
    class TruckSouth {
        +wait : state
        +wait__wait : state
        +done : state
        +cargo : data
        +deliveries : data
        +sender : data
        +state : data
    }
    <<TruckRole>> TruckSouth
    class StoreA {
        +idle : state
        +stocked : state
        +inventory : data
        +sender : data
        +shipment : data
        +sold : data
        +state : data
    }
    <<StoreRole>> StoreA
    class StoreB {
        +idle : state
        +stocked : state
        +inventory : data
        +sender : data
        +shipment : data
        +sold : data
        +state : data
    }
    <<StoreRole>> StoreB
    class StoreC {
        +idle : state
        +stocked : state
        +inventory : data
        +sender : data
        +shipment : data
        +sold : data
        +state : data
    }
    <<StoreRole>> StoreC
    class CustomerA {
        +ready : state
        +sale : data
        +sender : data
        +served : data
        +state : data
    }
    <<CustomerBaseRole>> CustomerA
    class CustomerB {
        +ready : state
        +sale : data
        +sender : data
        +served : data
        +state : data
    }
    <<CustomerBaseRole>> CustomerB
    class CustomerC {
        +ready : state
        +sale : data
        +sender : data
        +served : data
        +state : data
    }
    <<CustomerBaseRole>> CustomerC
```

#### Channel Sizes

##### Production Channel Size

```mermaid
xychart-beta
    title "Production Channel Size"
    x-axis "applied runtime step" [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32]
    y-axis "queued messages" 0 --> 1
    line [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]
```

##### TruckNorth Channel Size

```mermaid
xychart-beta
    title "TruckNorth Channel Size"
    x-axis "applied runtime step" [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32]
    y-axis "queued messages" 0 --> 1
    line [0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]
```

##### TruckSouth Channel Size

```mermaid
xychart-beta
    title "TruckSouth Channel Size"
    x-axis "applied runtime step" [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32]
    y-axis "queued messages" 0 --> 1
    line [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]
```

##### StoreA Channel Size

```mermaid
xychart-beta
    title "StoreA Channel Size"
    x-axis "applied runtime step" [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32]
    y-axis "queued messages" 0 --> 1
    line [0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]
```

##### StoreB Channel Size

```mermaid
xychart-beta
    title "StoreB Channel Size"
    x-axis "applied runtime step" [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32]
    y-axis "queued messages" 0 --> 1
    line [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]
```

##### StoreC Channel Size

```mermaid
xychart-beta
    title "StoreC Channel Size"
    x-axis "applied runtime step" [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32]
    y-axis "queued messages" 0 --> 1
    line [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]
```

##### CustomerA Channel Size

```mermaid
xychart-beta
    title "CustomerA Channel Size"
    x-axis "applied runtime step" [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32]
    y-axis "queued messages" 0 --> 1
    line [0, 0, 0, 0, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0]
```

##### CustomerB Channel Size

```mermaid
xychart-beta
    title "CustomerB Channel Size"
    x-axis "applied runtime step" [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32]
    y-axis "queued messages" 0 --> 1
    line [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]
```

##### CustomerC Channel Size

```mermaid
xychart-beta
    title "CustomerC Channel Size"
    x-axis "applied runtime step" [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32]
    y-axis "queued messages" 0 --> 1
    line [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]
```


# Message Plot

Because transitions, sends, and receives are now logged as structured events, the docs can render plots from an actual Runtime execution instead of from hand-written points.

One such plot is declared in the model itself with:

```lisp
(xyplot queue_outstanding
  (title "Queue Backlog By Step")
  (steps 100)
  (metric sent-minus-received))
```

The line charts below are rendered from every `xyplot` declaration in the example models. Monotone counters are shown as rates, not just cumulative totals. Backlog-style charts use `sent-minus-received`. The generated example sections also include one channel-size plot per actor so mailbox occupancy is visible directly.

### Message Outstanding

```mermaid
xychart-beta
    title "Message Chain Backlog By Step"
    x-axis "applied runtime step" [0, 1, 2, 3, 4]
    y-axis "sent - received" 0 --> 1
    line [0, 1, 0, 1, 0]
```

<details>
<summary>XY Plot Source: <code>message_outstanding</code></summary>
<pre><code class="language-lisp">
(xyplot message_outstanding
  (title "Message Chain Backlog By Step")
  (steps 4)
  (metric sent-minus-received))
</code></pre>
</details>

### Message Receives

```mermaid
xychart-beta
    title "Message Chain Receive Rate"
    x-axis "applied runtime step" [0, 1, 2, 3, 4]
    y-axis "receives per step" 0 --> 1
    line [0, 0, 1, 0, 1]
```

<details>
<summary>XY Plot Source: <code>message_receives</code></summary>
<pre><code class="language-lisp">
(xyplot message_receives
  (title "Message Chain Receive Rate")
  (steps 4)
  (metric receive-rate))
</code></pre>
</details>

### Message Sends

```mermaid
xychart-beta
    title "Message Chain Send Rate"
    x-axis "applied runtime step" [0, 1, 2, 3, 4]
    y-axis "sends per step" 0 --> 1
    line [0, 1, 0, 1, 0]
```

<details>
<summary>XY Plot Source: <code>message_sends</code></summary>
<pre><code class="language-lisp">
(xyplot message_sends
  (title "Message Chain Send Rate")
  (steps 4)
  (metric send-rate))
</code></pre>
</details>

### Queue Outstanding

```mermaid
xychart-beta
    title "Queue Backlog By Step"
    x-axis "applied runtime step" [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63, 64, 65, 66, 67, 68, 69, 70, 71, 72, 73, 74, 75, 76, 77, 78, 79, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89, 90, 91, 92, 93, 94, 95, 96, 97, 98, 99, 100]
    y-axis "sent - received" 0 --> 1
    line [0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 1, 0, 1, 0, 1, 0, 0, 0, 0, 0, 1, 0, 1, 0, 1, 0, 1, 0, 0, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 1, 0, 1, 0, 1, 0, 0, 0, 1, 0, 1, 0, 1, 0, 1, 0]
```

<details>
<summary>XY Plot Source: <code>queue_outstanding</code></summary>
<pre><code class="language-lisp">
(xyplot queue_outstanding
  (title "Queue Backlog By Step")
  (steps 100)
  (metric sent-minus-received))
</code></pre>
</details>



Natural follow-on plots include:

- send rate
- receive rate
- moving-average throughput
- queue length by actor
- service latency between matching send and receive events
- timeout and retry counters

# Mermaid Artifacts

The committed Markdown keeps Mermaid inline so GitHub can render the diagrams directly from the source document.

The important constraint is that the diagrams are not decorative extras. They are another view over the same declared control structure, and the examples above keep those diagrams next to the Lisp that generated them.

# Build

The `Makefile` provides:

- `make test`
- `make docs`
- `make diagrams`
- `make serve-docs`
- `make clean`

Current assumptions:

- `pandoc` is installed for document generation
- `mmdc` is optional and only needed for `make diagrams`

The document and Mermaid build are intentionally kept separate so the same generated Mermaid source can be inspected directly.

# Serving The HTML

After running `make docs`, the generated document lives at:

- `docs/build/ir.html`

For local review, the repository also provides a simple static server target:

- `make serve-docs`

That serves `docs/build/` over a local HTTP server so the generated HTML can be reviewed together in a browser.

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
