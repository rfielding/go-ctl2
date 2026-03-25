# LLM Authoring Prompt

```text
Write a go-ctl2 model as Lisp.
Use exactly one top-level (model ...).
Declare reusable behavior with (actor RoleName ...).
Declare runtime actors explicitly with (instance Name Role (PeerRole Target...)...).
There is no implicit actor creation.
Every send target is written as a peer role in the actor definition and must resolve through the instance bindings.
Use (send Role msg) only when that role resolves to exactly one concrete actor.
Use (send-any Role msg) when a role may resolve to several concrete actors.
State is actor-local. The only cross-actor effect is messaging.
Each transition is (edge guard action...) inside a declared (state ...).
Every edge must eventually reach at least one (become State).
Use (recv var) to consume a message. recv also writes the sender name into local variable sender.
Use quoted literals for structured messages, for example '(message (type ping)).
Keep control flow explicit with named states and become transitions.
Put CTL requirements in (assert ...).
Use only the builtins and forms documented below.
```

# Language Reference

## Core Model Forms

| Form | Parameters | Operational Semantics |
| --- | --- | --- |
| `(model item...)` | `item := actor | instance | assert | xyplot` | Top-level container. No actors are created implicitly. |
| `(actor RoleName item...)` | `item := data | state` | Declares a reusable actor-role template. |
| `(data key value)` | `key` symbol, `value` literal/value form | Introduces actor-local data with an initial value. |
| `(state Name edge...)` | `Name` symbol | Declares a named control state. The first declared state is the initial control location. |
| `(edge guard action...)` | `guard` guard form | Declares one guarded atomic transition. At least one reachable `become` is required. |
| `(instance Name Role (PeerRole Target...)...)` | `Target...` concrete actor names | Creates one runtime actor and binds each referenced peer role to one or more concrete instances. |
| `(assert ctl-formula)` | CTL formula | Adds a branching-time requirement checked over the explored model. |
| `(xyplot name (title s) (steps n) (metric m))` | `metric := send-count | receive-count | sent-minus-received` | Requests a runtime-derived plot for the model example. |

## Guard Forms

| Form | Parameters | Operational Semantics |
| --- | --- | --- |
| `true` | none | Always enabled. |
| `(mailbox msg)` | `msg` message literal/value | True when the actor mailbox currently contains a matching message. |
| `(data= key value)` | `key` local variable, `value` literal/value form | True when the actor-local value equals the resolved right-hand side. |
| `(data> key value)` | numeric comparison | True when the actor-local numeric value is greater than the resolved right-hand side. |
| `(dice-range lo hi)` | floating-point bounds | True when the sampled `Dice` value satisfies `lo ≤ Dice < hi`. |
| `(dice< x)` | floating-point threshold | True when `Dice < x`. |
| `(dice>= x)` | floating-point threshold | True when `Dice ≥ x`. |
| `(dice)` | none | Resolves to the sampled floating-point value in `[0,1]`. |
| `(and g...)`, `(or g...)`, `(not g)`, `(implies p q)` | guard forms | Boolean composition over guard predicates. |

## Action Forms

| Form | Parameters | Operational Semantics |
| --- | --- | --- |
| `(send Role msg)` | `Role` peer role with exactly one bound target | Sends `msg` to the single bound instance. Compile-time error if the role resolves to multiple instances. |
| `(send-any Role msg)` | `Role` peer role with one or more bound targets | Sends to the first ready concrete target in that role set. |
| `(recv var)` | `var` local name | Consumes one incoming message into `var` and also writes the sending actor name into local `sender`. |
| `(become State)` | `State` declared control state | Moves the actor into the next control location. |
| `(set key value)` | local name and value form | Stores the resolved value into actor-local data. |
| `(add key delta)`, `(sub key delta)` | numeric local name and numeric value form | Applies integer arithmetic to actor-local data. |
| `(if guard then [else])` | guard and action blocks | Conditional action execution inside an atomic transition. |
| `(do action...)` | action list | Explicit sequencing when a nested action block is needed. |
| `(def name (p...) body)` | actor-local pure helper | Defines a value-level helper callable from `set`, `send`, and other value positions. |
| `(md5 out source)` | destination variable and value form | Computes the MD5 digest of the resolved value and stores its hex string. |
| `(rsa-raw out modulus exponent message)` | numeric value forms | Computes raw modular exponentiation `message^exponent mod modulus` and stores the numeric result. |
| `(cryptorandom out bytes)` | destination variable and byte count | Generates cryptographic randomness and stores a hex string. |
| `(sample-exponential out rate)` | destination variable and positive rate | Samples an exponential variate and stores the floating-point value. |

## Value Forms

| Form | Parameters | Operational Semantics |
| --- | --- | --- |
| symbols | local variable names | Resolve to actor-local data when present; otherwise remain symbols. |
| `'x`, `'(a b)` | quoted literal | Prevents evaluation and injects a literal symbol/list value. |
| `(cons a b)` | value forms | Prepends `a` onto list `b`. |
| `(car xs)` | list value form | Returns the first list element, or invalid/empty when absent. |
| `(cdr xs)` | list value form | Returns the tail of a list. |

## CTL Surface Forms

| Form | Parameters | Operational Semantics |
| --- | --- | --- |
| `(in-state A s)` | actor and state | Atomic predicate `A.state = s`. |
| `(data= A key value)` | actor, local name, value | Atomic predicate over actor-local data. |
| `(mailbox-has A msg)` | actor and message | Atomic predicate over queued messages. |
| `(ex p)`, `(ax p)` | CTL formula | Next-step possibility and necessity. |
| `(ef p)`, `(af p)` | CTL formula | Future possibility and inevitability. |
| `(eg p)`, `(ag p)` | CTL formula | Existential and universal invariance. |
| `(eu p q)`, `(au p q)` | CTL formulas | Existential and universal until. |
| `(not p)`, `(and p q)`, `(or p q)`, `(implies p q)` | CTL formulas | Boolean composition over CTL formulas. |

## Raw Modal μ-Calculus Forms

| Form | Parameters | Operational Semantics |
| --- | --- | --- |
| `true`, `false` | none | Boolean constants for the raw modal μ-calculus layer. |
| `(diamond p)`, `(box p)` | μ-calculus formula | Existential and universal next-step modalities. |
| `(mu X body)`, `(nu X body)` | fixpoint variable and body | Least and greatest fixpoints. |
| `(not p)`, `(and p q)`, `(or p q)` | μ-calculus formulas | Boolean composition over formulas. |
| `(in-state A s)`, `(data= A key value)`, `(mailbox-has A msg)` | same atoms as CTL | State predicates shared with the CTL surface syntax. |

