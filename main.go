package main

import (
	"crypto/md5"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
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

type FunctionDef struct {
	Params []string
	Body   Value
}

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
	Defs   map[string]FunctionDef
	States []State
	Spec   Value
}

type Runtime struct {
	Actors        []*Actor
	Mailboxes     map[string][]Value
	MailboxCaps   map[string]int
	SyncInbox     map[string]Value
	Trace         []string
	Events        []Event
	Step          int
	DiceValue     float64
	ChooseActorFn func(*Runtime) int
	Dice          func() float64
}

type StepResult struct {
	Applied        bool
	ActorName      string
	StateName      string
	TransitionName string
}

type EventKind string

const (
	EventTransition EventKind = "transition"
	EventSend       EventKind = "send"
	EventReceive    EventKind = "receive"
)

type Event struct {
	Step           int
	Kind           EventKind
	ActorName      string
	StateName      string
	TransitionName string
	PeerName       string
	Message        Value
}

type MetricPoint struct {
	Step  int
	Value float64
}

type StatePredicate func(*Runtime) bool

type CTLOp uint8

const (
	CTLAtom CTLOp = iota
	CTLNot
	CTLAnd
	CTLOr
	CTLImplies
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

type MuOp uint8

const (
	MuFalse MuOp = iota
	MuTrue
	MuAtom
	MuVar
	MuNot
	MuAnd
	MuOr
	MuDiamond
	MuBox
	MuMu
	MuNu
)

type MuFormula struct {
	Op    MuOp
	Name  string
	Pred  StatePredicate
	Left  *MuFormula
	Right *MuFormula
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
		Dice: func() float64 {
			return rand.Float64()
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
	rt.rollDice()
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
		rt.Step++
		rt.logEvent(Event{
			Step:           rt.Step,
			Kind:           EventTransition,
			ActorName:      actor.Name,
			StateName:      state.Name,
			TransitionName: transition.Name,
		})
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

func (rt *Runtime) rollDice() {
	if rt.Dice == nil {
		rt.DiceValue = 0
		return
	}
	value := rt.Dice()
	switch {
	case value < 0:
		rt.DiceValue = 0
	case value > 1:
		rt.DiceValue = 1
	default:
		rt.DiceValue = value
	}
}

func (rt *Runtime) logEvent(event Event) {
	rt.Events = append(rt.Events, event)
}

func (rt *Runtime) EventCountSeries(kind EventKind, filter func(Event) bool) []MetricPoint {
	var out []MetricPoint
	count := 0.0
	for _, event := range rt.Events {
		if event.Kind != kind {
			continue
		}
		if filter != nil && !filter(event) {
			continue
		}
		count++
		out = append(out, MetricPoint{Step: event.Step, Value: count})
	}
	return out
}

func (rt *Runtime) EventRateSeries(kind EventKind, filter func(Event) bool, window int) []MetricPoint {
	if window <= 0 {
		window = 1
	}
	counts := map[int]float64{}
	maxStep := rt.Step
	for _, event := range rt.Events {
		if event.Kind != kind {
			continue
		}
		if filter != nil && !filter(event) {
			continue
		}
		counts[event.Step]++
	}
	var out []MetricPoint
	for step := 1; step <= maxStep; step++ {
		sum := 0.0
		start := step - window + 1
		if start < 1 {
			start = 1
		}
		for i := start; i <= step; i++ {
			sum += counts[i]
		}
		span := float64(step - start + 1)
		out = append(out, MetricPoint{Step: step, Value: sum / span})
	}
	return out
}

func (rt *Runtime) Clone() *Runtime {
	clone := &Runtime{
		Actors:      make([]*Actor, len(rt.Actors)),
		Mailboxes:   make(map[string][]Value, len(rt.Mailboxes)),
		MailboxCaps: make(map[string]int, len(rt.MailboxCaps)),
		SyncInbox:   cloneValueMap(rt.SyncInbox),
		Trace:       append([]string(nil), rt.Trace...),
		Events:      append([]Event(nil), rt.Events...),
		Step:        rt.Step,
		DiceValue:   rt.DiceValue,
		Dice:        rt.Dice,
	}
	for i, actor := range rt.Actors {
		cloneActor := &Actor{
			Name:   actor.Name,
			Data:   cloneValueMap(actor.Data),
			Defs:   cloneFunctionDefs(actor.Defs),
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

func CompileMu(src string) (MuFormula, error) {
	form, err := Read(src)
	if err != nil {
		return MuFormula{}, err
	}
	return buildMu(form)
}

func MustCompileMu(src string) MuFormula {
	formula, err := CompileMu(src)
	if err != nil {
		panic(err)
	}
	return formula
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

func Implies(left, right CTLFormula) CTLFormula {
	return CTLFormula{Op: CTLImplies, Left: &left, Right: &right}
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

func MuTrueFormula() MuFormula {
	return MuFormula{Op: MuTrue}
}

func MuFalseFormula() MuFormula {
	return MuFormula{Op: MuFalse}
}

func MuAtomFormula(pred StatePredicate) MuFormula {
	return MuFormula{Op: MuAtom, Pred: pred}
}

func MuVarFormula(name string) MuFormula {
	return MuFormula{Op: MuVar, Name: name}
}

func MuNotFormula(inner MuFormula) MuFormula {
	return MuFormula{Op: MuNot, Left: &inner}
}

func MuAndFormula(left, right MuFormula) MuFormula {
	return MuFormula{Op: MuAnd, Left: &left, Right: &right}
}

func MuOrFormula(left, right MuFormula) MuFormula {
	return MuFormula{Op: MuOr, Left: &left, Right: &right}
}

func MuDiamondFormula(inner MuFormula) MuFormula {
	return MuFormula{Op: MuDiamond, Left: &inner}
}

func MuBoxFormula(inner MuFormula) MuFormula {
	return MuFormula{Op: MuBox, Left: &inner}
}

func MuMuFormula(name string, body MuFormula) MuFormula {
	return MuFormula{Op: MuMu, Name: name, Left: &body}
}

func MuNuFormula(name string, body MuFormula) MuFormula {
	return MuFormula{Op: MuNu, Name: name, Left: &body}
}

func stripOptionalDescription(items []Value, minLen int) []Value {
	if len(items) > minLen && items[len(items)-1].Kind == KindString {
		return items[:len(items)-1]
	}
	return items
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
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return CTLFormula{}, fmt.Errorf("%s expects one operand", head)
		}
		inner, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return Not(inner), nil
	case "and":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return CTLFormula{}, fmt.Errorf("%s expects two operands", head)
		}
		left, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		right, err := buildCTL(items[2])
		if err != nil {
			return CTLFormula{}, err
		}
		return And(left, right), nil
	case "or":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return CTLFormula{}, fmt.Errorf("%s expects two operands", head)
		}
		left, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		right, err := buildCTL(items[2])
		if err != nil {
			return CTLFormula{}, err
		}
		return Or(left, right), nil
	case "implies", "->":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return CTLFormula{}, fmt.Errorf("%s expects two operands", head)
		}
		left, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		right, err := buildCTL(items[2])
		if err != nil {
			return CTLFormula{}, err
		}
		return Implies(left, right), nil
	case "ex":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return CTLFormula{}, fmt.Errorf("ex expects one operand")
		}
		inner, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return EX(inner), nil
	case "ax":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return CTLFormula{}, fmt.Errorf("ax expects one operand")
		}
		inner, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return AX(inner), nil
	case "ef":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return CTLFormula{}, fmt.Errorf("ef expects one operand")
		}
		inner, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return EF(inner), nil
	case "af":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return CTLFormula{}, fmt.Errorf("af expects one operand")
		}
		inner, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return AF(inner), nil
	case "eg":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return CTLFormula{}, fmt.Errorf("eg expects one operand")
		}
		inner, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return EG(inner), nil
	case "ag":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return CTLFormula{}, fmt.Errorf("ag expects one operand")
		}
		inner, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return AG(inner), nil
	case "eu":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return CTLFormula{}, fmt.Errorf("eu expects two operands")
		}
		left, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		right, err := buildCTL(items[2])
		if err != nil {
			return CTLFormula{}, err
		}
		return EU(left, right), nil
	case "au":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return CTLFormula{}, fmt.Errorf("au expects two operands")
		}
		left, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		right, err := buildCTL(items[2])
		if err != nil {
			return CTLFormula{}, err
		}
		return AU(left, right), nil
	case "in-state":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return CTLFormula{}, fmt.Errorf("in-state expects actor and state")
		}
		actor, err := expectSymbol(items[1], "actor name")
		if err != nil {
			return CTLFormula{}, err
		}
		state, err := expectSymbol(items[2], "state name")
		if err != nil {
			return CTLFormula{}, err
		}
		return Atom(ActorInState(actor, state)), nil
	case "data=":
		items := stripOptionalDescription(form.Items, 4)
		if len(items) != 4 {
			return CTLFormula{}, fmt.Errorf("data= expects actor, key, value")
		}
		actor, err := expectSymbol(items[1], "actor name")
		if err != nil {
			return CTLFormula{}, err
		}
		key, err := expectSymbol(items[2], "data key")
		if err != nil {
			return CTLFormula{}, err
		}
		return Atom(ActorDataEquals(actor, key, items[3])), nil
	case "mailbox-has":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return CTLFormula{}, fmt.Errorf("mailbox-has expects actor and message")
		}
		actor, err := expectSymbol(items[1], "actor name")
		if err != nil {
			return CTLFormula{}, err
		}
		return Atom(MailboxHas(actor, items[2])), nil
	default:
		return CTLFormula{}, fmt.Errorf("unsupported ctl operator %q", head)
	}
}

func buildMu(form Value) (MuFormula, error) {
	if form.Kind == KindSymbol {
		switch form.Text {
		case "true":
			return MuTrueFormula(), nil
		case "false":
			return MuFalseFormula(), nil
		default:
			return MuVarFormula(form.Text), nil
		}
	}
	if !isList(form) || len(form.Items) == 0 {
		return MuFormula{}, fmt.Errorf("mu formula must be a symbol or non-empty list")
	}

	head, err := expectSymbol(form.Items[0], "mu operator")
	if err != nil {
		return MuFormula{}, err
	}
	switch head {
	case "not":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return MuFormula{}, fmt.Errorf("not expects one operand")
		}
		inner, err := buildMu(items[1])
		if err != nil {
			return MuFormula{}, err
		}
		return MuNotFormula(inner), nil
	case "and":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return MuFormula{}, fmt.Errorf("and expects two operands")
		}
		left, err := buildMu(items[1])
		if err != nil {
			return MuFormula{}, err
		}
		right, err := buildMu(items[2])
		if err != nil {
			return MuFormula{}, err
		}
		return MuAndFormula(left, right), nil
	case "or":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return MuFormula{}, fmt.Errorf("or expects two operands")
		}
		left, err := buildMu(items[1])
		if err != nil {
			return MuFormula{}, err
		}
		right, err := buildMu(items[2])
		if err != nil {
			return MuFormula{}, err
		}
		return MuOrFormula(left, right), nil
	case "diamond":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return MuFormula{}, fmt.Errorf("diamond expects one operand")
		}
		inner, err := buildMu(items[1])
		if err != nil {
			return MuFormula{}, err
		}
		return MuDiamondFormula(inner), nil
	case "box":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return MuFormula{}, fmt.Errorf("box expects one operand")
		}
		inner, err := buildMu(items[1])
		if err != nil {
			return MuFormula{}, err
		}
		return MuBoxFormula(inner), nil
	case "mu":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return MuFormula{}, fmt.Errorf("mu expects a variable and body")
		}
		name, err := expectSymbol(items[1], "mu variable")
		if err != nil {
			return MuFormula{}, err
		}
		body, err := buildMu(items[2])
		if err != nil {
			return MuFormula{}, err
		}
		return MuMuFormula(name, body), nil
	case "nu":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return MuFormula{}, fmt.Errorf("nu expects a variable and body")
		}
		name, err := expectSymbol(items[1], "nu variable")
		if err != nil {
			return MuFormula{}, err
		}
		body, err := buildMu(items[2])
		if err != nil {
			return MuFormula{}, err
		}
		return MuNuFormula(name, body), nil
	case "in-state":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return MuFormula{}, fmt.Errorf("in-state expects actor and state")
		}
		actor, err := expectSymbol(items[1], "actor name")
		if err != nil {
			return MuFormula{}, err
		}
		state, err := expectSymbol(items[2], "state name")
		if err != nil {
			return MuFormula{}, err
		}
		return MuAtomFormula(ActorInState(actor, state)), nil
	case "data=":
		items := stripOptionalDescription(form.Items, 4)
		if len(items) != 4 {
			return MuFormula{}, fmt.Errorf("data= expects actor, key, value")
		}
		actor, err := expectSymbol(items[1], "actor name")
		if err != nil {
			return MuFormula{}, err
		}
		key, err := expectSymbol(items[2], "data key")
		if err != nil {
			return MuFormula{}, err
		}
		return MuAtomFormula(ActorDataEquals(actor, key, items[3])), nil
	case "mailbox-has":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return MuFormula{}, fmt.Errorf("mailbox-has expects actor and message")
		}
		actor, err := expectSymbol(items[1], "actor name")
		if err != nil {
			return MuFormula{}, err
		}
		return MuAtomFormula(MailboxHas(actor, items[2])), nil
	default:
		return MuFormula{}, fmt.Errorf("unsupported mu operator %q", head)
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
	return m.SatisfyingMuStates(lowerCTL(formula))
}

func (m *Model) HoldsMuAtInitial(formula MuFormula) bool {
	return m.HoldsMu(m.InitialID, formula)
}

func (m *Model) HoldsMu(stateID string, formula MuFormula) bool {
	set := m.SatisfyingMuStates(formula)
	return set[stateID]
}

func (m *Model) SatisfyingMuStates(formula MuFormula) map[string]bool {
	return m.satisfyingMuStates(formula, map[string]map[string]bool{})
}

func (m *Model) satisfyingMuStates(formula MuFormula, env map[string]map[string]bool) map[string]bool {
	switch formula.Op {
	case MuFalse:
		return map[string]bool{}
	case MuTrue:
		out := map[string]bool{}
		for id := range m.States {
			out[id] = true
		}
		return out
	case MuAtom:
		out := map[string]bool{}
		for id, state := range m.States {
			if formula.Pred != nil && formula.Pred(state.Runtime) {
				out[id] = true
			}
		}
		return out
	case MuVar:
		return copyStateSet(env[formula.Name])
	case MuNot:
		inner := m.satisfyingMuStates(*formula.Left, env)
		out := map[string]bool{}
		for id := range m.States {
			if !inner[id] {
				out[id] = true
			}
		}
		return out
	case MuAnd:
		left := m.satisfyingMuStates(*formula.Left, env)
		right := m.satisfyingMuStates(*formula.Right, env)
		out := map[string]bool{}
		for id := range m.States {
			if left[id] && right[id] {
				out[id] = true
			}
		}
		return out
	case MuOr:
		left := m.satisfyingMuStates(*formula.Left, env)
		right := m.satisfyingMuStates(*formula.Right, env)
		out := map[string]bool{}
		for id := range m.States {
			if left[id] || right[id] {
				out[id] = true
			}
		}
		return out
	case MuDiamond:
		inner := m.satisfyingMuStates(*formula.Left, env)
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
	case MuBox:
		inner := m.satisfyingMuStates(*formula.Left, env)
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
	case MuMu:
		current := map[string]bool{}
		for {
			nextEnv := cloneStateEnv(env)
			nextEnv[formula.Name] = copyStateSet(current)
			next := m.satisfyingMuStates(*formula.Left, nextEnv)
			if stateSetsEqual(current, next) {
				return next
			}
			current = next
		}
	case MuNu:
		current := map[string]bool{}
		for id := range m.States {
			current[id] = true
		}
		for {
			nextEnv := cloneStateEnv(env)
			nextEnv[formula.Name] = copyStateSet(current)
			next := m.satisfyingMuStates(*formula.Left, nextEnv)
			if stateSetsEqual(current, next) {
				return next
			}
			current = next
		}
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
		resolved := evalValue(actor, message)
		if rt.mailboxCap(to) == 0 {
			if err := rt.rendezvous(to, resolved); err != nil {
				return err
			}
			rt.logEvent(Event{
				Step:      rt.Step,
				Kind:      EventSend,
				ActorName: actor.Name,
				PeerName:  to,
				Message:   cloneValue(resolved),
			})
			rt.Tracef("%s -> %s %s", actor.Name, to, resolved.String())
			return nil
		}
		if cap := rt.mailboxCap(to); cap >= 0 && len(rt.Mailbox(to)) >= cap {
			return fmt.Errorf("mailbox %s is full", to)
		}
		rt.Enqueue(to, resolved)
		rt.logEvent(Event{
			Step:      rt.Step,
			Kind:      EventSend,
			ActorName: actor.Name,
			PeerName:  to,
			Message:   cloneValue(resolved),
		})
		rt.Tracef("%s -> %s %s", actor.Name, to, resolved.String())
		return nil
	}
}

func Receive(match MessageGuardFunc, handler MessageHandlerFunc) ActionFunc {
	return func(rt *Runtime, actor *Actor) error {
		if offered, ok := rt.SyncInbox[actor.Name]; ok {
			if match == nil || match(offered) {
				delete(rt.SyncInbox, actor.Name)
				rt.logEvent(Event{
					Step:      rt.Step,
					Kind:      EventReceive,
					ActorName: actor.Name,
					Message:   cloneValue(offered),
				})
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
		rt.logEvent(Event{
			Step:      rt.Step,
			Kind:      EventReceive,
			ActorName: actor.Name,
			Message:   cloneValue(message),
		})
		rt.Tracef("%s <= %s", actor.Name, message.String())
		if handler == nil {
			return nil
		}
		return handler(rt, actor, message)
	}
}

func ReceiveInto(name string) ActionFunc {
	return Receive(nil, func(_ *Runtime, actor *Actor, message Value) error {
		actor.Data[name] = cloneValue(message)
		return nil
	})
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
		if offered != nil {
			return true
		}
		return len(rt.Mailbox(actor.Name)) > 0
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
		rt.logEvent(Event{
			Step:           rt.Step,
			Kind:           EventTransition,
			ActorName:      targetActor.Name,
			StateName:      state.Name,
			TransitionName: transition.Name,
		})
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
			return rt.DiceValue < 0.5, nil
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
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return false, fmt.Errorf("mailbox guard must be (mailbox message)")
		}
		want := items[1]
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
		items := stripOptionalDescription(form.Items, 3)
		for _, item := range items[1:] {
			ok, err := rt.evalGuardSpec(item, actor, offered)
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	case "or":
		items := stripOptionalDescription(form.Items, 3)
		for _, item := range items[1:] {
			ok, err := rt.evalGuardSpec(item, actor, offered)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	case "not":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return false, fmt.Errorf("%s guard needs one operand", head)
		}
		ok, err := rt.evalGuardSpec(items[1], actor, offered)
		if err != nil {
			return false, err
		}
		return !ok, nil
	case "implies", "->":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return false, fmt.Errorf("%s guard needs two operands", head)
		}
		left, err := rt.evalGuardSpec(items[1], actor, offered)
		if err != nil {
			return false, err
		}
		if !left {
			return true, nil
		}
		return rt.evalGuardSpec(items[2], actor, offered)
	case "dice-range":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return false, fmt.Errorf("dice-range guard must be (dice-range low high)")
		}
		if rt.Dice == nil {
			return true, nil
		}
		low, err := valueFloat(items[1])
		if err != nil {
			return false, err
		}
		high, err := valueFloat(items[2])
		if err != nil {
			return false, err
		}
		return rt.DiceValue >= low && rt.DiceValue <= high, nil
	case "dice<":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return false, fmt.Errorf("dice< guard must be (dice< high)")
		}
		if rt.Dice == nil {
			return true, nil
		}
		high, err := valueFloat(items[1])
		if err != nil {
			return false, err
		}
		return rt.DiceValue < high, nil
	case "dice>=":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return false, fmt.Errorf("dice>= guard must be (dice>= low)")
		}
		if rt.Dice == nil {
			return true, nil
		}
		low, err := valueFloat(items[1])
		if err != nil {
			return false, err
		}
		return rt.DiceValue >= low, nil
	case "data=":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return false, fmt.Errorf("data= guard must be (data= key value)")
		}
		key, err := expectSymbol(items[1], "data key")
		if err != nil {
			return false, err
		}
		return actor.Data[key].Equal(items[2]), nil
	case "data>":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return false, fmt.Errorf("data> guard must be (data> key value)")
		}
		key, err := expectSymbol(items[1], "data key")
		if err != nil {
			return false, err
		}
		got, err := valueInt(actor.Data[key])
		if err != nil {
			return false, err
		}
		want, err := valueInt(items[2])
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
		Defs: map[string]FunctionDef{},
		Spec: form,
	}
	for _, item := range form.Items[2:] {
		states, err := buildState(item)
		if err != nil {
			return nil, fmt.Errorf("actor %s: %w", name, err)
		}
		actor.States = append(actor.States, states...)
	}
	if len(actor.States) == 0 {
		return nil, fmt.Errorf("actor %s: no states", name)
	}
	actor.Data["state"] = Symbol(actor.States[0].Name)
	return actor, nil
}

func buildState(form Value) ([]State, error) {
	if !isListHead(form, "state") || len(form.Items) < 2 {
		return nil, fmt.Errorf("state form must be (state name ...)")
	}
	name, err := expectSymbol(form.Items[1], "state name")
	if err != nil {
		return nil, err
	}

	state := State{
		Name:    name,
		Control: true,
		Spec:    form,
	}
	var generated []State
	waitCounter := 0
	for _, item := range form.Items[2:] {
		transitions, hiddenStates, normalized, err := buildTransitionChain(item, name, &waitCounter)
		if err != nil {
			return nil, fmt.Errorf("state %s: %w", name, err)
		}
		if normalized {
			state.Spec = Value{}
		}
		state.Transitions = append(state.Transitions, transitions...)
		generated = append(generated, hiddenStates...)
	}
	return append([]State{state}, generated...), nil
}

func buildTransition(form Value) (Transition, error) {
	if !isListHead(form, "edge") || len(form.Items) < 3 {
		return Transition{}, fmt.Errorf("transition form must be (edge guard action...)")
	}
	guard, err := compileGuard(form.Items[1])
	if err != nil {
		return Transition{}, err
	}
	actionSpec := seqForm(form.Items[2:])
	nextStates, err := collectBecomeStates(actionSpec)
	if err != nil {
		return Transition{}, err
	}
	action, err := compileAction(actionSpec)
	if err != nil {
		return Transition{}, err
	}
	return Transition{
		Name:       form.Items[1].String(),
		Guard:      guard,
		Action:     action,
		NextStates: nextStates,
		GuardSpec:  form.Items[1],
		ActionSpec: actionSpec,
	}, nil
}

func buildTransitionFromParts(guardSpec, actionSpec Value) (Transition, error) {
	form := List(Symbol("edge"), guardSpec)
	if isListHead(actionSpec, "do") {
		form.Items = append(form.Items, actionSpec.Items[1:]...)
	} else {
		form.Items = append(form.Items, actionSpec)
	}
	return buildTransition(form)
}

func buildTransitionChain(form Value, stateName string, waitCounter *int) ([]Transition, []State, bool, error) {
	base, err := buildTransition(form)
	if err != nil {
		return nil, nil, false, err
	}
	items := actionItems(base.ActionSpec)
	commIdxs := communicationIndices(items)
	if len(commIdxs) == 0 || (len(commIdxs) == 1 && commIdxs[0] == 0) {
		return []Transition{base}, nil, false, nil
	}

	var transitions []Transition
	var hiddenStates []State
	currentState := stateName
	currentGuard := base.GuardSpec
	start := 0
	for i, idx := range commIdxs {
		if idx > start {
			waitName := generatedWaitName(stateName, *waitCounter)
			*waitCounter++
			preItems := append([]Value{}, items[start:idx]...)
			preItems = append(preItems, List(Symbol("become"), Symbol(waitName)))
			preAction := seqForm(preItems)
			preTransition, err := buildTransitionFromParts(currentGuard, preAction)
			if err != nil {
				return nil, nil, false, err
			}
			if currentState == stateName {
				transitions = append(transitions, preTransition)
			} else {
				hiddenStates = append(hiddenStates, State{
					Name:        currentState,
					Control:     true,
					Transitions: []Transition{preTransition},
				})
			}
			currentState = waitName
			currentGuard = Symbol("true")
		}

		end := len(items)
		if i+1 < len(commIdxs) {
			end = commIdxs[i+1]
		}
		segmentItems := append([]Value{}, items[idx:end]...)
		if i+1 < len(commIdxs) {
			waitName := generatedWaitName(stateName, *waitCounter)
			*waitCounter++
			segmentItems = append(segmentItems, List(Symbol("become"), Symbol(waitName)))
			segmentAction := seqForm(segmentItems)
			segmentTransition, err := buildTransitionFromParts(currentGuard, segmentAction)
			if err != nil {
				return nil, nil, false, err
			}
			if currentState == stateName {
				transitions = append(transitions, segmentTransition)
			} else {
				hiddenStates = append(hiddenStates, State{
					Name:        currentState,
					Control:     true,
					Transitions: []Transition{segmentTransition},
				})
			}
			currentState = waitName
			currentGuard = Symbol("true")
		} else {
			segmentAction := seqForm(segmentItems)
			segmentTransition, err := buildTransitionFromParts(currentGuard, segmentAction)
			if err != nil {
				return nil, nil, false, err
			}
			if currentState == stateName {
				transitions = append(transitions, segmentTransition)
			} else {
				hiddenStates = append(hiddenStates, State{
					Name:        currentState,
					Control:     true,
					Transitions: []Transition{segmentTransition},
				})
			}
		}
		start = end
	}
	return transitions, hiddenStates, true, nil
}

func communicationIndices(items []Value) []int {
	var out []int
	for i, item := range items {
		if isSendOrRecvForm(item) {
			out = append(out, i)
		}
	}
	return out
}

func generatedWaitName(stateName string, idx int) string {
	return fmt.Sprintf("%s__wait_%d", stateName, idx)
}

func collectBecomeStates(form Value) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	if err := walkBecomeStates(form, &out, seen); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("edge must contain at least one become")
	}
	return out, nil
}

func walkBecomeStates(form Value, out *[]string, seen map[string]bool) error {
	if !isList(form) || len(form.Items) == 0 {
		return nil
	}
	head, err := expectSymbol(form.Items[0], "action operator")
	if err != nil {
		return err
	}
	switch head {
	case "do":
		for _, item := range form.Items[1:] {
			if err := walkBecomeStates(item, out, seen); err != nil {
				return err
			}
		}
	case "if":
		for _, item := range form.Items[2:] {
			if err := walkBecomeStates(item, out, seen); err != nil {
				return err
			}
		}
	case "become":
		if len(form.Items) != 2 {
			return fmt.Errorf("become must be (become state)")
		}
		name, err := expectSymbol(form.Items[1], "state name")
		if err != nil {
			return err
		}
		if !seen[name] {
			*out = append(*out, name)
			seen[name] = true
		}
	}
	return nil
}

func validateTransitionActionSpec(form Value) error {
	items := actionItems(form)
	if len(items) == 0 {
		return nil
	}
	if isSendOrRecvForm(items[0]) {
		for _, item := range items[1:] {
			if err := validateNoNestedSendRecv(item); err != nil {
				return err
			}
		}
		return nil
	}
	for _, item := range items {
		if err := validateNoNestedSendRecv(item); err != nil {
			return err
		}
	}
	return nil
}

func actionItems(form Value) []Value {
	if isListHead(form, "do") {
		return form.Items[1:]
	}
	if !isList(form) || len(form.Items) == 0 {
		return nil
	}
	return []Value{form}
}

func validateNoNestedSendRecv(form Value) error {
	if !isList(form) || len(form.Items) == 0 {
		return nil
	}
	head, err := expectSymbol(form.Items[0], "action operator")
	if err != nil {
		return err
	}
	switch head {
	case "send", "recv":
		return fmt.Errorf("%s must be the first action after the edge condition", head)
	case "do":
		for _, item := range form.Items[1:] {
			if err := validateNoNestedSendRecv(item); err != nil {
				return err
			}
		}
	case "if":
		for _, item := range form.Items[2:] {
			if err := validateNoNestedSendRecv(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func isSendOrRecvForm(form Value) bool {
	if !isList(form) || len(form.Items) == 0 {
		return false
	}
	head, err := expectSymbol(form.Items[0], "action operator")
	if err != nil {
		return false
	}
	return head == "send" || head == "recv"
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
				return rt.DiceValue < 0.5
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
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return nil, fmt.Errorf("mailbox guard must be (mailbox message)")
		}
		want := items[1]
		return func(rt *Runtime, actor *Actor) bool {
			for _, message := range rt.Mailbox(actor.Name) {
				if message.Equal(want) {
					return true
				}
			}
			return false
		}, nil
	case "and":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) < 3 {
			return nil, fmt.Errorf("%s guard needs at least two operands", head)
		}
		var guards []GuardFunc
		for _, item := range items[1:] {
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
	case "or":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) < 3 {
			return nil, fmt.Errorf("%s guard needs at least two operands", head)
		}
		var guards []GuardFunc
		for _, item := range items[1:] {
			guard, err := compileGuard(item)
			if err != nil {
				return nil, err
			}
			guards = append(guards, guard)
		}
		return func(rt *Runtime, actor *Actor) bool {
			for _, guard := range guards {
				if guard(rt, actor) {
					return true
				}
			}
			return false
		}, nil
	case "not":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return nil, fmt.Errorf("%s guard needs one operand", head)
		}
		inner, err := compileGuard(items[1])
		if err != nil {
			return nil, err
		}
		return func(rt *Runtime, actor *Actor) bool {
			return !inner(rt, actor)
		}, nil
	case "implies", "->":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return nil, fmt.Errorf("%s guard needs two operands", head)
		}
		left, err := compileGuard(items[1])
		if err != nil {
			return nil, err
		}
		right, err := compileGuard(items[2])
		if err != nil {
			return nil, err
		}
		return func(rt *Runtime, actor *Actor) bool {
			return !left(rt, actor) || right(rt, actor)
		}, nil
	case "dice-range":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return nil, fmt.Errorf("dice-range guard must be (dice-range low high)")
		}
		low, err := valueFloat(items[1])
		if err != nil {
			return nil, err
		}
		high, err := valueFloat(items[2])
		if err != nil {
			return nil, err
		}
		return func(rt *Runtime, _ *Actor) bool {
			if rt.Dice == nil {
				return true
			}
			return rt.DiceValue >= low && rt.DiceValue <= high
		}, nil
	case "dice<":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return nil, fmt.Errorf("dice< guard must be (dice< high)")
		}
		high, err := valueFloat(items[1])
		if err != nil {
			return nil, err
		}
		return func(rt *Runtime, _ *Actor) bool {
			if rt.Dice == nil {
				return true
			}
			return rt.DiceValue < high
		}, nil
	case "dice>=":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return nil, fmt.Errorf("dice>= guard must be (dice>= low)")
		}
		low, err := valueFloat(items[1])
		if err != nil {
			return nil, err
		}
		return func(rt *Runtime, _ *Actor) bool {
			if rt.Dice == nil {
				return true
			}
			return rt.DiceValue >= low
		}, nil
	case "data=":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return nil, fmt.Errorf("data= guard must be (data= key value)")
		}
		key, err := expectSymbol(items[1], "data key")
		if err != nil {
			return nil, err
		}
		want := items[2]
		return func(_ *Runtime, actor *Actor) bool {
			return actor.Data[key].Equal(want)
		}, nil
	case "data>":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return nil, fmt.Errorf("data> guard must be (data> key value)")
		}
		key, err := expectSymbol(items[1], "data key")
		if err != nil {
			return nil, err
		}
		want, err := valueInt(items[2])
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
			return nil, fmt.Errorf("recv must be (recv variable)")
		}
		name, err := expectSymbol(form.Items[1], "recv variable")
		if err != nil {
			return nil, err
		}
		return ReceiveInto(name), nil
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
			actor.Data[key] = evalValue(actor, value)
			return nil
		}, nil
	case "def":
		if len(form.Items) != 4 {
			return nil, fmt.Errorf("def must be (def name (params...) body)")
		}
		name, err := expectSymbol(form.Items[1], "function name")
		if err != nil {
			return nil, err
		}
		paramsForm := form.Items[2]
		if !isList(paramsForm) {
			return nil, fmt.Errorf("def params must be a list")
		}
		params := make([]string, 0, len(paramsForm.Items))
		for _, item := range paramsForm.Items {
			param, err := expectSymbol(item, "parameter name")
			if err != nil {
				return nil, err
			}
			params = append(params, param)
		}
		body := cloneValue(form.Items[3])
		return func(_ *Runtime, actor *Actor) error {
			if actor.Defs == nil {
				actor.Defs = map[string]FunctionDef{}
			}
			actor.Defs[name] = FunctionDef{
				Params: append([]string(nil), params...),
				Body:   cloneValue(body),
			}
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
		deltaForm := form.Items[2]
		return func(_ *Runtime, actor *Actor) error {
			current, err := valueInt(actor.Data[key])
			if err != nil {
				return err
			}
			delta, err := valueInt(evalValue(actor, deltaForm))
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
		deltaForm := form.Items[2]
		return func(_ *Runtime, actor *Actor) error {
			current, err := valueInt(actor.Data[key])
			if err != nil {
				return err
			}
			delta, err := valueInt(evalValue(actor, deltaForm))
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
			value := evalValue(actor, source)
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
			n, err := valueBigInt(evalValue(actor, modulus))
			if err != nil {
				return err
			}
			e, err := valueBigInt(evalValue(actor, exponent))
			if err != nil {
				return err
			}
			m, err := valueBigInt(evalValue(actor, message))
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
	case "sample-exponential":
		if len(form.Items) != 3 {
			return nil, fmt.Errorf("sample-exponential must be (sample-exponential out rate)")
		}
		out, err := expectSymbol(form.Items[1], "sample-exponential out key")
		if err != nil {
			return nil, err
		}
		rate, err := valueFloat(form.Items[2])
		if err != nil {
			return nil, err
		}
		if rate <= 0 {
			return nil, fmt.Errorf("sample-exponential rate must be positive")
		}
		return func(rt *Runtime, actor *Actor) error {
			u := rt.DiceValue
			if u <= 0 {
				u = 1e-12
			}
			if u >= 1 {
				u = 1 - 1e-12
			}
			sample := -math.Log(1-u) / rate
			actor.Data[out] = Number(strconv.FormatFloat(sample, 'g', -1, 64))
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

func cloneStateEnv(in map[string]map[string]bool) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(in))
	for key, value := range in {
		out[key] = copyStateSet(value)
	}
	return out
}

func stateSetsEqual(left, right map[string]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		if right[key] != leftValue {
			return false
		}
	}
	return true
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

func cloneFunctionDefs(in map[string]FunctionDef) map[string]FunctionDef {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]FunctionDef, len(in))
	for key, def := range in {
		out[key] = FunctionDef{
			Params: append([]string(nil), def.Params...),
			Body:   cloneValue(def.Body),
		}
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

func valueFloat(v Value) (float64, error) {
	if v.Kind != KindNumber {
		return 0, fmt.Errorf("value %s is not a number", v.String())
	}
	return strconv.ParseFloat(v.Text, 64)
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

func evalValue(actor *Actor, operand Value) Value {
	return evalValueWithEnv(actor, operand, nil)
}

func evalValueWithEnv(actor *Actor, operand Value, env map[string]Value) Value {
	switch operand.Kind {
	case KindSymbol:
		if env != nil {
			if value, ok := env[operand.Text]; ok {
				return cloneValue(value)
			}
		}
		if value, ok := actor.Data[operand.Text]; ok {
			return cloneValue(value)
		}
		return cloneValue(operand)
	case KindList:
		if len(operand.Items) == 0 {
			return List()
		}
		head := operand.Items[0]
		if head.Kind == KindSymbol {
			switch head.Text {
			case "quote":
				if len(operand.Items) == 2 {
					return cloneValue(operand.Items[1])
				}
				return cloneValue(operand)
			case "cons":
				if len(operand.Items) != 3 {
					return cloneValue(operand)
				}
				first := evalValueWithEnv(actor, operand.Items[1], env)
				rest := evalValueWithEnv(actor, operand.Items[2], env)
				if rest.Kind == KindList {
					items := make([]Value, 0, len(rest.Items)+1)
					items = append(items, first)
					items = append(items, cloneValueSlice(rest.Items)...)
					return List(items...)
				}
				return List(first, rest)
			case "car":
				if len(operand.Items) != 2 {
					return cloneValue(operand)
				}
				value := evalValueWithEnv(actor, operand.Items[1], env)
				if value.Kind == KindList && len(value.Items) > 0 {
					return cloneValue(value.Items[0])
				}
				return Value{}
			case "cdr":
				if len(operand.Items) != 2 {
					return cloneValue(operand)
				}
				value := evalValueWithEnv(actor, operand.Items[1], env)
				if value.Kind == KindList && len(value.Items) > 0 {
					return List(cloneValueSlice(value.Items[1:])...)
				}
				return List()
			default:
				if actor.Defs != nil {
					if def, ok := actor.Defs[head.Text]; ok {
						if len(operand.Items)-1 != len(def.Params) {
							return cloneValue(operand)
						}
						callEnv := make(map[string]Value, len(def.Params))
						for i, param := range def.Params {
							callEnv[param] = evalValueWithEnv(actor, operand.Items[i+1], env)
						}
						return evalValueWithEnv(actor, def.Body, callEnv)
					}
				}
			}
		}
		items := make([]Value, len(operand.Items))
		for i, item := range operand.Items {
			items[i] = evalValueWithEnv(actor, item, env)
		}
		return List(items...)
	default:
		return cloneValue(operand)
	}
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

func (f CTLFormula) String() string {
	switch f.Op {
	case CTLAtom:
		return "atom"
	case CTLNot:
		return fmt.Sprintf("(not %s)", f.Left.String())
	case CTLAnd:
		return fmt.Sprintf("(and %s %s)", f.Left.String(), f.Right.String())
	case CTLOr:
		return fmt.Sprintf("(or %s %s)", f.Left.String(), f.Right.String())
	case CTLImplies:
		return fmt.Sprintf("(implies %s %s)", f.Left.String(), f.Right.String())
	case CTLEX:
		return fmt.Sprintf("(EX %s)", f.Left.String())
	case CTLAX:
		return fmt.Sprintf("(AX %s)", f.Left.String())
	case CTLEF:
		return fmt.Sprintf("(EF %s)", f.Left.String())
	case CTLAF:
		return fmt.Sprintf("(AF %s)", f.Left.String())
	case CTLEG:
		return fmt.Sprintf("(EG %s)", f.Left.String())
	case CTLAG:
		return fmt.Sprintf("(AG %s)", f.Left.String())
	case CTLEU:
		return fmt.Sprintf("(EU %s %s)", f.Left.String(), f.Right.String())
	case CTLAU:
		return fmt.Sprintf("(AU %s %s)", f.Left.String(), f.Right.String())
	default:
		return "<invalid-ctl>"
	}
}

func (f MuFormula) String() string {
	switch f.Op {
	case MuFalse:
		return "false"
	case MuTrue:
		return "true"
	case MuAtom:
		return "atom"
	case MuVar:
		return f.Name
	case MuNot:
		return fmt.Sprintf("(not %s)", f.Left.String())
	case MuAnd:
		return fmt.Sprintf("(and %s %s)", f.Left.String(), f.Right.String())
	case MuOr:
		return fmt.Sprintf("(or %s %s)", f.Left.String(), f.Right.String())
	case MuDiamond:
		return fmt.Sprintf("(diamond %s)", f.Left.String())
	case MuBox:
		return fmt.Sprintf("(box %s)", f.Left.String())
	case MuMu:
		return fmt.Sprintf("(mu %s %s)", f.Name, f.Left.String())
	case MuNu:
		return fmt.Sprintf("(nu %s %s)", f.Name, f.Left.String())
	default:
		return "<invalid-mu>"
	}
}

func lowerCTL(formula CTLFormula) MuFormula {
	return lowerCTLWithFresh(formula, 0).formula
}

type lowerResult struct {
	formula MuFormula
	nextID  int
}

func lowerCTLWithFresh(formula CTLFormula, nextID int) lowerResult {
	switch formula.Op {
	case CTLAtom:
		return lowerResult{formula: MuAtomFormula(formula.Pred), nextID: nextID}
	case CTLNot:
		inner := lowerCTLWithFresh(*formula.Left, nextID)
		return lowerResult{formula: MuNotFormula(inner.formula), nextID: inner.nextID}
	case CTLAnd:
		left := lowerCTLWithFresh(*formula.Left, nextID)
		right := lowerCTLWithFresh(*formula.Right, left.nextID)
		return lowerResult{formula: MuAndFormula(left.formula, right.formula), nextID: right.nextID}
	case CTLOr:
		left := lowerCTLWithFresh(*formula.Left, nextID)
		right := lowerCTLWithFresh(*formula.Right, left.nextID)
		return lowerResult{formula: MuOrFormula(left.formula, right.formula), nextID: right.nextID}
	case CTLImplies:
		left := lowerCTLWithFresh(*formula.Left, nextID)
		right := lowerCTLWithFresh(*formula.Right, left.nextID)
		return lowerResult{formula: MuOrFormula(MuNotFormula(left.formula), right.formula), nextID: right.nextID}
	case CTLEX:
		inner := lowerCTLWithFresh(*formula.Left, nextID)
		return lowerResult{formula: MuDiamondFormula(inner.formula), nextID: inner.nextID}
	case CTLAX:
		inner := lowerCTLWithFresh(*formula.Left, nextID)
		return lowerResult{formula: MuBoxFormula(inner.formula), nextID: inner.nextID}
	case CTLEF:
		name := freshMuVar(nextID)
		inner := lowerCTLWithFresh(*formula.Left, nextID+1)
		body := MuOrFormula(inner.formula, MuDiamondFormula(MuVarFormula(name)))
		return lowerResult{formula: MuMuFormula(name, body), nextID: inner.nextID}
	case CTLAF:
		name := freshMuVar(nextID)
		inner := lowerCTLWithFresh(*formula.Left, nextID+1)
		body := MuOrFormula(inner.formula, MuBoxFormula(MuVarFormula(name)))
		return lowerResult{formula: MuMuFormula(name, body), nextID: inner.nextID}
	case CTLEG:
		name := freshMuVar(nextID)
		inner := lowerCTLWithFresh(*formula.Left, nextID+1)
		body := MuAndFormula(inner.formula, MuDiamondFormula(MuVarFormula(name)))
		return lowerResult{formula: MuNuFormula(name, body), nextID: inner.nextID}
	case CTLAG:
		name := freshMuVar(nextID)
		inner := lowerCTLWithFresh(*formula.Left, nextID+1)
		body := MuAndFormula(inner.formula, MuBoxFormula(MuVarFormula(name)))
		return lowerResult{formula: MuNuFormula(name, body), nextID: inner.nextID}
	case CTLEU:
		name := freshMuVar(nextID)
		left := lowerCTLWithFresh(*formula.Left, nextID+1)
		right := lowerCTLWithFresh(*formula.Right, left.nextID)
		body := MuOrFormula(right.formula, MuAndFormula(left.formula, MuDiamondFormula(MuVarFormula(name))))
		return lowerResult{formula: MuMuFormula(name, body), nextID: right.nextID}
	case CTLAU:
		name := freshMuVar(nextID)
		left := lowerCTLWithFresh(*formula.Left, nextID+1)
		right := lowerCTLWithFresh(*formula.Right, left.nextID)
		body := MuOrFormula(right.formula, MuAndFormula(left.formula, MuBoxFormula(MuVarFormula(name))))
		return lowerResult{formula: MuMuFormula(name, body), nextID: right.nextID}
	default:
		return lowerResult{formula: MuFalseFormula(), nextID: nextID}
	}
}

func freshMuVar(id int) string {
	return fmt.Sprintf("$X%d", id)
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
	if t.GuardSpec.Kind != KindInvalid && t.ActionSpec.Kind != KindInvalid {
		items := []Value{Symbol("edge"), t.GuardSpec}
		if isListHead(t.ActionSpec, "do") {
			items = append(items, t.ActionSpec.Items[1:]...)
		} else {
			items = append(items, t.ActionSpec)
		}
		return List(items...)
	}
	items := []Value{Symbol("edge"), Symbol(t.Name)}
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
	runes := []rune(input)

	for i := 0; i < len(runes); {
		ch := runes[i]

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
			for i < len(runes) && runes[i] != '"' {
				i++
			}
			if i >= len(runes) {
				return nil, fmt.Errorf("unterminated string literal")
			}
			out = append(out, token{
				kind:      "string",
				text:      string(runes[start:i]),
				tightLeft: start-1 == lastEnd,
			})
			i++
			lastEnd = i
			continue
		}

		if unicode.IsDigit(ch) || (ch == '-' && i+1 < len(runes) && unicode.IsDigit(runes[i+1])) {
			start := i
			i++
			for i < len(runes) && unicode.IsDigit(runes[i]) {
				i++
			}
			if i < len(runes)-1 && runes[i] == '.' && unicode.IsDigit(runes[i+1]) {
				i++
				for i < len(runes) && unicode.IsDigit(runes[i]) {
					i++
				}
			}
			out = append(out, token{
				kind:      "number",
				text:      string(runes[start:i]),
				tightLeft: start == lastEnd,
			})
			lastEnd = i
			continue
		}

		if isSymbolStart(ch) {
			start := i
			i++
			for i < len(runes) && isSymbolPart(runes[i]) {
				i++
			}
			text := string(runes[start:i])
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
