# go-ctl2

`go-ctl2` is effectively a Kripke philosophy calculator.

It uses a small Lisp-based IR to describe actor behavior, state machines, messages, and temporal requirements. The point is not just to generate models. The point is to argue with an LLM over requirements that can actually be checked.

![Dark branching history with p, q, r valuations](docs/static/readme_branching_history.svg)

Workflow:

1. the LLM writes a Lisp model
2. the compiler turns it into an explicit transition system
3. you inspect the states, channel contents, diagrams, and CTL claims
4. you reject or refine the model until the requirements are precise enough to check

CTL ranges over visible behavior only. You can assert over named control states and mailbox contents, for example `(in-state Server done)` or `(mailbox-has Client '(event (type ping) (from Relay) (to Client) (tstamp 7) (values (kind probe))))`. Internal actor variables still drive guards and actions, but they are not part of the CTL assertion language. When the public protocol matters, write it into structured messages.

Start with [docs/build/ir.generated.md](docs/build/ir.generated.md).
