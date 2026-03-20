package main

import (
	"fmt"
	"math/rand"
)

/*
Requirements store a set of abstract machines and predicates against them.
A set of communicating Abstract State Machines is the basis for representing
a requirement. It allows for a turing complete machine.

Technically, it is single-threaded. But, as we Tick, we move between actors,
giving them a chance to execute. If they get a valid step, then they execute
a step to mutate the actor's data. Note that this is NOT a finite state machine,
but an abstract state machine.

That means that we only have a guard to FIND the state, and a guard to
pick a transition. The transition modifies actor data; the only way to
get into another state. Note that each state can be where multiple substates
live.

When we have branchless code blocks with no send or recv, we can do
as much reading and writing as we need. This lets us call out to functions
such as ciphers and hashes. We can have loops in our blocks, but they will
be uninterruptible unless the loop iteration is in its own block.
*/
type Requirement struct {
	Actors     []Actor
	Predicates []Predicate
}

func (r *Requirement) Tick() {
	// pick an actor at random
	n := rand.Int() % len(r.Actors)
	a := r.Actors[n]
	// execute an actor step
	s := a.Step()
	if s != nil {
		s.Apply()
	}
}

type Predicate struct {
}

/*
Messages are the ony way to edit the internals of an actor
*/
type Message struct {
	Type string
	Data map[string]interface{}
}

/* a circular queue of fixed capacity */
type Channel struct {
	Messages []Message
	Capacity int
	Head     int
	Tail     int
	Length   int
}

func NewChannel(capacity int) *Channel {
	return &Channel{
		Messages: make([]Message, capacity),
		Capacity: capacity,
		Head:     0,
		Tail:     0,
		Length:   0,
	}
}
func (c *Channel) IsFull() bool {
	return c.Length == c.Capacity
}
func (c *Channel) IsEmpty() bool {
	return c.Length == 0
}
func (c *Channel) Write(msg Message) error {
	if c.IsFull() {
		return fmt.Errorf("channel is full")
	}
	c.Messages[c.Tail] = msg
	c.Tail = (c.Tail + 1) % c.Capacity
	c.Length++
	return nil
}
func (c *Channel) Read() (Message, error) {
	if c.IsEmpty() {
		return Message{}, fmt.Errorf("channel is empty")
	}
	msg := c.Messages[c.Head]
	c.Head = (c.Head + 1) % c.Capacity
	c.Length--
	return msg, nil
}

/*
Be careful! this is not a finite state machine state.
It is an abstract state machine state.
That means that we only have a guard to FIND the statre
and transitions are applied to it to mutate the actor.
Otherwise, we would be stuck in the state forever
*/
type State struct {
}

type Step struct {
	// preidiicate to look up  this Steo
	Guard Predicate
	// if this step is sending
	Send *Channel
	// if this step is recving
	Recv *Channel
}

func (s *Step) Apply() {
}

type Actor struct {
	// an incoming message can mutate our state

	Incoming Channel
	// we mutate THESE to go from state to state.
	Data map[string]interface{}
}

func (a *Actor) Step() *Step {
	// match a predicate node that is ready.
	// ie: matches predicate, not writing full, or reading empty
	return &Step{}
}

func NewActor(channelCapacity int) *Actor {
	return &Actor{
		Incoming: *NewChannel(channelCapacity),
	}
}

func main() {
	r := Requirement{
		Actors: []Actor{
			*NewActor(4),
			*NewActor(4),
		},
		Predicates: []Predicate{},
	}

	for true {
		r.Tick()
	}
}
