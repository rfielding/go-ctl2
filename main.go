package main

import (
	"crypto/md5"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type Kind uint8

const (
	KindInvalid Kind = iota
	KindSymbol
	KindNumber
	KindString
	KindBool
	KindList
)

type Value struct {
	Kind  Kind
	Text  string
	Items []Value
}

type GuardFunc func(*Runtime, *Actor) bool
type ActionFunc func(*Runtime, *Actor) error
type MessageGuardFunc func(Value) bool
type MessageHandlerFunc func(*Runtime, *Actor, Value) error

func Symbol(text string) Value {
	return Value{Kind: KindSymbol, Text: text}
}

func Number(text string) Value {
	return Value{Kind: KindNumber, Text: text}
}

func String(text string) Value {
	return Value{Kind: KindString, Text: text}
}

func Bool(text string) Value {
	return Value{Kind: KindBool, Text: text}
}

func List(items ...Value) Value {
	return Value{Kind: KindList, Items: items}
}

type Transition struct {
	Name       string
	Guard      GuardFunc
	Action     ActionFunc
	NextStates []string
	GuardSpec  Value
	NextSpec   Value
	ActionSpec Value
}

type State struct {
	Name        string
	Guard       GuardFunc
	Control     bool
	Transitions []Transition
	Spec        Value
}

type Actor struct {
	Name   string
	Data   map[string]Value
	States []State
	Spec   Value
}

type Runtime struct {
	Actors        []*Actor
	Mailboxes     map[string][]Value
	MailboxCaps   map[string]int
	SyncInbox     map[string]Value
	Trace         []string
	ChooseActorFn func(*Runtime) int
	Dice          func() bool
}

type StepResult struct {
	Applied        bool
	ActorName      string
	StateName      string
	TransitionName string
}

type StatePredicate func(*Runtime) bool

type CTLOp uint8

const (
	CTLAtom CTLOp = iota
	CTLNot
	CTLAnd
	CTLOr
	CTLEX
	CTLAX
	CTLEF
	CTLAF
	CTLEG
	CTLAG
	CTLEU
	CTLAU
)

type CTLFormula struct {
	Op    CTLOp
	Pred  StatePredicate
	Left  *CTLFormula
	Right *CTLFormula
}

type Model struct {
	InitialID  string
	States     map[string]*ExploredState
	Successors map[string][]string
	Edges      []ExploredEdge
}

type ExploredState struct {
	ID      string
	Runtime *Runtime
}

type ExploredEdge struct {
	FromID         string
	ToID           string
	ActorName      string
	StateName      string
	TransitionName string
}

func NewRuntime(actors ...*Actor) *Runtime {
	rt := &Runtime{
		Actors:      actors,
		Mailboxes:   make(map[string][]Value, len(actors)),
		MailboxCaps: make(map[string]int, len(actors)),
		SyncInbox:   make(map[string]Value, len(actors)),
		Dice: func() bool {
			return true
		},
	}
	for _, actor := range actors {
		rt.Mailboxes[actor.Name] = nil
		rt.MailboxCaps[actor.Name] = -1
		if actor.Data == nil {
			actor.Data = map[string]Value{}
		}
	}
	return rt
}

func (rt *Runtime) Tick() error {
	if len(rt.Actors) == 0 {
		rt.Tracef("tick: no actors")
		return nil
	}

	index := 0
	if rt.ChooseActorFn != nil {
		index = rt.ChooseActorFn(rt)
	} else {
		index = rand.Intn(len(rt.Actors))
	}
	if index < 0 || index >= len(rt.Actors) {
		err := fmt.Errorf("actor index %d out of range", index)
		rt.Tracef("tick error: %v", err)
		return err
	}

	actor := rt.Actors[index]
	result, err := rt.StepActorDetailed(actor)
	if err != nil {
		rt.Tracef("tick error: actor=%s err=%v", actor.Name, err)
		return err
	}
	if !result.Applied {
		rt.Tracef("tick: actor=%s no-op", actor.Name)
	}
	return err
}

func (rt *Runtime) StepActor(actor *Actor) (bool, error) {
	result, err := rt.StepActorDetailed(actor)
	return result.Applied, err
}

func (rt *Runtime) StepActorDetailed(actor *Actor) (StepResult, error) {
	state := rt.CurrentState(actor)
	if state == nil {
		rt.Tracef("step: actor=%s no-state", actor.Name)
		return StepResult{ActorName: actor.Name}, nil
	}

	for _, transition := range state.Transitions {
		if !guardHolds(transition.Guard, rt, actor) {
			continue
		}
		if !rt.transitionReady(transition, actor, nil) {
			continue
		}
		if transition.Action == nil {
			rt.Tracef("step: actor=%s state=%s transition=%s", actor.Name, state.Name, transition.Name)
			if err := rt.validateTransitionNext(transition, actor); err != nil {
				rt.Tracef("step error: actor=%s state=%s transition=%s err=%v", actor.Name, state.Name, transition.Name, err)
				return StepResult{
					Applied:        true,
					ActorName:      actor.Name,
					StateName:      state.Name,
					TransitionName: transition.Name,
				}, err
			}
			return StepResult{
				Applied:        true,
				ActorName:      actor.Name,
				StateName:      state.Name,
				TransitionName: transition.Name,
			}, nil
		}
		rt.Tracef("step: actor=%s state=%s transition=%s", actor.Name, state.Name, transition.Name)
		if err := transition.Action(rt, actor); err != nil {
			rt.Tracef("step error: actor=%s state=%s transition=%s err=%v", actor.Name, state.Name, transition.Name, err)
			return StepResult{
				Applied:        true,
				ActorName:      actor.Name,
				StateName:      state.Name,
				TransitionName: transition.Name,
			}, err
		}
		if err := rt.validateTransitionNext(transition, actor); err != nil {
			rt.Tracef("step error: actor=%s state=%s transition=%s err=%v", actor.Name, state.Name, transition.Name, err)
			return StepResult{
				Applied:        true,
				ActorName:      actor.Name,
				StateName:      state.Name,
				TransitionName: transition.Name,
			}, err
		}
		return StepResult{
			Applied:        true,
			ActorName:      actor.Name,
			StateName:      state.Name,
			TransitionName: transition.Name,
		}, nil
	}

	rt.Tracef("step: actor=%s state=%s blocked", actor.Name, state.Name)
	return StepResult{
		ActorName: actor.Name,
		StateName: state.Name,
	}, nil
}

func (rt *Runtime) validateTransitionNext(transition Transition, actor *Actor) error {
	if len(transition.NextStates) == 0 {
		return nil
	}
	nextName, ok := actorStateName(actor)
	if ok {
		if actorStateByName(actor, nextName) == nil {
			return fmt.Errorf("actor %s entered undeclared state %s", actor.Name, nextName)
		}
		for _, name := range transition.NextStates {
			if name == nextName {
				return nil
			}
		}
		return fmt.Errorf("actor %s visited undeclared successor %s from transition %s", actor.Name, nextName, transition.Name)
	}

	next := rt.CurrentState(actor)
	if next == nil {
		return fmt.Errorf("actor %s fell out of known states after transition %s", actor.Name, transition.Name)
	}
	for _, name := range transition.NextStates {
		if name == next.Name {
			return nil
		}
	}
	return fmt.Errorf("actor %s visited undeclared successor %s from transition %s", actor.Name, next.Name, transition.Name)
}

func (rt *Runtime) CurrentState(actor *Actor) *State {
	if stateName, ok := actorStateName(actor); ok {
		if state := actorStateByName(actor, stateName); state != nil {
			return state
		}
	}
	for i := range actor.States {
		state := &actor.States[i]
		if state.Control {
			continue
		}
		if guardHolds(state.Guard, rt, actor) {
			return state
		}
	}
	return nil
}

func actorStateName(actor *Actor) (string, bool) {
	value, ok := actor.Data["state"]
	if !ok || value.Kind != KindSymbol {
		return "", false
	}
	return value.Text, true
}

func actorStateByName(actor *Actor, name string) *State {
	for i := range actor.States {
		if actor.States[i].Name == name {
			return &actor.States[i]
		}
	}
	return nil
}

func (rt *Runtime) HasReadyStep() bool {
	for _, actor := range rt.Actors {
		state := rt.CurrentState(actor)
		if state == nil {
			continue
		}
		for _, transition := range state.Transitions {
			if guardHolds(transition.Guard, rt, actor) && rt.transitionReady(transition, actor, nil) {
				return true
			}
		}
	}
	return false
}

func (rt *Runtime) Mailbox(name string) []Value {
	return rt.Mailboxes[name]
}

func (rt *Runtime) mailboxCap(name string) int {
	cap, ok := rt.MailboxCaps[name]
	if !ok {
		return -1
	}
	return cap
}

func (rt *Runtime) Enqueue(name string, message Value) {
	rt.Mailboxes[name] = append(rt.Mailboxes[name], message)
}

func (rt *Runtime) DequeueMatching(name string, guard MessageGuardFunc) (Value, bool) {
	mailbox := rt.Mailboxes[name]
	for i, message := range mailbox {
		if guard != nil && !guard(message) {
			continue
		}
		rt.Mailboxes[name] = append(mailbox[:i], mailbox[i+1:]...)
		return message, true
	}
	return Value{}, false
}

func (rt *Runtime) Tracef(format string, args ...interface{}) {
	rt.Trace = append(rt.Trace, fmt.Sprintf(format, args...))
}

func (rt *Runtime) Clone() *Runtime {
	clone := &Runtime{
		Actors:      make([]*Actor, len(rt.Actors)),
		Mailboxes:   make(map[string][]Value, len(rt.Mailboxes)),
		MailboxCaps: make(map[string]int, len(rt.MailboxCaps)),
		SyncInbox:   cloneValueMap(rt.SyncInbox),
		Trace:       append([]string(nil), rt.Trace...),
		Dice:        rt.Dice,
	}
	for i, actor := range rt.Actors {
		cloneActor := &Actor{
			Name:   actor.Name,
			Data:   cloneValueMap(actor.Data),
			States: actor.States,
		}
		clone.Actors[i] = cloneActor
	}
	for name, mailbox := range rt.Mailboxes {
		clone.Mailboxes[name] = cloneValueSlice(mailbox)
	}
	for name, cap := range rt.MailboxCaps {
		clone.MailboxCaps[name] = cap
	}
	return clone
}

func (rt *Runtime) StateKey() string {
	var b strings.Builder
	for _, actor := range rt.Actors {
		b.WriteString("actor:")
		b.WriteString(actor.Name)
		b.WriteString("{")
		keys := sortedValueKeys(actor.Data)
		for i, key := range keys {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(key)
			b.WriteString("=")
			b.WriteString(actor.Data[key].String())
		}
		b.WriteString("}")
		b.WriteString("|mailbox:")
		for i, message := range rt.Mailboxes[actor.Name] {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(message.String())
		}
		b.WriteString(";")
	}
	return b.String()
}

func (rt *Runtime) SuccessorRuntimes() ([]*Runtime, []ExploredEdge, error) {
	var out []*Runtime
	var edges []ExploredEdge
	for i := range rt.Actors {
		next := rt.Clone()
		result, err := next.StepActorDetailed(next.Actors[i])
		if err != nil {
			return nil, nil, err
		}
		if result.Applied {
			out = append(out, next)
			edges = append(edges, ExploredEdge{
				ActorName:      result.ActorName,
				StateName:      result.StateName,
				TransitionName: result.TransitionName,
			})
		}
	}
	return out, edges, nil
}

func ExploreModel(initial *Runtime) (*Model, error) {
	start := initial.Clone()
	model := &Model{
		InitialID:  start.StateKey(),
		States:     map[string]*ExploredState{},
		Successors: map[string][]string{},
	}

	queue := []*Runtime{start}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		id := current.StateKey()
		if _, ok := model.States[id]; ok {
			continue
		}
		model.States[id] = &ExploredState{
			ID:      id,
			Runtime: current,
		}

		successors, successorEdges, err := current.SuccessorRuntimes()
		if err != nil {
			return nil, err
		}
		if len(successors) == 0 {
			model.Successors[id] = []string{id}
			model.Edges = append(model.Edges, ExploredEdge{
				FromID:         id,
				ToID:           id,
				TransitionName: "deadlock",
			})
			continue
		}

		seen := map[string]bool{}
		for i, next := range successors {
			nextID := next.StateKey()
			if seen[nextID] {
				continue
			}
			seen[nextID] = true
			model.Successors[id] = append(model.Successors[id], nextID)
			edge := successorEdges[i]
			edge.FromID = id
			edge.ToID = nextID
			model.Edges = append(model.Edges, edge)
			if _, ok := model.States[nextID]; !ok {
				queue = append(queue, next)
			}
		}
	}

	return model, nil
}

func Atom(pred StatePredicate) CTLFormula {
	return CTLFormula{Op: CTLAtom, Pred: pred}
}

func CompileCTL(src string) (CTLFormula, error) {
	form, err := Read(src)
	if err != nil {
		return CTLFormula{}, err
	}
	return buildCTL(form)
}

func MustCompileCTL(src string) CTLFormula {
	formula, err := CompileCTL(src)
	if err != nil {
		panic(err)
	}
	return formula
}

func Not(inner CTLFormula) CTLFormula {
	return CTLFormula{Op: CTLNot, Left: &inner}
}

func And(left, right CTLFormula) CTLFormula {
	return CTLFormula{Op: CTLAnd, Left: &left, Right: &right}
}

func Or(left, right CTLFormula) CTLFormula {
	return CTLFormula{Op: CTLOr, Left: &left, Right: &right}
}

func EX(inner CTLFormula) CTLFormula {
	return CTLFormula{Op: CTLEX, Left: &inner}
}

func AX(inner CTLFormula) CTLFormula {
	return CTLFormula{Op: CTLAX, Left: &inner}
}

func EF(inner CTLFormula) CTLFormula {
	return CTLFormula{Op: CTLEF, Left: &inner}
}

func AF(inner CTLFormula) CTLFormula {
	return CTLFormula{Op: CTLAF, Left: &inner}
}

func EG(inner CTLFormula) CTLFormula {
	return CTLFormula{Op: CTLEG, Left: &inner}
}

func AG(inner CTLFormula) CTLFormula {
	return CTLFormula{Op: CTLAG, Left: &inner}
}

func EU(left, right CTLFormula) CTLFormula {
	return CTLFormula{Op: CTLEU, Left: &left, Right: &right}
}

func AU(left, right CTLFormula) CTLFormula {
	return CTLFormula{Op: CTLAU, Left: &left, Right: &right}
}

func buildCTL(form Value) (CTLFormula, error) {
	if !isList(form) || len(form.Items) == 0 {
		return CTLFormula{}, fmt.Errorf("ctl formula must be a non-empty list")
	}

	head, err := expectSymbol(form.Items[0], "ctl operator")
	if err != nil {
		return CTLFormula{}, err
	}
	switch head {
	case "not":
		if len(form.Items) != 2 {
			return CTLFormula{}, fmt.Errorf("not expects one operand")
		}
		inner, err := buildCTL(form.Items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return Not(inner), nil
	case "and":
		if len(form.Items) != 3 {
			return CTLFormula{}, fmt.Errorf("and expects two operands")
		}
		left, err := buildCTL(form.Items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		right, err := buildCTL(form.Items[2])
		if err != nil {
			return CTLFormula{}, err
		}
		return And(left, right), nil
	case "or":
		if len(form.Items) != 3 {
			return CTLFormula{}, fmt.Errorf("or expects two operands")
		}
		left, err := buildCTL(form.Items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		right, err := buildCTL(form.Items[2])
		if err != nil {
			return CTLFormula{}, err
		}
		return Or(left, right), nil
	case "ex":
		if len(form.Items) != 2 {
			return CTLFormula{}, fmt.Errorf("ex expects one operand")
		}
		inner, err := buildCTL(form.Items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return EX(inner), nil
	case "ax":
		if len(form.Items) != 2 {
			return CTLFormula{}, fmt.Errorf("ax expects one operand")
		}
		inner, err := buildCTL(form.Items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return AX(inner), nil
	case "ef":
		if len(form.Items) != 2 {
			return CTLFormula{}, fmt.Errorf("ef expects one operand")
		}
		inner, err := buildCTL(form.Items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return EF(inner), nil
	case "af":
		if len(form.Items) != 2 {
			return CTLFormula{}, fmt.Errorf("af expects one operand")
		}
		inner, err := buildCTL(form.Items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return AF(inner), nil
	case "eg":
		if len(form.Items) != 2 {
			return CTLFormula{}, fmt.Errorf("eg expects one operand")
		}
		inner, err := buildCTL(form.Items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return EG(inner), nil
	case "ag":
		if len(form.Items) != 2 {
			return CTLFormula{}, fmt.Errorf("ag expects one operand")
		}
		inner, err := buildCTL(form.Items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return AG(inner), nil
	case "eu":
		if len(form.Items) != 3 {
			return CTLFormula{}, fmt.Errorf("eu expects two operands")
		}
		left, err := buildCTL(form.Items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		right, err := buildCTL(form.Items[2])
		if err != nil {
			return CTLFormula{}, err
		}
		return EU(left, right), nil
	case "au":
		if len(form.Items) != 3 {
			return CTLFormula{}, fmt.Errorf("au expects two operands")
		}
		left, err := buildCTL(form.Items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		right, err := buildCTL(form.Items[2])
		if err != nil {
			return CTLFormula{}, err
		}
		return AU(left, right), nil
	case "in-state":
		if len(form.Items) != 3 {
			return CTLFormula{}, fmt.Errorf("in-state expects actor and state")
		}
		actor, err := expectSymbol(form.Items[1], "actor name")
		if err != nil {
			return CTLFormula{}, err
		}
		state, err := expectSymbol(form.Items[2], "state name")
		if err != nil {
			return CTLFormula{}, err
		}
		return Atom(ActorInState(actor, state)), nil
	case "data=":
		if len(form.Items) != 4 {
			return CTLFormula{}, fmt.Errorf("data= expects actor, key, value")
		}
		actor, err := expectSymbol(form.Items[1], "actor name")
		if err != nil {
			return CTLFormula{}, err
		}
		key, err := expectSymbol(form.Items[2], "data key")
		if err != nil {
			return CTLFormula{}, err
		}
		return Atom(ActorDataEquals(actor, key, form.Items[3])), nil
	case "mailbox-has":
		if len(form.Items) != 3 {
			return CTLFormula{}, fmt.Errorf("mailbox-has expects actor and message")
		}
		actor, err := expectSymbol(form.Items[1], "actor name")
		if err != nil {
			return CTLFormula{}, err
		}
		return Atom(MailboxHas(actor, form.Items[2])), nil
	default:
		return CTLFormula{}, fmt.Errorf("unsupported ctl operator %q", head)
	}
}

func (m *Model) HoldsAtInitial(formula CTLFormula) bool {
	return m.Holds(m.InitialID, formula)
}

func (m *Model) Holds(stateID string, formula CTLFormula) bool {
	set := m.SatisfyingStates(formula)
	return set[stateID]
}

func (m *Model) SatisfyingStates(formula CTLFormula) map[string]bool {
	switch formula.Op {
	case CTLAtom:
		out := map[string]bool{}
		for id, state := range m.States {
			if formula.Pred != nil && formula.Pred(state.Runtime) {
				out[id] = true
			}
		}
		return out
	case CTLNot:
		inner := m.SatisfyingStates(*formula.Left)
		out := map[string]bool{}
		for id := range m.States {
			if !inner[id] {
				out[id] = true
			}
		}
		return out
	case CTLAnd:
		left := m.SatisfyingStates(*formula.Left)
		right := m.SatisfyingStates(*formula.Right)
		out := map[string]bool{}
		for id := range m.States {
			if left[id] && right[id] {
				out[id] = true
			}
		}
		return out
	case CTLOr:
		left := m.SatisfyingStates(*formula.Left)
		right := m.SatisfyingStates(*formula.Right)
		out := map[string]bool{}
		for id := range m.States {
			if left[id] || right[id] {
				out[id] = true
			}
		}
		return out
	case CTLEX:
		inner := m.SatisfyingStates(*formula.Left)
		out := map[string]bool{}
		for id, succs := range m.Successors {
			for _, succ := range succs {
				if inner[succ] {
					out[id] = true
					break
				}
			}
		}
		return out
	case CTLAX:
		inner := m.SatisfyingStates(*formula.Left)
		out := map[string]bool{}
		for id, succs := range m.Successors {
			all := true
			for _, succ := range succs {
				if !inner[succ] {
					all = false
					break
				}
			}
			if all {
				out[id] = true
			}
		}
		return out
	case CTLEF:
		return m.SatisfyingStates(EU(Atom(func(*Runtime) bool { return true }), *formula.Left))
	case CTLAF:
		return m.SatisfyingStates(AU(Atom(func(*Runtime) bool { return true }), *formula.Left))
	case CTLEG:
		inner := m.SatisfyingStates(*formula.Left)
		out := copyStateSet(inner)
		changed := true
		for changed {
			changed = false
			for id := range out {
				if !inner[id] {
					delete(out, id)
					changed = true
					continue
				}
				ok := false
				for _, succ := range m.Successors[id] {
					if out[succ] {
						ok = true
						break
					}
				}
				if !ok {
					delete(out, id)
					changed = true
				}
			}
		}
		return out
	case CTLAG:
		return m.SatisfyingStates(Not(EF(Not(*formula.Left))))
	case CTLEU:
		left := m.SatisfyingStates(*formula.Left)
		right := m.SatisfyingStates(*formula.Right)
		out := copyStateSet(right)
		changed := true
		for changed {
			changed = false
			for id := range m.States {
				if out[id] || !left[id] {
					continue
				}
				for _, succ := range m.Successors[id] {
					if out[succ] {
						out[id] = true
						changed = true
						break
					}
				}
			}
		}
		return out
	case CTLAU:
		left := m.SatisfyingStates(*formula.Left)
		right := m.SatisfyingStates(*formula.Right)
		out := copyStateSet(right)
		changed := true
		for changed {
			changed = false
			for id := range m.States {
				if out[id] || !left[id] {
					continue
				}
				all := true
				for _, succ := range m.Successors[id] {
					if !out[succ] {
						all = false
						break
					}
				}
				if all {
					out[id] = true
					changed = true
				}
			}
		}
		return out
	default:
		return map[string]bool{}
	}
}

func ActorInState(actorName, stateName string) StatePredicate {
	return func(rt *Runtime) bool {
		for _, actor := range rt.Actors {
			if actor.Name == actorName {
				return actor.Data["state"].Equal(Symbol(stateName))
			}
		}
		return false
	}
}

func ActorDataEquals(actorName, key string, want Value) StatePredicate {
	return func(rt *Runtime) bool {
		for _, actor := range rt.Actors {
			if actor.Name == actorName {
				return actor.Data[key].Equal(want)
			}
		}
		return false
	}
}

func MailboxHas(actorName string, want Value) StatePredicate {
	return func(rt *Runtime) bool {
		for _, message := range rt.Mailbox(actorName) {
			if message.Equal(want) {
				return true
			}
		}
		return false
	}
}

func Send(to string, message Value) ActionFunc {
	return func(rt *Runtime, actor *Actor) error {
		if rt.mailboxCap(to) == 0 {
			if err := rt.rendezvous(to, message); err != nil {
				return err
			}
			rt.Tracef("%s -> %s %s", actor.Name, to, message.String())
			return nil
		}
		if cap := rt.mailboxCap(to); cap >= 0 && len(rt.Mailbox(to)) >= cap {
			return fmt.Errorf("mailbox %s is full", to)
		}
		rt.Enqueue(to, message)
		rt.Tracef("%s -> %s %s", actor.Name, to, message.String())
		return nil
	}
}

func Receive(match MessageGuardFunc, handler MessageHandlerFunc) ActionFunc {
	return func(rt *Runtime, actor *Actor) error {
		if offered, ok := rt.SyncInbox[actor.Name]; ok {
			if match == nil || match(offered) {
				delete(rt.SyncInbox, actor.Name)
				rt.Tracef("%s <= %s", actor.Name, offered.String())
				if handler == nil {
					return nil
				}
				return handler(rt, actor, offered)
			}
		}
		message, ok := rt.DequeueMatching(actor.Name, match)
		if !ok {
			return nil
		}
		rt.Tracef("%s <= %s", actor.Name, message.String())
		if handler == nil {
			return nil
		}
		return handler(rt, actor, message)
	}
}

func MatchMessage(want Value) MessageGuardFunc {
	return func(got Value) bool {
		return got.Equal(want)
	}
}

func guardHolds(guard GuardFunc, rt *Runtime, actor *Actor) bool {
	if guard == nil {
		return true
	}
	return guard(rt, actor)
}

func (rt *Runtime) transitionReady(transition Transition, actor *Actor, offered *Value) bool {
	if transition.ActionSpec.Kind == KindInvalid {
		return true
	}
	return rt.actionReady(transition.ActionSpec, actor, offered)
}

func (rt *Runtime) actionReady(form Value, actor *Actor, offered *Value) bool {
	if !isList(form) || len(form.Items) == 0 {
		return false
	}
	head, err := expectSymbol(form.Items[0], "action operator")
	if err != nil {
		return false
	}
	switch head {
	case "do":
		for _, item := range form.Items[1:] {
			if !rt.actionReady(item, actor, offered) {
				return false
			}
		}
		return true
	case "if":
		if len(form.Items) != 3 && len(form.Items) != 4 {
			return false
		}
		ok, err := rt.evalGuardSpec(form.Items[1], actor, offered)
		if err != nil {
			return false
		}
		if ok {
			return rt.actionReady(form.Items[2], actor, offered)
		}
		if len(form.Items) == 4 {
			return rt.actionReady(form.Items[3], actor, offered)
		}
		return true
	case "send":
		if len(form.Items) != 3 {
			return false
		}
		target, err := expectSymbol(form.Items[1], "send target")
		if err != nil {
			return false
		}
		message := form.Items[2]
		cap := rt.mailboxCap(target)
		if cap == 0 {
			return rt.canRendezvous(target, message)
		}
		return cap < 0 || len(rt.Mailbox(target)) < cap
	case "recv":
		if len(form.Items) != 2 {
			return false
		}
		want := form.Items[1]
		if offered != nil && offered.Equal(want) {
			return true
		}
		for _, message := range rt.Mailbox(actor.Name) {
			if message.Equal(want) {
				return true
			}
		}
		return false
	case "become", "set":
		return true
	default:
		return true
	}
}

func (rt *Runtime) canRendezvous(target string, message Value) bool {
	targetActor := rt.actorByName(target)
	if targetActor == nil {
		return false
	}
	state := rt.CurrentState(targetActor)
	if state == nil {
		return false
	}
	offered := message
	for _, transition := range state.Transitions {
		if !rt.guardHoldsSpec(transition, targetActor, &offered) {
			continue
		}
		if !rt.transitionReady(transition, targetActor, &offered) {
			continue
		}
		return true
	}
	return false
}

func (rt *Runtime) rendezvous(target string, message Value) error {
	targetActor := rt.actorByName(target)
	if targetActor == nil {
		return fmt.Errorf("unknown actor %s", target)
	}
	state := rt.CurrentState(targetActor)
	if state == nil {
		return fmt.Errorf("actor %s has no current state", target)
	}
	offered := message
	for _, transition := range state.Transitions {
		if !rt.guardHoldsSpec(transition, targetActor, &offered) {
			continue
		}
		if !rt.transitionReady(transition, targetActor, &offered) {
			continue
		}
		rt.SyncInbox[target] = message
		rt.Tracef("step: actor=%s state=%s transition=%s", targetActor.Name, state.Name, transition.Name)
		if transition.Action != nil {
			if err := transition.Action(rt, targetActor); err != nil {
				delete(rt.SyncInbox, target)
				return err
			}
		}
		if err := rt.validateTransitionNext(transition, targetActor); err != nil {
			delete(rt.SyncInbox, target)
			return err
		}
		return nil
	}
	return fmt.Errorf("no rendezvous receiver ready on %s for %s", target, message.String())
}

func (rt *Runtime) guardHoldsSpec(transition Transition, actor *Actor, offered *Value) bool {
	if transition.GuardSpec.Kind != KindInvalid {
		ok, err := rt.evalGuardSpec(transition.GuardSpec, actor, offered)
		if err == nil {
			return ok
		}
	}
	return guardHolds(transition.Guard, rt, actor)
}

func (rt *Runtime) evalGuardSpec(form Value, actor *Actor, offered *Value) (bool, error) {
	if form.Kind == KindBool {
		return form.Text == "true", nil
	}
	if form.Kind == KindSymbol {
		switch form.Text {
		case "true":
			return true, nil
		case "dice":
			if rt.Dice == nil {
				return true, nil
			}
			return rt.Dice(), nil
		default:
			return false, fmt.Errorf("unsupported guard symbol %q", form.Text)
		}
	}
	if !isList(form) || len(form.Items) == 0 {
		return false, fmt.Errorf("unsupported guard %s", form.String())
	}
	head, err := expectSymbol(form.Items[0], "guard operator")
	if err != nil {
		return false, err
	}
	switch head {
	case "mailbox":
		if len(form.Items) != 2 {
			return false, fmt.Errorf("mailbox guard must be (mailbox message)")
		}
		want := form.Items[1]
		if offered != nil && offered.Equal(want) {
			return true, nil
		}
		for _, message := range rt.Mailbox(actor.Name) {
			if message.Equal(want) {
				return true, nil
			}
		}
		return false, nil
	case "and":
		for _, item := range form.Items[1:] {
			ok, err := rt.evalGuardSpec(item, actor, offered)
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	case "not":
		if len(form.Items) != 2 {
			return false, fmt.Errorf("not guard needs one operand")
		}
		ok, err := rt.evalGuardSpec(form.Items[1], actor, offered)
		if err != nil {
			return false, err
		}
		return !ok, nil
	case "data=":
		if len(form.Items) != 3 {
			return false, fmt.Errorf("data= guard must be (data= key value)")
		}
		key, err := expectSymbol(form.Items[1], "data key")
		if err != nil {
			return false, err
		}
		return actor.Data[key].Equal(form.Items[2]), nil
	case "data>":
		if len(form.Items) != 3 {
			return false, fmt.Errorf("data> guard must be (data> key value)")
		}
		key, err := expectSymbol(form.Items[1], "data key")
		if err != nil {
			return false, err
		}
		got, err := valueInt(actor.Data[key])
		if err != nil {
			return false, err
		}
		want, err := valueInt(form.Items[2])
		if err != nil {
			return false, err
		}
		return got > want, nil
	default:
		return false, fmt.Errorf("unsupported guard form %q", head)
	}
}

func (rt *Runtime) actorByName(name string) *Actor {
	for _, actor := range rt.Actors {
		if actor.Name == name {
			return actor
		}
	}
	return nil
}

func (v Value) Equal(other Value) bool {
	if v.Kind != other.Kind || v.Text != other.Text || len(v.Items) != len(other.Items) {
		return false
	}
	for i := range v.Items {
		if !v.Items[i].Equal(other.Items[i]) {
			return false
		}
	}
	return true
}

func CompileActor(src string) (*Actor, error) {
	form, err := Read(src)
	if err != nil {
		return nil, err
	}
	return buildActor(form)
}

func MustCompileActor(src string) *Actor {
	actor, err := CompileActor(src)
	if err != nil {
		panic(err)
	}
	return actor
}

func buildActor(form Value) (*Actor, error) {
	if !isListHead(form, "actor") || len(form.Items) < 3 {
		return nil, fmt.Errorf("actor form must be (actor name state...)")
	}
	name, err := expectSymbol(form.Items[1], "actor name")
	if err != nil {
		return nil, err
	}

	actor := &Actor{
		Name: name,
		Data: map[string]Value{},
		Spec: form,
	}
	for _, item := range form.Items[2:] {
		state, err := buildState(item)
		if err != nil {
			return nil, fmt.Errorf("actor %s: %w", name, err)
		}
		actor.States = append(actor.States, state)
	}
	if len(actor.States) == 0 {
		return nil, fmt.Errorf("actor %s: no states", name)
	}
	actor.Data["state"] = Symbol(actor.States[0].Name)
	return actor, nil
}

func buildState(form Value) (State, error) {
	if !isListHead(form, "state") || len(form.Items) < 2 {
		return State{}, fmt.Errorf("state form must be (state name ...)")
	}
	name, err := expectSymbol(form.Items[1], "state name")
	if err != nil {
		return State{}, err
	}

	state := State{
		Name:    name,
		Control: true,
		Spec:    form,
	}
	for _, item := range form.Items[2:] {
		transition, err := buildTransition(item)
		if err != nil {
			return State{}, fmt.Errorf("state %s: %w", name, err)
		}
		state.Transitions = append(state.Transitions, transition)
	}
	return state, nil
}

func buildStateNames(form Value, head string) ([]string, error) {
	if len(form.Items) < 2 {
		return nil, fmt.Errorf("%s form must be (%s name...)", head, head)
	}
	names := make([]string, 0, len(form.Items)-1)
	for _, item := range form.Items[1:] {
		name, err := expectSymbol(item, "successor name")
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

func buildTransition(form Value) (Transition, error) {
	if !isListHead(form, "on") || len(form.Items) < 4 {
		return Transition{}, fmt.Errorf("transition form must be (on guard (next state...) action...)")
	}
	if !isListHead(form.Items[2], "next") {
		return Transition{}, fmt.Errorf("transition form must declare (next state...)")
	}
	nextStates, err := buildStateNames(form.Items[2], "next")
	if err != nil {
		return Transition{}, err
	}
	guard, err := compileGuard(form.Items[1])
	if err != nil {
		return Transition{}, err
	}
	action, err := compileAction(seqForm(form.Items[3:]))
	if err != nil {
		return Transition{}, err
	}
	return Transition{
		Name:       form.Items[1].String(),
		Guard:      guard,
		Action:     action,
		NextStates: nextStates,
		GuardSpec:  form.Items[1],
		NextSpec:   form.Items[2],
		ActionSpec: seqForm(form.Items[3:]),
	}, nil
}

func compileGuard(form Value) (GuardFunc, error) {
	if form.Kind == KindBool {
		return func(*Runtime, *Actor) bool {
			return form.Text == "true"
		}, nil
	}
	if form.Kind == KindSymbol {
		switch form.Text {
		case "true":
			return func(*Runtime, *Actor) bool { return true }, nil
		case "dice":
			return func(rt *Runtime, _ *Actor) bool {
				if rt.Dice == nil {
					return true
				}
				return rt.Dice()
			}, nil
		default:
			return nil, fmt.Errorf("unsupported guard symbol %q", form.Text)
		}
	}

	if !isList(form) || len(form.Items) == 0 {
		return nil, fmt.Errorf("unsupported guard %s", form.String())
	}

	head, err := expectSymbol(form.Items[0], "guard operator")
	if err != nil {
		return nil, err
	}
	switch head {
	case "mailbox":
		if len(form.Items) != 2 {
			return nil, fmt.Errorf("mailbox guard must be (mailbox message)")
		}
		want := form.Items[1]
		return func(rt *Runtime, actor *Actor) bool {
			for _, message := range rt.Mailbox(actor.Name) {
				if message.Equal(want) {
					return true
				}
			}
			return false
		}, nil
	case "and":
		if len(form.Items) < 3 {
			return nil, fmt.Errorf("and guard needs at least two operands")
		}
		var guards []GuardFunc
		for _, item := range form.Items[1:] {
			guard, err := compileGuard(item)
			if err != nil {
				return nil, err
			}
			guards = append(guards, guard)
		}
		return func(rt *Runtime, actor *Actor) bool {
			for _, guard := range guards {
				if !guard(rt, actor) {
					return false
				}
			}
			return true
		}, nil
	case "not":
		if len(form.Items) != 2 {
			return nil, fmt.Errorf("not guard needs one operand")
		}
		inner, err := compileGuard(form.Items[1])
		if err != nil {
			return nil, err
		}
		return func(rt *Runtime, actor *Actor) bool {
			return !inner(rt, actor)
		}, nil
	case "data=":
		if len(form.Items) != 3 {
			return nil, fmt.Errorf("data= guard must be (data= key value)")
		}
		key, err := expectSymbol(form.Items[1], "data key")
		if err != nil {
			return nil, err
		}
		want := form.Items[2]
		return func(_ *Runtime, actor *Actor) bool {
			return actor.Data[key].Equal(want)
		}, nil
	case "data>":
		if len(form.Items) != 3 {
			return nil, fmt.Errorf("data> guard must be (data> key value)")
		}
		key, err := expectSymbol(form.Items[1], "data key")
		if err != nil {
			return nil, err
		}
		want, err := valueInt(form.Items[2])
		if err != nil {
			return nil, err
		}
		return func(_ *Runtime, actor *Actor) bool {
			got, err := valueInt(actor.Data[key])
			return err == nil && got > want
		}, nil
	default:
		return nil, fmt.Errorf("unsupported guard form %q", head)
	}
}

func compileAction(form Value) (ActionFunc, error) {
	if !isList(form) || len(form.Items) == 0 {
		return nil, fmt.Errorf("unsupported action %s", form.String())
	}

	head, err := expectSymbol(form.Items[0], "action operator")
	if err != nil {
		return nil, err
	}
	switch head {
	case "do":
		if len(form.Items) < 2 {
			return nil, fmt.Errorf("do needs at least one action")
		}
		var actions []ActionFunc
		for _, item := range form.Items[1:] {
			action, err := compileAction(item)
			if err != nil {
				return nil, err
			}
			actions = append(actions, action)
		}
		return func(rt *Runtime, actor *Actor) error {
			for _, action := range actions {
				if err := action(rt, actor); err != nil {
					return err
				}
			}
			return nil
		}, nil
	case "send":
		if len(form.Items) != 3 {
			return nil, fmt.Errorf("send must be (send actor message)")
		}
		to, err := expectSymbol(form.Items[1], "send target")
		if err != nil {
			return nil, err
		}
		return Send(to, form.Items[2]), nil
	case "recv":
		if len(form.Items) != 2 {
			return nil, fmt.Errorf("recv must be (recv message)")
		}
		return Receive(MatchMessage(form.Items[1]), nil), nil
	case "become":
		if len(form.Items) != 2 {
			return nil, fmt.Errorf("become must be (become state)")
		}
		name, err := expectSymbol(form.Items[1], "state name")
		if err != nil {
			return nil, err
		}
		return func(_ *Runtime, actor *Actor) error {
			actor.Data["state"] = Symbol(name)
			return nil
		}, nil
	case "set":
		if len(form.Items) != 3 {
			return nil, fmt.Errorf("set must be (set key value)")
		}
		key, err := expectSymbol(form.Items[1], "set key")
		if err != nil {
			return nil, err
		}
		value := form.Items[2]
		return func(_ *Runtime, actor *Actor) error {
			actor.Data[key] = value
			return nil
		}, nil
	case "if":
		if len(form.Items) != 3 && len(form.Items) != 4 {
			return nil, fmt.Errorf("if must be (if guard then [else])")
		}
		cond, err := compileGuard(form.Items[1])
		if err != nil {
			return nil, err
		}
		thenAction, err := compileAction(form.Items[2])
		if err != nil {
			return nil, err
		}
		var elseAction ActionFunc
		if len(form.Items) == 4 {
			elseAction, err = compileAction(form.Items[3])
			if err != nil {
				return nil, err
			}
		}
		return func(rt *Runtime, actor *Actor) error {
			if cond(rt, actor) {
				return thenAction(rt, actor)
			}
			if elseAction != nil {
				return elseAction(rt, actor)
			}
			return nil
		}, nil
	case "add":
		if len(form.Items) != 3 {
			return nil, fmt.Errorf("add must be (add key value)")
		}
		key, err := expectSymbol(form.Items[1], "add key")
		if err != nil {
			return nil, err
		}
		delta, err := valueInt(form.Items[2])
		if err != nil {
			return nil, err
		}
		return func(_ *Runtime, actor *Actor) error {
			current, err := valueInt(actor.Data[key])
			if err != nil {
				return err
			}
			actor.Data[key] = Number(strconv.Itoa(current + delta))
			return nil
		}, nil
	case "sub":
		if len(form.Items) != 3 {
			return nil, fmt.Errorf("sub must be (sub key value)")
		}
		key, err := expectSymbol(form.Items[1], "sub key")
		if err != nil {
			return nil, err
		}
		delta, err := valueInt(form.Items[2])
		if err != nil {
			return nil, err
		}
		return func(_ *Runtime, actor *Actor) error {
			current, err := valueInt(actor.Data[key])
			if err != nil {
				return err
			}
			actor.Data[key] = Number(strconv.Itoa(current - delta))
			return nil
		}, nil
	case "md5":
		if len(form.Items) != 3 {
			return nil, fmt.Errorf("md5 must be (md5 out source)")
		}
		out, err := expectSymbol(form.Items[1], "md5 out key")
		if err != nil {
			return nil, err
		}
		source := form.Items[2]
		return func(_ *Runtime, actor *Actor) error {
			value := resolveOperand(actor, source)
			sum := md5.Sum([]byte(valueText(value)))
			actor.Data[out] = String(hex.EncodeToString(sum[:]))
			return nil
		}, nil
	case "rsa-raw":
		if len(form.Items) != 5 {
			return nil, fmt.Errorf("rsa-raw must be (rsa-raw out modulus exponent message)")
		}
		out, err := expectSymbol(form.Items[1], "rsa out key")
		if err != nil {
			return nil, err
		}
		modulus := form.Items[2]
		exponent := form.Items[3]
		message := form.Items[4]
		return func(_ *Runtime, actor *Actor) error {
			n, err := valueBigInt(resolveOperand(actor, modulus))
			if err != nil {
				return err
			}
			e, err := valueBigInt(resolveOperand(actor, exponent))
			if err != nil {
				return err
			}
			m, err := valueBigInt(resolveOperand(actor, message))
			if err != nil {
				return err
			}
			result := new(big.Int).Exp(m, e, n)
			actor.Data[out] = Number(result.String())
			return nil
		}, nil
	case "cryptorandom":
		if len(form.Items) != 3 {
			return nil, fmt.Errorf("cryptorandom must be (cryptorandom out bytes)")
		}
		out, err := expectSymbol(form.Items[1], "cryptorandom out key")
		if err != nil {
			return nil, err
		}
		nbytes, err := valueInt(form.Items[2])
		if err != nil {
			return nil, err
		}
		if nbytes < 0 {
			return nil, fmt.Errorf("cryptorandom byte count must be non-negative")
		}
		return func(_ *Runtime, actor *Actor) error {
			buf := make([]byte, nbytes)
			if _, err := crand.Read(buf); err != nil {
				return err
			}
			actor.Data[out] = String(hex.EncodeToString(buf))
			return nil
		}, nil
	default:
		return nil, fmt.Errorf("unsupported action form %q", head)
	}
}

func seqForm(forms []Value) Value {
	if len(forms) == 1 {
		return forms[0]
	}
	items := make([]Value, 0, len(forms)+1)
	items = append(items, Symbol("do"))
	items = append(items, forms...)
	return List(items...)
}

func isList(v Value) bool {
	return v.Kind == KindList
}

func copyStateSet(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneValueSlice(in []Value) []Value {
	out := make([]Value, len(in))
	for i, value := range in {
		out[i] = cloneValue(value)
	}
	return out
}

func cloneValueMap(in map[string]Value) map[string]Value {
	out := make(map[string]Value, len(in))
	for key, value := range in {
		out[key] = cloneValue(value)
	}
	return out
}

func cloneValue(in Value) Value {
	out := Value{
		Kind: in.Kind,
		Text: in.Text,
	}
	if len(in.Items) > 0 {
		out.Items = cloneValueSlice(in.Items)
	}
	return out
}

func sortedValueKeys(in map[string]Value) []string {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func valueInt(v Value) (int, error) {
	if v.Kind != KindNumber {
		return 0, fmt.Errorf("value %s is not a number", v.String())
	}
	return strconv.Atoi(v.Text)
}

func valueBigInt(v Value) (*big.Int, error) {
	switch v.Kind {
	case KindNumber:
		out, ok := new(big.Int).SetString(v.Text, 10)
		if !ok {
			return nil, fmt.Errorf("invalid integer %s", v.Text)
		}
		return out, nil
	case KindString:
		out, ok := new(big.Int).SetString(v.Text, 10)
		if !ok {
			return nil, fmt.Errorf("invalid integer string %s", v.Text)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("value %s is not an integer", v.String())
	}
}

func resolveOperand(actor *Actor, operand Value) Value {
	if operand.Kind == KindSymbol {
		if value, ok := actor.Data[operand.Text]; ok {
			return value
		}
	}
	return operand
}

func valueText(v Value) string {
	switch v.Kind {
	case KindString, KindSymbol, KindNumber, KindBool:
		return v.Text
	default:
		return v.String()
	}
}

func isListHead(v Value, want string) bool {
	return v.Kind == KindList && len(v.Items) > 0 && v.Items[0].Kind == KindSymbol && v.Items[0].Text == want
}

func expectSymbol(v Value, context string) (string, error) {
	if v.Kind != KindSymbol {
		return "", fmt.Errorf("%s must be a symbol", context)
	}
	return v.Text, nil
}

func Read(input string) (Value, error) {
	tokens, err := tokenize(input)
	if err != nil {
		return Value{}, err
	}

	p := parser{tokens: tokens}
	v, err := p.parseValue()
	if err != nil {
		return Value{}, err
	}
	if p.hasNext() {
		return Value{}, fmt.Errorf("unexpected token %q", p.peek().text)
	}
	return v, nil
}

func MustRead(input string) Value {
	v, err := Read(input)
	if err != nil {
		panic(err)
	}
	return v
}

func (v Value) String() string {
	switch v.Kind {
	case KindSymbol:
		return v.Text
	case KindNumber:
		return v.Text
	case KindString:
		return strconv.Quote(v.Text)
	case KindBool:
		return v.Text
	case KindList:
		parts := make([]string, 0, len(v.Items))
		for _, item := range v.Items {
			parts = append(parts, item.String())
		}
		return "(" + strings.Join(parts, " ") + ")"
	default:
		return "<invalid>"
	}
}

func (t Transition) Lisp() Value {
	if t.GuardSpec.Kind != KindInvalid && t.NextSpec.Kind != KindInvalid && t.ActionSpec.Kind != KindInvalid {
		items := []Value{Symbol("on"), t.GuardSpec, t.NextSpec}
		if isListHead(t.ActionSpec, "do") {
			items = append(items, t.ActionSpec.Items[1:]...)
		} else {
			items = append(items, t.ActionSpec)
		}
		return List(items...)
	}
	items := []Value{Symbol("on"), Symbol(t.Name)}
	if len(t.NextStates) > 0 {
		next := []Value{Symbol("next")}
		for _, name := range t.NextStates {
			next = append(next, Symbol(name))
		}
		items = append(items, List(next...))
	}
	if t.ActionSpec.Kind != KindInvalid {
		items = append(items, t.ActionSpec)
	}
	return List(items...)
}

func (s State) Lisp() Value {
	if s.Spec.Kind != KindInvalid {
		return s.Spec
	}
	items := []Value{Symbol("state"), Symbol(s.Name)}
	for _, transition := range s.Transitions {
		items = append(items, transition.Lisp())
	}
	return List(items...)
}

func (a Actor) Lisp() Value {
	if a.Spec.Kind != KindInvalid {
		return a.Spec
	}
	items := []Value{Symbol("actor"), Symbol(a.Name)}
	for _, state := range a.States {
		items = append(items, state.Lisp())
	}
	return List(items...)
}

func (rt *Runtime) Lisp() Value {
	items := []Value{Symbol("runtime")}
	for _, actor := range rt.Actors {
		items = append(items, actor.Lisp())
		items = append(items, runtimeActorState(actor))
		items = append(items, runtimeActorData(actor))
		items = append(items, runtimeMailbox(actor.Name, rt.Mailbox(actor.Name)))
	}
	return List(items...)
}

func runtimeActorState(actor *Actor) Value {
	if stateName, ok := actorStateName(actor); ok {
		return List(Symbol("current-state"), Symbol(actor.Name), Symbol(stateName))
	}
	return List(Symbol("current-state"), Symbol(actor.Name), Symbol("<unknown>"))
}

func runtimeActorData(actor *Actor) Value {
	items := []Value{Symbol("data"), Symbol(actor.Name)}
	keys := sortedValueKeys(actor.Data)
	for _, key := range keys {
		items = append(items, List(Symbol(key), actor.Data[key]))
	}
	return List(items...)
}

func runtimeMailbox(name string, messages []Value) Value {
	items := []Value{Symbol("mailbox"), Symbol(name)}
	items = append(items, messages...)
	return List(items...)
}

type token struct {
	kind      string
	text      string
	tightLeft bool
}

type parser struct {
	tokens []token
	pos    int
}

func (p *parser) parseValue() (Value, error) {
	if !p.hasNext() {
		return Value{}, fmt.Errorf("unexpected end of input")
	}

	switch p.peek().kind {
	case "quote":
		return p.parseQuote()
	case "lparen":
		return p.parseSExpr()
	case "symbol", "number", "string", "bool":
		if p.canStartMExpr() {
			return p.parseMExpr()
		}
		return p.parseAtom()
	default:
		return Value{}, fmt.Errorf("unexpected token %q", p.peek().text)
	}
}

func (p *parser) parseQuote() (Value, error) {
	if !p.match("quote") {
		return Value{}, fmt.Errorf("expected quote")
	}
	quoted, err := p.parseValue()
	if err != nil {
		return Value{}, err
	}
	return List(Symbol("quote"), quoted), nil
}

func (p *parser) parseSExpr() (Value, error) {
	if !p.match("lparen") {
		return Value{}, fmt.Errorf("expected '('")
	}

	var items []Value
	for {
		if !p.hasNext() {
			return Value{}, fmt.Errorf("unterminated list")
		}
		if p.match("rparen") {
			return List(items...), nil
		}

		var (
			item Value
			err  error
		)
		if len(items) == 0 {
			item, err = p.parseHeadValue()
		} else {
			item, err = p.parseValue()
		}
		if err != nil {
			return Value{}, err
		}
		items = append(items, item)
	}
}

func (p *parser) parseHeadValue() (Value, error) {
	if !p.hasNext() {
		return Value{}, fmt.Errorf("unexpected end of input")
	}

	switch p.peek().kind {
	case "symbol", "number", "string", "bool":
		return p.parseAtom()
	default:
		return p.parseValue()
	}
}

func (p *parser) parseMExpr() (Value, error) {
	head, err := p.parseAtom()
	if err != nil {
		return Value{}, err
	}
	if !p.match("lparen") {
		return Value{}, fmt.Errorf("expected '(' after %q", head.Text)
	}

	items := []Value{head}
	for {
		if !p.hasNext() {
			return Value{}, fmt.Errorf("unterminated m-expression")
		}
		if p.match("rparen") {
			return List(items...), nil
		}

		item, err := p.parseValue()
		if err != nil {
			return Value{}, err
		}
		items = append(items, item)

		if p.match("comma") {
			continue
		}
		if p.peek().kind == "rparen" {
			continue
		}
	}
}

func (p *parser) parseAtom() (Value, error) {
	if !p.hasNext() {
		return Value{}, fmt.Errorf("unexpected end of input")
	}

	tok := p.peek()
	p.pos++

	switch tok.kind {
	case "symbol":
		return Symbol(tok.text), nil
	case "number":
		return Number(tok.text), nil
	case "string":
		return String(tok.text), nil
	case "bool":
		return Bool(tok.text), nil
	default:
		return Value{}, fmt.Errorf("unexpected atom %q", tok.text)
	}
}

func (p *parser) canStartMExpr() bool {
	return p.pos+1 < len(p.tokens) &&
		p.tokens[p.pos+1].kind == "lparen" &&
		p.tokens[p.pos+1].tightLeft
}

func (p *parser) hasNext() bool {
	return p.pos < len(p.tokens)
}

func (p *parser) peek() token {
	return p.tokens[p.pos]
}

func (p *parser) match(kind string) bool {
	if !p.hasNext() || p.tokens[p.pos].kind != kind {
		return false
	}
	p.pos++
	return true
}

func tokenize(input string) ([]token, error) {
	var out []token
	lastEnd := 0

	for i := 0; i < len(input); {
		ch := rune(input[i])

		if unicode.IsSpace(ch) {
			i++
			continue
		}

		switch ch {
		case '\'':
			out = append(out, token{
				kind:      "quote",
				text:      "'",
				tightLeft: i == lastEnd,
			})
			i++
			lastEnd = i
			continue
		case '(':
			out = append(out, token{
				kind:      "lparen",
				text:      "(",
				tightLeft: i == lastEnd,
			})
			i++
			lastEnd = i
			continue
		case ')':
			out = append(out, token{
				kind:      "rparen",
				text:      ")",
				tightLeft: i == lastEnd,
			})
			i++
			lastEnd = i
			continue
		case ',':
			out = append(out, token{
				kind:      "comma",
				text:      ",",
				tightLeft: i == lastEnd,
			})
			i++
			lastEnd = i
			continue
		case '"':
			start := i + 1
			i++
			for i < len(input) && input[i] != '"' {
				i++
			}
			if i >= len(input) {
				return nil, fmt.Errorf("unterminated string literal")
			}
			out = append(out, token{
				kind:      "string",
				text:      input[start:i],
				tightLeft: start-1 == lastEnd,
			})
			i++
			lastEnd = i
			continue
		}

		if unicode.IsDigit(ch) || (ch == '-' && i+1 < len(input) && unicode.IsDigit(rune(input[i+1]))) {
			start := i
			i++
			for i < len(input) && unicode.IsDigit(rune(input[i])) {
				i++
			}
			out = append(out, token{
				kind:      "number",
				text:      input[start:i],
				tightLeft: start == lastEnd,
			})
			lastEnd = i
			continue
		}

		if isSymbolStart(ch) {
			start := i
			i++
			for i < len(input) && isSymbolPart(rune(input[i])) {
				i++
			}
			text := input[start:i]
			switch text {
			case "true", "false":
				out = append(out, token{
					kind:      "bool",
					text:      text,
					tightLeft: start == lastEnd,
				})
			default:
				out = append(out, token{
					kind:      "symbol",
					text:      text,
					tightLeft: start == lastEnd,
				})
			}
			lastEnd = i
			continue
		}

		return nil, fmt.Errorf("unexpected character %q", string(ch))
	}

	return out, nil
}

func isSymbolStart(ch rune) bool {
	return unicode.IsLetter(ch) || strings.ContainsRune("+-*/<>=!?_", ch)
}

func isSymbolPart(ch rune) bool {
	return unicode.IsLetter(ch) || unicode.IsDigit(ch) || strings.ContainsRune("+-*/<>=!?_", ch)
}

func main() {}
