package main

import (
	"crypto/md5"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math"
	"math/big"
	"math/rand"
	"os"
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
type MessageHandlerFunc func(*Runtime, *Actor, Value, string) error

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
	Name         string
	Role         string
	RoleBindings map[string][]string
	Data         map[string]Value
	Defs         map[string]FunctionDef
	States       []State
	Spec         Value
}

type Runtime struct {
	Actors         []*Actor
	Mailboxes      map[string][]Value
	MailboxSenders map[string][]string
	MailboxCaps    map[string]int
	SyncInbox      map[string]Value
	SyncSender     map[string]string
	Trace          []string
	Events         []Event
	Step           int
	DiceValue      float64
	ChooseActorFn  func(*Runtime) int
	Dice           func() float64
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

type Assertion struct {
	Formula CTLFormula
	Spec    Value
}

type AssertionResult struct {
	Assertion Assertion
	Holds     bool
}

type RequirementsModel struct {
	ActorTypes   []*Actor
	Actors       []*Actor
	Declarations []ActorDeclaration
	Assertions   []Assertion
	Plots        []XYPlot
	Spec         Value
}

type ActorDeclaration struct {
	Name         string
	Role         string
	QueueLen     int
	RoleBindings map[string][]string
	Spec         Value
}

type XYPlot struct {
	Name   string
	Title  string
	Steps  int
	Metric string
	Spec   Value
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
		Actors:         actors,
		Mailboxes:      make(map[string][]Value, len(actors)),
		MailboxSenders: make(map[string][]string, len(actors)),
		MailboxCaps:    make(map[string]int, len(actors)),
		SyncInbox:      make(map[string]Value, len(actors)),
		SyncSender:     make(map[string]string, len(actors)),
		Dice: func() float64 {
			return rand.Float64()
		},
	}
	for _, actor := range actors {
		rt.Mailboxes[actor.Name] = nil
		rt.MailboxSenders[actor.Name] = nil
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

func (rt *Runtime) mailboxSenders(name string) []string {
	return rt.MailboxSenders[name]
}

func (rt *Runtime) mailboxCap(name string) int {
	cap, ok := rt.MailboxCaps[name]
	if !ok {
		return -1
	}
	return cap
}

func (rt *Runtime) Enqueue(name string, message Value, sender string) {
	rt.Mailboxes[name] = append(rt.Mailboxes[name], message)
	rt.MailboxSenders[name] = append(rt.MailboxSenders[name], sender)
}

func (rt *Runtime) DequeueMatching(name string, guard MessageGuardFunc) (Value, string, bool) {
	mailbox := rt.Mailboxes[name]
	senders := rt.MailboxSenders[name]
	for i, message := range mailbox {
		if guard != nil && !guard(message) {
			continue
		}
		rt.Mailboxes[name] = append(mailbox[:i], mailbox[i+1:]...)
		sender := ""
		if i < len(senders) {
			sender = senders[i]
			rt.MailboxSenders[name] = append(senders[:i], senders[i+1:]...)
		}
		return message, sender, true
	}
	return Value{}, "", false
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

func (rt *Runtime) MailboxSizeSeries(actorName string) []MetricPoint {
	counts := map[int]float64{}
	for _, event := range rt.Events {
		switch event.Kind {
		case EventSend:
			if event.PeerName == actorName && rt.mailboxCap(actorName) != 0 {
				counts[event.Step]++
			}
		case EventReceive:
			if event.ActorName == actorName && rt.mailboxCap(actorName) != 0 {
				counts[event.Step]--
			}
		}
	}
	out := []MetricPoint{{Step: 0, Value: 0}}
	size := 0.0
	for step := 1; step <= rt.Step; step++ {
		size += counts[step]
		out = append(out, MetricPoint{Step: step, Value: size})
	}
	return out
}

func (rt *Runtime) Clone() *Runtime {
	clone := &Runtime{
		Actors:         make([]*Actor, len(rt.Actors)),
		Mailboxes:      make(map[string][]Value, len(rt.Mailboxes)),
		MailboxSenders: cloneStringSliceMap(rt.MailboxSenders),
		MailboxCaps:    make(map[string]int, len(rt.MailboxCaps)),
		SyncInbox:      cloneValueMap(rt.SyncInbox),
		SyncSender:     cloneStringMap(rt.SyncSender),
		Trace:          append([]string(nil), rt.Trace...),
		Events:         append([]Event(nil), rt.Events...),
		Step:           rt.Step,
		DiceValue:      rt.DiceValue,
		Dice:           rt.Dice,
	}
	for i, actor := range rt.Actors {
		cloneActor := &Actor{
			Name:         actor.Name,
			Role:         actor.Role,
			RoleBindings: cloneStringSliceMap(actor.RoleBindings),
			Data:         cloneValueMap(actor.Data),
			Defs:         cloneFunctionDefs(actor.Defs),
			States:       cloneStates(actor.States),
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

func cloneActor(actor *Actor) *Actor {
	return &Actor{
		Name:         actor.Name,
		Role:         actor.Role,
		RoleBindings: cloneStringSliceMap(actor.RoleBindings),
		Data:         cloneValueMap(actor.Data),
		Defs:         cloneFunctionDefs(actor.Defs),
		States:       cloneStates(actor.States),
		Spec:         cloneValue(actor.Spec),
	}
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
			if sender := mailboxSenderAt(rt.mailboxSenders(actor.Name), i); sender != "" {
				b.WriteString(sender)
				b.WriteString(">")
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

func adviceError(summary, advice string) error {
	return fmt.Errorf("%s. Do this: %s", summary, advice)
}

func syntaxError(name, syntax string) error {
	return adviceError(fmt.Sprintf("%s has the wrong shape", name), fmt.Sprintf("rewrite it as `%s`", syntax))
}

func operandError(name string, count int, example string) error {
	word := "operand"
	if count != 1 {
		word = "operands"
	}
	return adviceError(fmt.Sprintf("%s expects %d %s", name, count, word), fmt.Sprintf("use `%s`", example))
}

func unsupportedFormError(kind, name string, supported []string) error {
	return adviceError(
		fmt.Sprintf("unsupported %s %q", kind, name),
		fmt.Sprintf("use one of %s", strings.Join(supported, ", ")),
	)
}

func normalizePredicateLiteral(v Value) Value {
	if isListHead(v, "quote") && len(v.Items) == 2 {
		return cloneValue(v.Items[1])
	}
	return cloneValue(v)
}

func buildCTL(form Value) (CTLFormula, error) {
	if !isList(form) || len(form.Items) == 0 {
		return CTLFormula{}, adviceError("CTL formulas must be non-empty lists", "wrap the operator and its operands in parentheses, for example `(ef (in-state Server done))`")
	}

	head, err := expectSymbol(form.Items[0], "ctl operator")
	if err != nil {
		return CTLFormula{}, err
	}
	switch head {
	case "not":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return CTLFormula{}, operandError(head, 1, "(not p)")
		}
		inner, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return Not(inner), nil
	case "and":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return CTLFormula{}, operandError(head, 2, "(and p q)")
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
			return CTLFormula{}, operandError(head, 2, "(or p q)")
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
			return CTLFormula{}, operandError(head, 2, "(implies p q)")
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
			return CTLFormula{}, operandError("ex", 1, "(ex p)")
		}
		inner, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return EX(inner), nil
	case "ax":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return CTLFormula{}, operandError("ax", 1, "(ax p)")
		}
		inner, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return AX(inner), nil
	case "ef":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return CTLFormula{}, operandError("ef", 1, "(ef p)")
		}
		inner, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return EF(inner), nil
	case "af":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return CTLFormula{}, operandError("af", 1, "(af p)")
		}
		inner, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return AF(inner), nil
	case "eg":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return CTLFormula{}, operandError("eg", 1, "(eg p)")
		}
		inner, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return EG(inner), nil
	case "ag":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return CTLFormula{}, operandError("ag", 1, "(ag p)")
		}
		inner, err := buildCTL(items[1])
		if err != nil {
			return CTLFormula{}, err
		}
		return AG(inner), nil
	case "eu":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return CTLFormula{}, operandError("eu", 2, "(eu p q)")
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
			return CTLFormula{}, operandError("au", 2, "(au p q)")
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
			return CTLFormula{}, syntaxError("in-state", "(in-state ActorName StateName)")
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
			return CTLFormula{}, syntaxError("data=", "(data= ActorName key value)")
		}
		actor, err := expectSymbol(items[1], "actor name")
		if err != nil {
			return CTLFormula{}, err
		}
		key, err := expectSymbol(items[2], "data key")
		if err != nil {
			return CTLFormula{}, err
		}
		return Atom(ActorDataEquals(actor, key, normalizePredicateLiteral(items[3]))), nil
	case "mailbox-has":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return CTLFormula{}, syntaxError("mailbox-has", "(mailbox-has ActorName message)")
		}
		actor, err := expectSymbol(items[1], "actor name")
		if err != nil {
			return CTLFormula{}, err
		}
		return Atom(MailboxHas(actor, normalizePredicateLiteral(items[2]))), nil
	default:
		return CTLFormula{}, unsupportedFormError("CTL operator", head, []string{"`not`", "`and`", "`or`", "`implies`", "`ex`", "`ax`", "`ef`", "`af`", "`eg`", "`ag`", "`eu`", "`au`", "`in-state`", "`data=`", "`mailbox-has`"})
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
		return MuFormula{}, adviceError("modal mu-calculus formulas must be symbols or non-empty lists", "write a variable like `X`, a constant like `true`, or a list such as `(mu X (or p (diamond X)))`")
	}

	head, err := expectSymbol(form.Items[0], "mu operator")
	if err != nil {
		return MuFormula{}, err
	}
	switch head {
	case "not":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return MuFormula{}, operandError("not", 1, "(not p)")
		}
		inner, err := buildMu(items[1])
		if err != nil {
			return MuFormula{}, err
		}
		return MuNotFormula(inner), nil
	case "and":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return MuFormula{}, operandError("and", 2, "(and p q)")
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
			return MuFormula{}, operandError("or", 2, "(or p q)")
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
			return MuFormula{}, operandError("diamond", 1, "(diamond p)")
		}
		inner, err := buildMu(items[1])
		if err != nil {
			return MuFormula{}, err
		}
		return MuDiamondFormula(inner), nil
	case "box":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return MuFormula{}, operandError("box", 1, "(box p)")
		}
		inner, err := buildMu(items[1])
		if err != nil {
			return MuFormula{}, err
		}
		return MuBoxFormula(inner), nil
	case "mu":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return MuFormula{}, syntaxError("mu", "(mu X body)")
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
			return MuFormula{}, syntaxError("nu", "(nu X body)")
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
			return MuFormula{}, syntaxError("in-state", "(in-state ActorName StateName)")
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
			return MuFormula{}, syntaxError("data=", "(data= ActorName key value)")
		}
		actor, err := expectSymbol(items[1], "actor name")
		if err != nil {
			return MuFormula{}, err
		}
		key, err := expectSymbol(items[2], "data key")
		if err != nil {
			return MuFormula{}, err
		}
		return MuAtomFormula(ActorDataEquals(actor, key, normalizePredicateLiteral(items[3]))), nil
	case "mailbox-has":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return MuFormula{}, syntaxError("mailbox-has", "(mailbox-has ActorName message)")
		}
		actor, err := expectSymbol(items[1], "actor name")
		if err != nil {
			return MuFormula{}, err
		}
		return MuAtomFormula(MailboxHas(actor, normalizePredicateLiteral(items[2]))), nil
	default:
		return MuFormula{}, unsupportedFormError("mu operator", head, []string{"`true`", "`false`", "`not`", "`and`", "`or`", "`diamond`", "`box`", "`mu`", "`nu`", "`in-state`", "`data=`", "`mailbox-has`"})
	}
}

func (m *Model) HoldsAtInitial(formula CTLFormula) bool {
	return m.Holds(m.InitialID, formula)
}

func (m *Model) Holds(stateID string, formula CTLFormula) bool {
	set := m.SatisfyingStates(formula)
	return set[stateID]
}

func (m *RequirementsModel) Runtime() *Runtime {
	actors := make([]*Actor, len(m.Actors))
	for i, actor := range m.Actors {
		actors[i] = cloneActor(actor)
	}
	rt := NewRuntime(actors...)
	for _, decl := range m.Declarations {
		rt.MailboxCaps[decl.Name] = decl.QueueLen
	}
	return rt
}

func (m *RequirementsModel) CheckAssertions() ([]AssertionResult, error) {
	explored, err := ExploreModel(m.Runtime())
	if err != nil {
		return nil, err
	}
	results := make([]AssertionResult, 0, len(m.Assertions))
	for _, assertion := range m.Assertions {
		results = append(results, AssertionResult{
			Assertion: assertion,
			Holds:     explored.HoldsAtInitial(assertion.Formula),
		})
	}
	return results, nil
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

func (rt *Runtime) targetReady(target string, message Value) bool {
	cap := rt.mailboxCap(target)
	if cap == 0 {
		return rt.canRendezvous(target, message)
	}
	return cap < 0 || len(rt.Mailbox(target)) < cap
}

func (rt *Runtime) deliverMessage(from, to string, resolved Value) error {
	if rt.mailboxCap(to) == 0 {
		if err := rt.rendezvous(from, to, resolved); err != nil {
			return err
		}
		rt.logEvent(Event{
			Step:      rt.Step,
			Kind:      EventSend,
			ActorName: from,
			PeerName:  to,
			Message:   cloneValue(resolved),
		})
		rt.Tracef("%s -> %s %s", from, to, resolved.String())
		return nil
	}
	if cap := rt.mailboxCap(to); cap >= 0 && len(rt.Mailbox(to)) >= cap {
		return fmt.Errorf("mailbox %s is full", to)
	}
	rt.Enqueue(to, resolved, from)
	rt.logEvent(Event{
		Step:      rt.Step,
		Kind:      EventSend,
		ActorName: from,
		PeerName:  to,
		Message:   cloneValue(resolved),
	})
	rt.Tracef("%s -> %s %s", from, to, resolved.String())
	return nil
}

func Send(to string, message Value) ActionFunc {
	return func(rt *Runtime, actor *Actor) error {
		resolved := evalValue(actor, message)
		return rt.deliverMessage(actor.Name, to, resolved)
	}
}

func SendAny(targets []string, message Value) ActionFunc {
	return func(rt *Runtime, actor *Actor) error {
		resolved := evalValue(actor, message)
		for _, to := range targets {
			if !rt.targetReady(to, resolved) {
				continue
			}
			return rt.deliverMessage(actor.Name, to, resolved)
		}
		return fmt.Errorf("no send-any target ready for %s", resolved.String())
	}
}

func Receive(match MessageGuardFunc, handler MessageHandlerFunc) ActionFunc {
	return func(rt *Runtime, actor *Actor) error {
		if offered, ok := rt.SyncInbox[actor.Name]; ok {
			if match == nil || match(offered) {
				delete(rt.SyncInbox, actor.Name)
				sender := rt.SyncSender[actor.Name]
				delete(rt.SyncSender, actor.Name)
				rt.logEvent(Event{
					Step:      rt.Step,
					Kind:      EventReceive,
					ActorName: actor.Name,
					PeerName:  sender,
					Message:   cloneValue(offered),
				})
				rt.Tracef("%s <= %s", actor.Name, offered.String())
				if handler == nil {
					return nil
				}
				return handler(rt, actor, offered, sender)
			}
		}
		message, sender, ok := rt.DequeueMatching(actor.Name, match)
		if !ok {
			return nil
		}
		rt.logEvent(Event{
			Step:      rt.Step,
			Kind:      EventReceive,
			ActorName: actor.Name,
			PeerName:  sender,
			Message:   cloneValue(message),
		})
		rt.Tracef("%s <= %s", actor.Name, message.String())
		if handler == nil {
			return nil
		}
		return handler(rt, actor, message, sender)
	}
}

func ReceiveInto(name string) ActionFunc {
	return Receive(nil, func(_ *Runtime, actor *Actor, message Value, sender string) error {
		actor.Data[name] = cloneValue(message)
		actor.Data["sender"] = Symbol(sender)
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
		return rt.targetReady(target, message)
	case "send-any":
		if len(form.Items) < 4 {
			return false
		}
		message := form.Items[len(form.Items)-1]
		for _, item := range form.Items[1 : len(form.Items)-1] {
			target, err := expectSymbol(item, "send-any target")
			if err != nil {
				return false
			}
			if rt.targetReady(target, message) {
				return true
			}
		}
		return false
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

func (rt *Runtime) rendezvous(from, target string, message Value) error {
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
		rt.SyncSender[target] = from
		rt.Tracef("step: actor=%s state=%s transition=%s", targetActor.Name, state.Name, transition.Name)
		if transition.Action != nil {
			if err := transition.Action(rt, targetActor); err != nil {
				delete(rt.SyncInbox, target)
				delete(rt.SyncSender, target)
				return err
			}
		}
		if err := rt.validateTransitionNext(transition, targetActor); err != nil {
			delete(rt.SyncInbox, target)
			delete(rt.SyncSender, target)
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
			return false, unsupportedFormError("guard symbol", form.Text, []string{"`true`", "`dice`"})
		}
	}
	if !isList(form) || len(form.Items) == 0 {
		return false, adviceError(fmt.Sprintf("unsupported guard %s", form.String()), "write a guard symbol like `true` or a guard list such as `(and (mailbox req) (data> count 0))`")
	}
	head, err := expectSymbol(form.Items[0], "guard operator")
	if err != nil {
		return false, err
	}
	switch head {
	case "mailbox":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return false, syntaxError("mailbox guard", "(mailbox message)")
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
			return false, operandError(head, 1, "(not guard)")
		}
		ok, err := rt.evalGuardSpec(items[1], actor, offered)
		if err != nil {
			return false, err
		}
		return !ok, nil
	case "implies", "->":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return false, operandError(head, 2, "(implies left right)")
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
			return false, syntaxError("dice-range guard", "(dice-range low high)")
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
			return false, syntaxError("dice< guard", "(dice< high)")
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
			return false, syntaxError("dice>= guard", "(dice>= low)")
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
			return false, syntaxError("data= guard", "(data= key value)")
		}
		key, err := expectSymbol(items[1], "data key")
		if err != nil {
			return false, err
		}
		return actor.Data[key].Equal(items[2]), nil
	case "data>":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) != 3 {
			return false, syntaxError("data> guard", "(data> key value)")
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
		return false, unsupportedFormError("guard form", head, []string{"`mailbox`", "`and`", "`or`", "`not`", "`implies`", "`dice-range`", "`dice<`", "`dice>=`", "`data=`", "`data>`"})
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

func CompileModel(src string) (*RequirementsModel, error) {
	form, err := Read(src)
	if err != nil {
		return nil, err
	}
	return buildRequirementsModel(form)
}

func MustCompileActor(src string) *Actor {
	actor, err := CompileActor(src)
	if err != nil {
		panic(err)
	}
	return actor
}

func MustCompileModel(src string) *RequirementsModel {
	model, err := CompileModel(src)
	if err != nil {
		panic(err)
	}
	return model
}

func buildActor(form Value) (*Actor, error) {
	if !isListHead(form, "actor") || len(form.Items) < 3 {
		return nil, syntaxError("actor", "(actor RoleName item...)")
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
		switch {
		case isListHead(item, "state"):
			states, err := buildState(item)
			if err != nil {
				return nil, fmt.Errorf("actor %s: %w", name, err)
			}
			actor.States = append(actor.States, states...)
		case isListHead(item, "data"):
			if len(item.Items) != 3 {
				return nil, fmt.Errorf("actor %s: %w", name, syntaxError("data", "(data key value)"))
			}
			key, err := expectSymbol(item.Items[1], "data key")
			if err != nil {
				return nil, fmt.Errorf("actor %s: %w", name, err)
			}
			actor.Data[key] = cloneValue(item.Items[2])
		default:
			return nil, fmt.Errorf("actor %s: %w", name, adviceError("actor items must be `state` or `data` forms", "move helper declarations inside actions with `def`, and keep the actor body as `(data ...)` or `(state ...)` only"))
		}
	}
	if len(actor.States) == 0 {
		return nil, fmt.Errorf("actor %s: %w", name, adviceError("actor has no states", "declare at least one `(state Name ...)`; the first state becomes the initial control location"))
	}
	actor.Data["state"] = Symbol(actor.States[0].Name)
	return actor, nil
}

func buildRequirementsModel(form Value) (*RequirementsModel, error) {
	if !isListHead(form, "model") || len(form.Items) < 2 {
		return nil, syntaxError("model", "(model item...)")
	}
	model := &RequirementsModel{Spec: form}
	actorTypes := map[string]*Actor{}
	for _, item := range form.Items[1:] {
		switch {
		case isListHead(item, "actor"):
			actor, err := buildActor(item)
			if err != nil {
				return nil, err
			}
			if _, exists := actorTypes[actor.Name]; exists {
				return nil, adviceError(fmt.Sprintf("duplicate actor role %s", actor.Name), "rename one role or merge their states into a single `(actor RoleName ...)` block")
			}
			actorTypes[actor.Name] = actor
			model.ActorTypes = append(model.ActorTypes, actor)
		case isListHead(item, "instance"):
			decl, err := buildActorDeclaration(item)
			if err != nil {
				return nil, err
			}
			model.Declarations = append(model.Declarations, decl)
		case isListHead(item, "assert"):
			assertion, err := buildAssertion(item)
			if err != nil {
				return nil, err
			}
			model.Assertions = append(model.Assertions, assertion)
		case isListHead(item, "xyplot"):
			plot, err := buildXYPlot(item)
			if err != nil {
				return nil, err
			}
			model.Plots = append(model.Plots, plot)
		default:
			return nil, adviceError("model items must be `actor`, `instance`, `assert`, or `xyplot` forms", "move any other forms inside an actor state or inside a supported top-level form")
		}
	}
	if len(model.Declarations) == 0 {
		return nil, adviceError("model has no actor instances", "add at least one `(instance Name Role (queue n) ...)` so the runtime has concrete actors to explore")
	}
	seenNames := map[string]bool{}
	declarationsByName := map[string]ActorDeclaration{}
	for _, decl := range model.Declarations {
		if seenNames[decl.Name] {
			return nil, adviceError(fmt.Sprintf("duplicate actor name %s", decl.Name), "give each `(instance Name Role (queue n) ...)` a unique concrete name")
		}
		seenNames[decl.Name] = true
		declarationsByName[decl.Name] = decl
	}
	for _, decl := range model.Declarations {
		actorType, ok := actorTypes[decl.Role]
		if !ok {
			return nil, adviceError(fmt.Sprintf("instance %s references unknown actor role %s", decl.Name, decl.Role), "declare that role with `(actor RoleName ...)` before using it in `(instance Name Role (queue n) ...)`")
		}
		requiredPeerRoles, err := collectActorPeerRoles(actorType)
		if err != nil {
			return nil, fmt.Errorf("actor role %s: %w", actorType.Name, err)
		}
		if err := validateActorRoleBindings(decl, actorType, actorTypes, declarationsByName, requiredPeerRoles); err != nil {
			return nil, err
		}
		actor := cloneActor(actorType)
		actor.Name = decl.Name
		actor.Role = decl.Role
		actor.RoleBindings = cloneStringSliceMap(decl.RoleBindings)
		actor.Spec = Value{}
		if err := resolveActorPeerRoles(actor); err != nil {
			return nil, err
		}
		model.Actors = append(model.Actors, actor)
	}
	if len(model.Actors) == 0 {
		return nil, adviceError("model produced no runtime actors", "check that each `(instance Name Role (queue n) ...)` references a declared role and survived validation")
	}
	return model, nil
}

func isGeneratedStepName(name string) bool {
	return strings.Contains(name, "__wait")
}

func collectActorPeerRoles(actor *Actor) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, state := range actor.States {
		for _, transition := range state.Transitions {
			if err := walkSendTargets(transition.ActionSpec, func(role string) {
				if !seen[role] {
					seen[role] = true
					out = append(out, role)
				}
			}); err != nil {
				return nil, err
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func walkSendTargets(form Value, visit func(string)) error {
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
			if err := walkSendTargets(item, visit); err != nil {
				return err
			}
		}
	case "if":
		for _, item := range form.Items[2:] {
			if err := walkSendTargets(item, visit); err != nil {
				return err
			}
		}
	case "send", "send-any":
		if len(form.Items) != 3 {
			return syntaxError(head, fmt.Sprintf("(%s role message)", head))
		}
		role, err := expectSymbol(form.Items[1], "send target role")
		if err != nil {
			return err
		}
		visit(role)
	}
	return nil
}

func validateActorRoleBindings(decl ActorDeclaration, actorType *Actor, actorTypes map[string]*Actor, declarations map[string]ActorDeclaration, requiredPeerRoles []string) error {
	required := map[string]bool{}
	for _, role := range requiredPeerRoles {
		required[role] = true
		if _, ok := actorTypes[role]; !ok {
			return adviceError(fmt.Sprintf("actor role %s sends to unknown peer role %s", actorType.Name, role), "declare the peer role with `(actor PeerRole ...)` or change the send target role name")
		}
		targetInstances, ok := decl.RoleBindings[role]
		if !ok || len(targetInstances) == 0 {
			return adviceError(fmt.Sprintf("instance %s must fill peer role %s", decl.Name, role), fmt.Sprintf("add a binding like `(%s TargetInstance)` inside that `(instance %s %s (queue %d) ...)` form", role, decl.Name, decl.Role, decl.QueueLen))
		}
		for _, targetInstance := range targetInstances {
			targetDecl, ok := declarations[targetInstance]
			if !ok {
				return adviceError(fmt.Sprintf("instance %s fills peer role %s with unknown instance %s", decl.Name, role, targetInstance), "declare that target with its own `(instance Name Role (queue n) ...)` form")
			}
			if targetDecl.Role != role {
				return adviceError(fmt.Sprintf("instance %s fills peer role %s with instance %s playing role %s", decl.Name, role, targetInstance, targetDecl.Role), fmt.Sprintf("bind `%s` to an instance whose declared role is `%s`", role, role))
			}
		}
	}
	for role := range decl.RoleBindings {
		if !required[role] {
			return adviceError(fmt.Sprintf("instance %s provides unused peer role fill %s", decl.Name, role), "remove that binding or add a matching `send` or `send-any` that uses the role")
		}
	}
	return nil
}

func resolveActorPeerRoles(actor *Actor) error {
	for i := range actor.States {
		actor.States[i].Spec = Value{}
		for j := range actor.States[i].Transitions {
			resolved, err := resolveSendTargets(actor.States[i].Transitions[j].ActionSpec, actor.RoleBindings)
			if err != nil {
				return fmt.Errorf("instance %s: %w", actor.Name, err)
			}
			actor.States[i].Transitions[j].ActionSpec = resolved
			action, err := compileAction(resolved)
			if err != nil {
				return fmt.Errorf("instance %s: %w", actor.Name, err)
			}
			actor.States[i].Transitions[j].Action = action
		}
	}
	return nil
}

func resolveSendTargets(form Value, bindings map[string][]string) (Value, error) {
	if !isList(form) || len(form.Items) == 0 {
		return cloneValue(form), nil
	}
	head, err := expectSymbol(form.Items[0], "action operator")
	if err != nil {
		return Value{}, err
	}
	switch head {
	case "send":
		if len(form.Items) != 3 {
			return Value{}, syntaxError("send", "(send role message)")
		}
		role, err := expectSymbol(form.Items[1], "peer role")
		if err != nil {
			return Value{}, err
		}
		targets, ok := bindings[role]
		if !ok || len(targets) == 0 {
			return Value{}, adviceError(fmt.Sprintf("unresolved peer role %s", role), "add that role to the enclosing `(instance Name Role (queue n) ...)` bindings")
		}
		if len(targets) != 1 {
			return Value{}, adviceError(fmt.Sprintf("peer role %s resolves to %d instances", role, len(targets)), "replace `send` with `send-any` when a role can map to multiple concrete actors")
		}
		return List(Symbol("send"), Symbol(targets[0]), cloneValue(form.Items[2])), nil
	case "send-any":
		if len(form.Items) != 3 {
			return Value{}, syntaxError("send-any", "(send-any role message)")
		}
		role, err := expectSymbol(form.Items[1], "peer role")
		if err != nil {
			return Value{}, err
		}
		targets, ok := bindings[role]
		if !ok || len(targets) == 0 {
			return Value{}, adviceError(fmt.Sprintf("unresolved peer role %s", role), "add that role to the enclosing `(instance Name Role (queue n) ...)` bindings")
		}
		items := []Value{Symbol("send-any")}
		for _, target := range targets {
			items = append(items, Symbol(target))
		}
		items = append(items, cloneValue(form.Items[2]))
		return List(items...), nil
	case "do", "if":
		items := make([]Value, len(form.Items))
		items[0] = cloneValue(form.Items[0])
		for i := 1; i < len(form.Items); i++ {
			item, err := resolveSendTargets(form.Items[i], bindings)
			if err != nil {
				return Value{}, err
			}
			items[i] = item
		}
		return List(items...), nil
	default:
		return cloneValue(form), nil
	}
}

func buildActorDeclaration(form Value) (ActorDeclaration, error) {
	if !isListHead(form, "instance") || len(form.Items) < 4 {
		return ActorDeclaration{}, syntaxError("instance", "(instance Name Role (queue n) (PeerRole Target...)...)")
	}
	name, err := expectSymbol(form.Items[1], "actor name")
	if err != nil {
		return ActorDeclaration{}, err
	}
	role, err := expectSymbol(form.Items[2], "actor role")
	if err != nil {
		return ActorDeclaration{}, err
	}
	if !isListHead(form.Items[3], "queue") || len(form.Items[3].Items) != 2 {
		return ActorDeclaration{}, syntaxError("instance queue", "(queue n)")
	}
	queueLen, err := valueInt(form.Items[3].Items[1])
	if err != nil {
		return ActorDeclaration{}, err
	}
	if queueLen < 0 {
		return ActorDeclaration{}, adviceError("instance queue length must be non-negative", "use `0` for rendezvous mailboxes or a positive integer for buffered mailboxes")
	}
	bindings := map[string][]string{}
	for _, item := range form.Items[4:] {
		if !isList(item) || len(item.Items) < 2 {
			return ActorDeclaration{}, syntaxError("instance binding", "(PeerRole InstanceName...)")
		}
		peerRole, err := expectSymbol(item.Items[0], "peer role")
		if err != nil {
			return ActorDeclaration{}, err
		}
		if _, exists := bindings[peerRole]; exists {
			return ActorDeclaration{}, adviceError(fmt.Sprintf("instance %s repeats binding for peer role %s", name, peerRole), "keep one binding per peer role and list all target instances inside it")
		}
		targets := make([]string, 0, len(item.Items)-1)
		seen := map[string]bool{}
		for _, targetForm := range item.Items[1:] {
			target, err := expectSymbol(targetForm, "peer instance")
			if err != nil {
				return ActorDeclaration{}, err
			}
			if seen[target] {
				return ActorDeclaration{}, adviceError(fmt.Sprintf("instance %s repeats peer instance %s for role %s", name, target, peerRole), "list each concrete peer instance only once per role binding")
			}
			seen[target] = true
			targets = append(targets, target)
		}
		bindings[peerRole] = targets
	}
	return ActorDeclaration{
		Name:         name,
		Role:         role,
		QueueLen:     queueLen,
		RoleBindings: bindings,
		Spec:         form,
	}, nil
}

func buildAssertion(form Value) (Assertion, error) {
	if !isListHead(form, "assert") || len(form.Items) != 2 {
		return Assertion{}, syntaxError("assert", "(assert ctl-formula)")
	}
	formula, err := buildCTL(form.Items[1])
	if err != nil {
		return Assertion{}, err
	}
	return Assertion{Formula: formula, Spec: form}, nil
}

func buildXYPlot(form Value) (XYPlot, error) {
	if !isListHead(form, "xyplot") || len(form.Items) < 2 {
		return XYPlot{}, syntaxError("xyplot", "(xyplot name option...)")
	}
	name, err := expectSymbol(form.Items[1], "xyplot name")
	if err != nil {
		return XYPlot{}, err
	}
	plot := XYPlot{
		Name:   name,
		Title:  name,
		Steps:  100,
		Metric: "sent-minus-received",
		Spec:   form,
	}
	for _, item := range form.Items[2:] {
		if !isList(item) || len(item.Items) == 0 {
			return XYPlot{}, adviceError("xyplot options must be non-empty lists", "rewrite each option as a list such as `(title \"Queue backlog\")` or `(steps 100)`")
		}
		head, err := expectSymbol(item.Items[0], "xyplot option")
		if err != nil {
			return XYPlot{}, err
		}
		switch head {
		case "title":
			if len(item.Items) != 2 || item.Items[1].Kind != KindString {
				return XYPlot{}, syntaxError("xyplot title", "(title \"...\")")
			}
			plot.Title = item.Items[1].Text
		case "steps":
			if len(item.Items) != 2 {
				return XYPlot{}, syntaxError("xyplot steps", "(steps n)")
			}
			steps, err := valueInt(item.Items[1])
			if err != nil {
				return XYPlot{}, err
			}
			plot.Steps = steps
		case "metric":
			if len(item.Items) != 2 {
				return XYPlot{}, syntaxError("xyplot metric", "(metric name)")
			}
			metric, err := expectSymbol(item.Items[1], "xyplot metric")
			if err != nil {
				return XYPlot{}, err
			}
			switch metric {
			case "sent-minus-received", "send-count", "receive-count", "send-rate", "receive-rate":
			default:
				return XYPlot{}, unsupportedFormError("xyplot metric", metric, []string{"`sent-minus-received`", "`send-rate`", "`receive-rate`", "`send-count`", "`receive-count`"})
			}
			plot.Metric = metric
		default:
			return XYPlot{}, unsupportedFormError("xyplot option", head, []string{"`title`", "`steps`", "`metric`"})
		}
	}
	return plot, nil
}

func buildState(form Value) ([]State, error) {
	if !isListHead(form, "state") || len(form.Items) < 2 {
		return nil, syntaxError("state", "(state Name edge...)")
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
	waitNames := generatedWaitNames(stateName, items, commIdxs)
	nextWait := 0

	var transitions []Transition
	var hiddenStates []State
	currentState := stateName
	currentGuard := base.GuardSpec
	start := 0
	for i, idx := range commIdxs {
		if idx > start {
			waitName := waitNames[nextWait]
			nextWait++
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
			waitName := waitNames[nextWait]
			nextWait++
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

func generatedWaitNames(stateName string, items []Value, commIdxs []int) []string {
	count := 0
	start := 0
	for i, idx := range commIdxs {
		if idx > start {
			count++
		}
		if i+1 < len(commIdxs) {
			count++
			start = commIdxs[i+1]
		} else {
			start = len(items)
		}
	}
	names := make([]string, 0, count)
	for i := 0; i < count; i++ {
		names = append(names, generatedWaitName(stateName, i, count))
	}
	return names
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

func generatedWaitName(stateName string, idx, total int) string {
	if total == 1 {
		return fmt.Sprintf("%s__wait", stateName)
	}
	return fmt.Sprintf("%s__wait_%d", stateName, idx)
}

func collectBecomeStates(form Value) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	if err := walkBecomeStates(form, &out, seen); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, adviceError("edge has no reachable `become`", "end every control-flow branch with `(become StateName)` so the next state is explicit")
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
			return syntaxError("become", "(become StateName)")
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
	case "send", "send-any", "recv":
		return adviceError(fmt.Sprintf("%s must be the first action after the edge condition", head), "move communication to the front of the edge body, or split the logic into extra states so send/recv happens before local work")
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

func validatePureFunctionBody(form Value) error {
	if !isList(form) || len(form.Items) == 0 {
		return nil
	}
	if isListHead(form, "quote") {
		return nil
	}
	head, err := expectSymbol(form.Items[0], "function body operator")
	if err != nil {
		return err
	}
	switch head {
	case "send", "send-any", "recv", "become", "do", "if", "set", "add", "sub", "md5", "rsa-raw", "cryptorandom", "sample-exponential", "def":
		return adviceError(
			fmt.Sprintf("function body uses forbidden form `%s`", head),
			"keep `def` bodies pure and move messaging, state changes, and other actions into the surrounding `edge` body",
		)
	}
	for _, item := range form.Items {
		if err := validatePureFunctionBody(item); err != nil {
			return err
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
	return head == "send" || head == "send-any" || head == "recv"
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
			return nil, unsupportedFormError("guard symbol", form.Text, []string{"`true`", "`dice`"})
		}
	}

	if !isList(form) || len(form.Items) == 0 {
		return nil, adviceError(fmt.Sprintf("unsupported guard %s", form.String()), "write a guard symbol like `true` or a guard list such as `(and (mailbox req) (data> count 0))`")
	}

	head, err := expectSymbol(form.Items[0], "guard operator")
	if err != nil {
		return nil, err
	}
	switch head {
	case "mailbox":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) != 2 {
			return nil, syntaxError("mailbox guard", "(mailbox message)")
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
			return nil, adviceError("and guard needs at least two operands", "write `(and guard1 guard2 ...)` with two or more child guards")
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
			return nil, adviceError("or guard needs at least two operands", "write `(or guard1 guard2 ...)` with two or more child guards")
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
			return nil, operandError(head, 1, "(not guard)")
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
			return nil, operandError(head, 2, "(implies left right)")
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
			return nil, syntaxError("dice-range guard", "(dice-range low high)")
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
			return nil, syntaxError("dice< guard", "(dice< high)")
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
			return nil, syntaxError("dice>= guard", "(dice>= low)")
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
			return nil, syntaxError("data= guard", "(data= key value)")
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
			return nil, syntaxError("data> guard", "(data> key value)")
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
		return nil, unsupportedFormError("guard form", head, []string{"`mailbox`", "`and`", "`or`", "`not`", "`implies`", "`dice-range`", "`dice<`", "`dice>=`", "`data=`", "`data>`"})
	}
}

func compileAction(form Value) (ActionFunc, error) {
	if !isList(form) || len(form.Items) == 0 {
		return nil, adviceError(fmt.Sprintf("unsupported action %s", form.String()), "write an action list headed by `send`, `recv`, `become`, `set`, `if`, `do`, or another documented action form")
	}

	head, err := expectSymbol(form.Items[0], "action operator")
	if err != nil {
		return nil, err
	}
	switch head {
	case "do":
		if len(form.Items) < 2 {
			return nil, adviceError("do needs at least one action", "write `(do action1 action2 ...)` or remove the empty `do` wrapper")
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
			return nil, syntaxError("send", "(send ActorName message)")
		}
		to, err := expectSymbol(form.Items[1], "send target")
		if err != nil {
			return nil, err
		}
		return Send(to, form.Items[2]), nil
	case "send-any":
		if len(form.Items) < 3 {
			return nil, syntaxError("send-any", "(send-any ActorName... message)")
		}
		targets := make([]string, 0, len(form.Items)-2)
		for _, item := range form.Items[1 : len(form.Items)-1] {
			target, err := expectSymbol(item, "send-any target")
			if err != nil {
				return nil, err
			}
			targets = append(targets, target)
		}
		return SendAny(targets, form.Items[len(form.Items)-1]), nil
	case "recv":
		if len(form.Items) != 2 {
			return nil, syntaxError("recv", "(recv variable)")
		}
		name, err := expectSymbol(form.Items[1], "recv variable")
		if err != nil {
			return nil, err
		}
		return ReceiveInto(name), nil
	case "become":
		if len(form.Items) != 2 {
			return nil, syntaxError("become", "(become StateName)")
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
			return nil, syntaxError("set", "(set key value)")
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
			return nil, syntaxError("def", "(def name (params...) body)")
		}
		name, err := expectSymbol(form.Items[1], "function name")
		if err != nil {
			return nil, err
		}
		paramsForm := form.Items[2]
		if !isList(paramsForm) {
			return nil, adviceError("def parameter list must be a list", "rewrite the helper as `(def name (p1 p2 ...) body)`")
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
		if err := validatePureFunctionBody(body); err != nil {
			return nil, err
		}
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
			return nil, syntaxError("if", "(if guard then [else])")
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
			return nil, syntaxError("add", "(add key value)")
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
			return nil, syntaxError("sub", "(sub key value)")
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
			return nil, syntaxError("md5", "(md5 out source)")
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
			return nil, syntaxError("rsa-raw", "(rsa-raw out modulus exponent message)")
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
			return nil, syntaxError("cryptorandom", "(cryptorandom out bytes)")
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
			return nil, adviceError("cryptorandom byte count must be non-negative", "use `0` or a positive integer byte count")
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
			return nil, syntaxError("sample-exponential", "(sample-exponential out rate)")
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
			return nil, adviceError("sample-exponential rate must be positive", "use a positive floating-point rate such as `0.5` or `2.0`")
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
		return nil, unsupportedFormError("action form", head, []string{"`do`", "`send`", "`send-any`", "`recv`", "`become`", "`set`", "`def`", "`if`", "`add`", "`sub`", "`md5`", "`rsa-raw`", "`cryptorandom`", "`sample-exponential`"})
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

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneStringSliceMap(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, value := range in {
		out[key] = append([]string(nil), value...)
	}
	return out
}

func cloneStates(in []State) []State {
	if len(in) == 0 {
		return nil
	}
	out := make([]State, len(in))
	for i, state := range in {
		out[i] = State{
			Name:        state.Name,
			Guard:       state.Guard,
			Control:     state.Control,
			Transitions: cloneTransitions(state.Transitions),
			Spec:        cloneValue(state.Spec),
		}
	}
	return out
}

func cloneTransitions(in []Transition) []Transition {
	if len(in) == 0 {
		return nil
	}
	out := make([]Transition, len(in))
	for i, transition := range in {
		out[i] = Transition{
			Name:       transition.Name,
			Guard:      transition.Guard,
			Action:     transition.Action,
			NextStates: append([]string(nil), transition.NextStates...),
			GuardSpec:  cloneValue(transition.GuardSpec),
			ActionSpec: cloneValue(transition.ActionSpec),
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

func sortedStringKeys(in map[string][]string) []string {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func mailboxSenderAt(senders []string, idx int) string {
	if idx < 0 || idx >= len(senders) {
		return ""
	}
	return senders[idx]
}

func valueInt(v Value) (int, error) {
	if v.Kind != KindNumber {
		return 0, adviceError(fmt.Sprintf("value %s is not a number", v.String()), "use an integer literal such as `0`, `1`, or `42`")
	}
	out, err := strconv.Atoi(v.Text)
	if err != nil {
		return 0, adviceError(fmt.Sprintf("invalid integer %s", v.Text), "use a base-10 integer literal such as `0`, `1`, or `42`")
	}
	return out, nil
}

func valueFloat(v Value) (float64, error) {
	if v.Kind != KindNumber {
		return 0, adviceError(fmt.Sprintf("value %s is not a number", v.String()), "use a numeric literal such as `0.5`, `1.0`, or `2`")
	}
	out, err := strconv.ParseFloat(v.Text, 64)
	if err != nil {
		return 0, adviceError(fmt.Sprintf("invalid floating-point number %s", v.Text), "use a decimal literal such as `0.5` or `2.0`")
	}
	return out, nil
}

func valueBigInt(v Value) (*big.Int, error) {
	switch v.Kind {
	case KindNumber:
		out, ok := new(big.Int).SetString(v.Text, 10)
		if !ok {
			return nil, adviceError(fmt.Sprintf("invalid integer %s", v.Text), "use a base-10 integer literal such as `65537`")
		}
		return out, nil
	case KindString:
		out, ok := new(big.Int).SetString(v.Text, 10)
		if !ok {
			return nil, adviceError(fmt.Sprintf("invalid integer string %s", v.Text), "store a base-10 integer string such as \"65537\", or use a numeric literal instead")
		}
		return out, nil
	default:
		return nil, adviceError(fmt.Sprintf("value %s is not an integer", v.String()), "use an integer literal or a variable that resolves to an integer")
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
		return "", adviceError(fmt.Sprintf("%s must be a symbol", context), fmt.Sprintf("replace %s with a bare symbol such as `Server`, `done`, or `count`", v.String()))
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
		return Value{}, adviceError(fmt.Sprintf("unexpected token %q", p.peek().text), "remove the extra token or wrap the whole program in one valid Lisp form")
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
	if a.Role != "" && a.Role != a.Name {
		items = append(items, List(Symbol("role"), Symbol(a.Role)))
	}
	for _, role := range sortedStringKeys(a.RoleBindings) {
		binding := []Value{Symbol(role)}
		for _, target := range a.RoleBindings[role] {
			binding = append(binding, Symbol(target))
		}
		items = append(items, List(binding...))
	}
	for _, state := range a.States {
		items = append(items, state.Lisp())
	}
	return List(items...)
}

func (d ActorDeclaration) Lisp() Value {
	if d.Spec.Kind != KindInvalid {
		return d.Spec
	}
	items := []Value{Symbol("instance"), Symbol(d.Name), Symbol(d.Role), List(Symbol("queue"), Number(strconv.Itoa(d.QueueLen)))}
	for _, role := range sortedStringKeys(d.RoleBindings) {
		binding := []Value{Symbol(role)}
		for _, target := range d.RoleBindings[role] {
			binding = append(binding, Symbol(target))
		}
		items = append(items, List(binding...))
	}
	return List(items...)
}

func (a Assertion) Lisp() Value {
	if a.Spec.Kind != KindInvalid {
		return a.Spec
	}
	return List(Symbol("assert"))
}

func (p XYPlot) Lisp() Value {
	if p.Spec.Kind != KindInvalid {
		return p.Spec
	}
	return List(
		Symbol("xyplot"),
		Symbol(p.Name),
		List(Symbol("title"), String(p.Title)),
		List(Symbol("steps"), Number(strconv.Itoa(p.Steps))),
		List(Symbol("metric"), Symbol(p.Metric)),
	)
}

func (m RequirementsModel) Lisp() Value {
	if m.Spec.Kind != KindInvalid {
		return m.Spec
	}
	items := []Value{Symbol("model")}
	for _, actorType := range m.ActorTypes {
		items = append(items, actorType.Lisp())
	}
	for _, decl := range m.Declarations {
		items = append(items, decl.Lisp())
	}
	for _, assertion := range m.Assertions {
		items = append(items, assertion.Lisp())
	}
	for _, plot := range m.Plots {
		items = append(items, plot.Lisp())
	}
	return List(items...)
}

func (rt *Runtime) Lisp() Value {
	items := []Value{Symbol("runtime")}
	for _, actor := range rt.Actors {
		items = append(items, actor.Lisp())
		items = append(items, runtimeActorState(actor))
		items = append(items, runtimeActorData(actor))
		items = append(items, runtimeMailbox(actor.Name, rt.Mailbox(actor.Name), rt.mailboxSenders(actor.Name)))
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

func runtimeMailbox(name string, messages []Value, senders []string) Value {
	items := []Value{Symbol("mailbox"), Symbol(name)}
	for i, message := range messages {
		sender := mailboxSenderAt(senders, i)
		if sender == "" {
			items = append(items, message)
			continue
		}
		items = append(items, List(Symbol("message"), List(Symbol("from"), Symbol(sender)), List(Symbol("body"), message)))
	}
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
		return Value{}, adviceError("unexpected end of input", "close any open lists and finish the current form")
	}

	switch p.peek().kind {
	case "quote":
		return p.parseQuote()
	case "lparen":
		return p.parseSExpr()
	case "symbol", "number", "string", "bool":
		return p.parseAtom()
	default:
		return Value{}, adviceError(fmt.Sprintf("unexpected token %q", p.peek().text), "start a form with `(`, a quote `'`, or an atom")
	}
}

func (p *parser) parseQuote() (Value, error) {
	if !p.match("quote") {
		return Value{}, adviceError("expected quote", "use `'value` to quote the next form")
	}
	quoted, err := p.parseValue()
	if err != nil {
		return Value{}, err
	}
	return List(Symbol("quote"), quoted), nil
}

func (p *parser) parseSExpr() (Value, error) {
	if !p.match("lparen") {
		return Value{}, adviceError("expected '('", "start each list form with an opening parenthesis")
	}

	var items []Value
	for {
		if !p.hasNext() {
			return Value{}, adviceError("unterminated list", "add the missing `)` to close the current form")
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
		return Value{}, adviceError("unexpected end of input", "close any open lists and finish the current form")
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
		return Value{}, adviceError("unexpected end of input", "finish the atom or close the surrounding list")
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
		return Value{}, adviceError(fmt.Sprintf("unexpected atom %q", tok.text), "use a symbol, number, string, or boolean atom")
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
		if ch == ';' && i+1 < len(runes) && runes[i+1] == ';' {
			i += 2
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
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
				return nil, adviceError("unterminated string literal", "add the closing double quote to finish the string")
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

		return nil, adviceError(fmt.Sprintf("unexpected character %q", string(ch)), "remove that character or rewrite the form with Lisp syntax such as `(send Target msg)`")
	}

	return out, nil
}

func isSymbolStart(ch rune) bool {
	return unicode.IsLetter(ch) || strings.ContainsRune("+-*/<>=!?_", ch)
}

func isSymbolPart(ch rune) bool {
	return unicode.IsLetter(ch) || unicode.IsDigit(ch) || strings.ContainsRune("+-*/<>=!?_", ch)
}

type docPlotData struct {
	Name      string        `json:"name"`
	Title     string        `json:"title"`
	Subtitle  string        `json:"subtitle"`
	Steps     int           `json:"steps"`
	Metric    string        `json:"metric"`
	YLabel    string        `json:"ylabel"`
	Legend    string        `json:"legend"`
	Series    []MetricPoint `json:"series"`
	ImageName string        `json:"image_name"`
}

type docPlotBinding struct {
	Plot     XYPlot
	Subtitle string
	Runtime  func(int) (*Runtime, error)
}

type languageFormDoc struct {
	Form      string
	Params    string
	Semantics string
}

type docExample struct {
	Title  string
	Source string
	Spec   *RequirementsModel
}

const docQueueModelSource = `
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
`

const docMessageModelSource = `
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
`

const docBakeryModelSource = `
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
`

func docQueueModel() (*RequirementsModel, error) {
	return CompileModel(docQueueModelSource)
}

func docMessageModel() (*RequirementsModel, error) {
	return CompileModel(docMessageModelSource)
}

func docBakeryModel() (*RequirementsModel, error) {
	return CompileModel(docBakeryModelSource)
}

func docQueueRuntime(steps int) (*Runtime, error) {
	spec, err := docQueueModel()
	if err != nil {
		return nil, err
	}
	rt := spec.Runtime()
	rt.Actors[1].Data["count"] = Number("0")
	rt.Actors[1].Data["elapsed"] = Number("0")
	rt.Actors[1].Data["dropped_count"] = Number("0")

	rng := rand.New(rand.NewSource(7))
	rt.Dice = rng.Float64

	nextActor := 0
	rt.ChooseActorFn = func(runtime *Runtime) int {
		index := nextActor
		nextActor = (nextActor + 1) % len(runtime.Actors)
		return index
	}

	maxTicks := steps*8 + 64
	for attempts := 0; rt.Step < steps && attempts < maxTicks; attempts++ {
		if err := rt.Tick(); err != nil {
			return nil, err
		}
	}
	if rt.Step < steps {
		return nil, fmt.Errorf("doc queue example reached only %d applied steps after %d ticks", rt.Step, maxTicks)
	}
	return rt, nil
}

func docMessageRuntime(steps int) (*Runtime, error) {
	spec, err := docMessageModel()
	if err != nil {
		return nil, err
	}
	rt := spec.Runtime()
	order := []int{0, 1, 1, 2}
	next := 0
	rt.ChooseActorFn = func(*Runtime) int {
		index := order[next%len(order)]
		next++
		return index
	}
	maxTicks := steps + 16
	for attempts := 0; rt.Step < steps && attempts < maxTicks; attempts++ {
		if err := rt.Tick(); err != nil {
			return nil, err
		}
	}
	if rt.Step < steps {
		return nil, fmt.Errorf("doc message example reached only %d applied steps after %d ticks", rt.Step, maxTicks)
	}
	return rt, nil
}

func docPlotBindings() (map[string]docPlotBinding, error) {
	queueModel, err := docQueueModel()
	if err != nil {
		return nil, err
	}
	messageModel, err := docMessageModel()
	if err != nil {
		return nil, err
	}
	out := map[string]docPlotBinding{}
	add := func(model *RequirementsModel, subtitle string, runtime func(int) (*Runtime, error)) {
		for _, plot := range model.Plots {
			out[plot.Name] = docPlotBinding{
				Plot:     plot,
				Subtitle: subtitle,
				Runtime:  runtime,
			}
		}
	}
	add(messageModel, "Runtime trace of the message-chain example.", docMessageRuntime)
	add(queueModel, "Runtime trace of the M/M/1/5-style queue example.", docQueueRuntime)
	return out, nil
}

func renderDocLanguageSections() (string, error) {
	modelForms := []languageFormDoc{
		{Form: "`(model $item:form...)`", Params: "`$item := actor | instance | assert | xyplot`", Semantics: "Top-level container. Nothing is created implicitly."},
		{Form: "`(actor $role:symbol $item:form...)`", Params: "`$item := data | state`", Semantics: "Declares a reusable actor-role template."},
		{Form: "`(data $key:symbol $value:value)`", Params: "`$key` actor-local name", Semantics: "Initializes actor-local data before execution starts."},
		{Form: "`(state $name:symbol $edge:form...)`", Params: "`$name` control-state name", Semantics: "Declares one named control location. The first state is initial."},
		{Form: "`(edge $guard:guard $action:action...)`", Params: "`$guard` readiness condition", Semantics: "One atomic transition. Every branch must reach a `become`."},
		{Form: "`(instance $name:symbol $role:symbol (queue $n:int) ($peerRole:symbol $target:symbol...)...)`", Params: "`$n` mailbox capacity; `0` means rendezvous", Semantics: "Creates one runtime actor, sets its mailbox length, and fills its peer-role bindings."},
		{Form: "`(assert $p:ctl)`", Params: "`$p` CTL formula", Semantics: "Checks a branching-time requirement over the explored model."},
		{Form: "`(xyplot $name:symbol (title $title:string) (steps $n:int) (metric $m:symbol))`", Params: "`$m := sent-minus-received | send-rate | receive-rate`", Semantics: "Requests a runtime-derived line chart. Use rate-style metrics for monotone event counts."},
	}
	guardForms := []languageFormDoc{
		{Form: "`true`", Params: "none", Semantics: "Always enabled."},
		{Form: "`dice`", Params: "none", Semantics: "Shorthand for a 50/50 branch when no bounds are needed."},
		{Form: "`(mailbox $msg:value)`", Params: "`$msg` literal or quoted message", Semantics: "True when the mailbox contains a matching message."},
		{Form: "`(data= $key:symbol $value:value)`", Params: "`$key` actor-local variable", Semantics: "True when the local value equals the resolved right-hand side."},
		{Form: "`(data> $key:symbol $value:int)`", Params: "`$value` integer threshold", Semantics: "True when the local integer is strictly greater."},
		{Form: "`(dice-range $low:float $high:float)`", Params: "`0.0 ≤ $low ≤ $high ≤ 1.0`", Semantics: "True when `Dice ∈ [$low,$high]`."},
		{Form: "`(dice< $high:float)`", Params: "`$high` upper bound", Semantics: "True when `Dice < $high`."},
		{Form: "`(dice>= $low:float)`", Params: "`$low` lower bound", Semantics: "True when `Dice ≥ $low`."},
		{Form: "`(and $g:guard...)`, `(or $g:guard...)`, `(not $g:guard)`, `(implies $p:guard $q:guard)`", Params: "guard composition", Semantics: "Boolean structure over guards."},
	}
	actionForms := []languageFormDoc{
		{Form: "`(send $role:symbol $msg:value)`", Params: "`$role` must bind to exactly one target", Semantics: "Sends one message to the bound concrete actor."},
		{Form: "`(send-any $role:symbol $msg:value)`", Params: "`$role` may bind to several targets", Semantics: "Sends to the first ready target in that peer-role set."},
		{Form: "`(recv $var:symbol)`", Params: "`$var` local variable name", Semantics: "Consumes one incoming message and also stores the sender in `sender`."},
		{Form: "`(become $state:symbol)`", Params: "`$state` declared control state", Semantics: "Sets the next control location."},
		{Form: "`(set $key:symbol $value:value)`", Params: "`$key` actor-local variable", Semantics: "Writes a resolved value into actor-local data."},
		{Form: "`(add $key:symbol $delta:int)`, `(sub $key:symbol $delta:int)`", Params: "`$key` integer variable", Semantics: "Integer arithmetic on actor-local data."},
		{Form: "`(if $guard:guard $then:action [$else:action])`", Params: "guard plus action branches", Semantics: "Conditional execution inside one atomic transition."},
		{Form: "`(do $action:action...)`", Params: "explicit action sequence", Semantics: "Groups nested actions when one form is required."},
		{Form: "`(def $name:symbol ($param:symbol...) $body:value)`", Params: "actor-local pure helper", Semantics: "Defines a value helper used from value positions. `send`, `recv`, `become`, and other action forms are forbidden inside the body."},
		{Form: "`(md5 $out:symbol $source:value)`", Params: "`$out` destination variable", Semantics: "Stores an MD5 hex digest."},
		{Form: "`(rsa-raw $out:symbol $modulus:int $exponent:int $message:int)`", Params: "big-integer inputs", Semantics: "Stores `message^exponent mod modulus`."},
		{Form: "`(cryptorandom $out:symbol $bytes:int)`", Params: "`$bytes ≥ 0`", Semantics: "Stores a random hex string."},
		{Form: "`(sample-exponential $out:symbol $rate:float)`", Params: "`$rate > 0`", Semantics: "Stores an exponential variate sample."},
	}
	valueForms := []languageFormDoc{
		{Form: "bare symbol", Params: "`$var:symbol` in docs; actual model omits `$`", Semantics: "Resolves to actor-local data when present; otherwise stays a symbol literal."},
		{Form: "`'$x`, `'(a b)`", Params: "quoted literal", Semantics: "Prevents evaluation and injects a literal symbol or list."},
		{Form: "`(cons $head:value $tail:value)`", Params: "value forms", Semantics: "Prepends `$head` onto list `$tail`."},
		{Form: "`(car $xs:value)`", Params: "`$xs` list value", Semantics: "Returns the first list element, or empty/invalid when absent."},
		{Form: "`(cdr $xs:value)`", Params: "`$xs` list value", Semantics: "Returns the tail of a list."},
	}
	ctlForms := []languageFormDoc{
		{Form: "`(in-state $actor:symbol $state:symbol)`", Params: "atomic state predicate", Semantics: "Asserts `$actor.state = $state`."},
		{Form: "`(data= $actor:symbol $key:symbol $value:value)`", Params: "atomic data predicate", Semantics: "Asserts one actor-local value equals `$value`."},
		{Form: "`(mailbox-has $actor:symbol $msg:value)`", Params: "atomic mailbox predicate", Semantics: "Asserts the mailbox currently contains `$msg`."},
		{Form: "`(ex $p:ctl)`, `(ax $p:ctl)`", Params: "next-step modalities", Semantics: "`EX` and `AX`."},
		{Form: "`(ef $p:ctl)`, `(af $p:ctl)`", Params: "eventual modalities", Semantics: "`EF` and `AF`."},
		{Form: "`(eg $p:ctl)`, `(ag $p:ctl)`", Params: "global modalities", Semantics: "`EG` and `AG`."},
		{Form: "`(eu $p:ctl $q:ctl)`, `(au $p:ctl $q:ctl)`", Params: "until modalities", Semantics: "`E[p U q]` and `A[p U q]`."},
		{Form: "`(not $p:ctl)`, `(and $p:ctl $q:ctl)`, `(or $p:ctl $q:ctl)`, `(implies $p:ctl $q:ctl)`", Params: "Boolean CTL structure", Semantics: "Boolean composition over CTL formulas."},
	}
	muForms := []languageFormDoc{
		{Form: "`true`, `false`", Params: "none", Semantics: "Boolean constants for the raw modal μ-calculus layer."},
		{Form: "`(diamond $p:mu)`, `(box $p:mu)`", Params: "next-step modalities", Semantics: "Existential and universal modal operators."},
		{Form: "`(mu $X:symbol $body:mu)`, `(nu $X:symbol $body:mu)`", Params: "fixpoint variable plus body", Semantics: "Least and greatest fixpoints."},
		{Form: "`(not $p:mu)`, `(and $p:mu $q:mu)`, `(or $p:mu $q:mu)`", Params: "Boolean mu structure", Semantics: "Boolean composition over formulas."},
		{Form: "`(in-state $actor:symbol $state:symbol)`, `(data= $actor:symbol $key:symbol $value:value)`, `(mailbox-has $actor:symbol $msg:value)`", Params: "same atoms as CTL", Semantics: "State predicates shared with the CTL surface syntax."},
	}

	var b strings.Builder
	b.WriteString("# LLM Authoring Prompt\n\n")
	b.WriteString("```text\n")
	b.WriteString("Write a go-ctl2 model as Lisp.\n")
	b.WriteString("Use exactly one top-level (model ...).\n")
	b.WriteString("Declare reusable behavior with (actor RoleName ...).\n")
	b.WriteString("Declare concrete runtime actors with (instance Name Role (queue n) (PeerRole Target...)...).\n")
	b.WriteString("Every instance must declare its mailbox length with `(queue n)`. Use `0` for rendezvous and a positive integer for buffered mailboxes.\n")
	b.WriteString("There is no implicit actor creation, implicit topology, or implicit next state.\n")
	b.WriteString("Model the situation as a state machine, not as loose propositions.\n")
	b.WriteString("For real-world scenarios such as wars, elections, outages, or negotiations: define actors for the participants, explicit states for phases, and messages or random branches for external events.\n")
	b.WriteString("Only assert propositions that are grounded in named states, mailbox contents, or actor-local data.\n")
	b.WriteString("Every send target is written as a peer role in the actor definition and must resolve through the instance bindings.\n")
	b.WriteString("Use (send Role msg) only when that role resolves to exactly one concrete actor.\n")
	b.WriteString("Use (send-any Role msg) when a role may resolve to several concrete actors.\n")
	b.WriteString("State is actor-local. The only cross-actor effect is messaging.\n")
	b.WriteString("Each transition is (edge guard action...) inside a declared (state ...).\n")
	b.WriteString("Every edge must reach at least one (become State).\n")
	b.WriteString("Use (recv var) to consume a message. recv also writes the sender name into local variable sender.\n")
	b.WriteString("Use quoted literals for structured messages, for example '(message (type strike) (target refinery)).\n")
	b.WriteString("Prefer short named states such as idle, mobilizing, negotiating, retaliating, ceasefire, failed.\n")
	b.WriteString("Put branching-time requirements in (assert ...).\n")
	b.WriteString("Use only the forms documented below.\n")
	b.WriteString("```")
	b.WriteString("\n\n# Language Reference\n\n")
	b.WriteString("Documentation metavariables start with `$` and include a type tag, for example `$count:int` or `$msg:value`. Actual models do not include the `$`; write `count` and `msg` in the Lisp itself.\n\n")
	writeLanguageTable(&b, "## Core Model Forms", modelForms)
	writeLanguageTable(&b, "## Guard Forms", guardForms)
	writeLanguageTable(&b, "## Action Forms", actionForms)
	writeLanguageTable(&b, "## Value Forms", valueForms)
	b.WriteString("## Branching-Time Logic Forms\n\n")
	writeLanguageTable(&b, "### CTL Surface Forms", ctlForms)
	writeLanguageTable(&b, "### Raw Modal μ-Calculus Forms", muForms)
	return b.String(), nil
}

func writeLanguageTable(b *strings.Builder, title string, rows []languageFormDoc) {
	b.WriteString(title)
	b.WriteString("\n\n| Form | Metavariables | Operational Semantics |\n| --- | --- | --- |\n")
	for _, row := range rows {
		fmt.Fprintf(b, "| %s | %s | %s |\n", row.Form, row.Params, row.Semantics)
	}
	b.WriteString("\n")
}

func docExamples() ([]docExample, error) {
	queueModel, err := docQueueModel()
	if err != nil {
		return nil, err
	}
	messageModel, err := docMessageModel()
	if err != nil {
		return nil, err
	}
	bakeryModel, err := docBakeryModel()
	if err != nil {
		return nil, err
	}
	return []docExample{
		{Title: "Message Chain Example", Source: strings.TrimSpace(docMessageModelSource), Spec: messageModel},
		{Title: "Queue Example", Source: strings.TrimSpace(docQueueModelSource), Spec: queueModel},
		{Title: "Bakery Role-Reuse Example", Source: strings.TrimSpace(docBakeryModelSource), Spec: bakeryModel},
	}, nil
}

func renderDocExampleMarkdown(item docExample) (string, error) {
	var b strings.Builder
	b.WriteString("#### State Diagram\n\n```mermaid\n")
	b.WriteString(renderStateDiagramMermaid(item.Spec))
	b.WriteString("```\n\n#### Message Diagram\n\n```mermaid\n")
	b.WriteString(renderSequenceDiagramMermaid(item.Spec))
	b.WriteString("```\n\n#### Class Diagram\n\n```mermaid\n")
	b.WriteString(renderClassDiagramMermaid(item.Spec))
	b.WriteString("```\n\n")

	if len(item.Spec.Assertions) > 0 {
		results, err := item.Spec.CheckAssertions()
		if err != nil {
			return "", err
		}
		if len(results) > 0 {
			b.WriteString("#### CTL Outcomes\n\n")
			for _, result := range results {
				status := "FAIL"
				if result.Holds {
					status = "PASS"
				}
				fmt.Fprintf(&b, "- `%s` `%s`\n", status, result.Assertion.Spec.Items[1].String())
			}
			b.WriteString("\n")
		}
	}

	if len(item.Spec.Plots) > 0 {
		b.WriteString("#### Line Graphs\n\n")
		for _, plot := range item.Spec.Plots {
			data, err := plotDataForModel(item.Spec, plot)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "##### %s\n\n```lisp\n%s\n```\n\n```mermaid\n%s```\n\n", plot.Title, plot.Lisp().String(), renderPlotMermaid(data))
		}
	}

	channelPlots, err := mailboxPlotDataForModel(item.Spec)
	if err != nil {
		return "", err
	}
	if len(channelPlots) > 0 {
		b.WriteString("#### Channel Sizes\n\n")
		for _, data := range channelPlots {
			fmt.Fprintf(&b, "##### %s\n\n```mermaid\n%s```\n\n", data.Title, renderPlotMermaid(data))
		}
	}
	return strings.TrimSpace(b.String()), nil
}

func renderDocExampleSections() (string, error) {
	examples, err := docExamples()
	if err != nil {
		return "", err
	}

	var b strings.Builder
	for _, item := range examples {
		fmt.Fprintf(&b, "## %s\n\n### Input Lisp\n\n```lisp\n%s\n```\n\n", item.Title, item.Source)
		markdown, err := renderDocExampleMarkdown(item)
		if err != nil {
			return "", err
		}
		b.WriteString("### Rendered Output\n\n")
		b.WriteString(markdown)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String()) + "\n", nil
}

func docPlotSeries(rt *Runtime, metric string) ([]MetricPoint, string, string, error) {
	switch metric {
	case "send-count":
		series := append([]MetricPoint{{Step: 0, Value: 0}}, rt.EventCountSeries(EventSend, nil)...)
		return series, "cumulative sends", "sends", nil
	case "receive-count":
		series := append([]MetricPoint{{Step: 0, Value: 0}}, rt.EventCountSeries(EventReceive, nil)...)
		return series, "cumulative receives", "receives", nil
	case "send-rate":
		return append([]MetricPoint{{Step: 0, Value: 0}}, rt.EventRateSeries(EventSend, nil, 1)...), "sends per step", "send rate", nil
	case "receive-rate":
		return append([]MetricPoint{{Step: 0, Value: 0}}, rt.EventRateSeries(EventReceive, nil, 1)...), "receives per step", "receive rate", nil
	case "sent-minus-received":
		sendSeries := rt.EventCountSeries(EventSend, nil)
		receiveSeries := rt.EventCountSeries(EventReceive, nil)
		sendMap := map[int]float64{}
		recvMap := map[int]float64{}
		for _, point := range sendSeries {
			sendMap[point.Step] = point.Value
		}
		for _, point := range receiveSeries {
			recvMap[point.Step] = point.Value
		}
		out := []MetricPoint{{Step: 0, Value: 0}}
		sends := 0.0
		receives := 0.0
		for step := 1; step <= rt.Step; step++ {
			if value, ok := sendMap[step]; ok {
				sends = value
			}
			if value, ok := recvMap[step]; ok {
				receives = value
			}
			out = append(out, MetricPoint{Step: step, Value: sends - receives})
		}
		return out, "sent - received", "outstanding = sends - receives", nil
	default:
		return nil, "", "", fmt.Errorf("unsupported doc plot metric %q", metric)
	}
}

func mailboxPlotDataForModel(spec *RequirementsModel) ([]docPlotData, error) {
	steps := 32
	for _, plot := range spec.Plots {
		if plot.Steps > steps {
			steps = plot.Steps
		}
	}
	rt, err := runModelForPlotUpTo(spec, steps)
	if err != nil {
		return nil, err
	}
	var out []docPlotData
	for _, actor := range rt.Actors {
		out = append(out, docPlotData{
			Name:      diagramID(actor.Name) + "_channel_size",
			Title:     fmt.Sprintf("%s Channel Size", actor.Name),
			Subtitle:  fmt.Sprintf("%d-step runtime trace.", rt.Step),
			Steps:     rt.Step,
			Metric:    "mailbox-size",
			YLabel:    "queued messages",
			Legend:    actor.Name + " mailbox size",
			Series:    rt.MailboxSizeSeries(actor.Name),
			ImageName: diagramID(actor.Name) + "_channel_size.svg",
		})
	}
	return out, nil
}

func emitDocPlotManifest() error {
	plots, err := docPlotManifestData()
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(plots)
}

func docPlotManifestData() ([]docPlotData, error) {
	bindings, err := docPlotBindings()
	if err != nil {
		return nil, err
	}
	var plots []docPlotData
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		binding := bindings[name]
		plots = append(plots, docPlotData{
			Name:      binding.Plot.Name,
			Title:     binding.Plot.Title,
			Subtitle:  binding.Subtitle,
			Steps:     binding.Plot.Steps,
			Metric:    binding.Plot.Metric,
			ImageName: binding.Plot.Name + ".svg",
		})
	}
	return plots, nil
}

func emitDocPlotData(name string, steps int) error {
	data, err := docPlotDataByName(name, steps)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(data)
}

func runModelForPlot(spec *RequirementsModel, steps int) (*Runtime, error) {
	rt := spec.Runtime()
	rng := rand.New(rand.NewSource(7))
	rt.Dice = rng.Float64
	nextActor := 0
	rt.ChooseActorFn = func(runtime *Runtime) int {
		index := nextActor % len(runtime.Actors)
		nextActor++
		return index
	}
	maxTicks := steps*len(rt.Actors)*4 + 64
	for attempts := 0; rt.Step < steps && attempts < maxTicks; attempts++ {
		if err := rt.Tick(); err != nil {
			return nil, err
		}
	}
	if rt.Step < steps {
		return nil, fmt.Errorf("model reached only %d applied steps after %d ticks", rt.Step, maxTicks)
	}
	return rt, nil
}

func runModelForPlotUpTo(spec *RequirementsModel, steps int) (*Runtime, error) {
	rt := spec.Runtime()
	rng := rand.New(rand.NewSource(7))
	rt.Dice = rng.Float64
	nextActor := 0
	rt.ChooseActorFn = func(runtime *Runtime) int {
		index := nextActor % len(runtime.Actors)
		nextActor++
		return index
	}
	maxTicks := steps*len(rt.Actors)*4 + 64
	for attempts := 0; rt.Step < steps && attempts < maxTicks; attempts++ {
		if !rt.HasReadyStep() {
			break
		}
		if err := rt.Tick(); err != nil {
			return nil, err
		}
	}
	return rt, nil
}

func plotDataForModel(spec *RequirementsModel, plot XYPlot) (docPlotData, error) {
	rt, err := runModelForPlot(spec, plot.Steps)
	if err != nil {
		return docPlotData{}, err
	}
	series, ylabel, legend, err := docPlotSeries(rt, plot.Metric)
	if err != nil {
		return docPlotData{}, err
	}
	return docPlotData{
		Name:      plot.Name,
		Title:     plot.Title,
		Subtitle:  fmt.Sprintf("%d-step runtime trace.", rt.Step),
		Steps:     rt.Step,
		Metric:    plot.Metric,
		YLabel:    ylabel,
		Legend:    legend,
		Series:    series,
		ImageName: plot.Name + ".svg",
	}, nil
}

func renderPlotSVG(data docPlotData) string {
	width := 960.0
	height := 420.0
	marginLeft := 84.0
	marginRight := 24.0
	marginTop := 56.0
	marginBottom := 54.0
	plotW := width - marginLeft - marginRight
	plotH := height - marginTop - marginBottom
	xmax := 1.0
	if data.Steps > 0 {
		xmax = float64(data.Steps)
	}
	ymax := 1.0
	for _, point := range data.Series {
		if point.Value > ymax {
			ymax = point.Value
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, int(width), int(height), int(width), int(height))
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" fill="#0f1115"/>`)
	fmt.Fprintf(&b, `<rect x="20" y="20" width="%d" height="%d" rx="14" fill="#151922" stroke="#2a3140"/>`, int(width-40), int(height-40))
	fmt.Fprintf(&b, `<text x="%.1f" y="38" fill="#e7edf5" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="22">%s</text>`, marginLeft, html.EscapeString(data.Title))
	fmt.Fprintf(&b, `<text x="%.1f" y="58" fill="#a9b7c8" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">%s</text>`, marginLeft, html.EscapeString(data.Subtitle))
	fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#a9b7c8" stroke-width="1.5"/>`, marginLeft, marginTop, marginLeft, marginTop+plotH)
	fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#a9b7c8" stroke-width="1.5"/>`, marginLeft, marginTop+plotH, marginLeft+plotW, marginTop+plotH)
	for y := 0.0; y <= ymax; y++ {
		py := marginTop + plotH - (y/ymax)*plotH
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#2a3140" stroke-width="1"/>`, marginLeft, py, marginLeft+plotW, py)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="end" fill="#a9b7c8" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">%.0f</text>`, marginLeft-18, py+6, y)
	}
	for _, tick := range axisTicks(int(xmax)) {
		px := marginLeft + (float64(tick)/xmax)*plotW
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#2a3140" stroke-width="1"/>`, px, marginTop, px, marginTop+plotH)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="middle" fill="#a9b7c8" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">%d</text>`, px, marginTop+plotH+28, tick)
	}
	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="middle" fill="#a9b7c8" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">applied runtime step</text>`, marginLeft+plotW/2, height-20)
	fmt.Fprintf(&b, `<text x="26" y="%.1f" transform="rotate(-90 26 %.1f)" text-anchor="middle" fill="#a9b7c8" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">%s</text>`, marginTop+plotH/2, marginTop+plotH/2, html.EscapeString(data.YLabel))
	fmt.Fprintf(&b, `<polyline fill="none" stroke="#8bc3ff" stroke-width="3" points="%s"/>`, svgPolyline(data.Series, marginLeft, marginTop, plotW, plotH, xmax, ymax))
	for _, point := range data.Series {
		cx := marginLeft + (float64(point.Step)/xmax)*plotW
		cy := marginTop + plotH - (point.Value/ymax)*plotH
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="3.2" fill="#8bc3ff"/>`, cx, cy)
	}
	fmt.Fprintf(&b, `<line x1="%.1f" y1="78" x2="%.1f" y2="78" stroke="#8bc3ff" stroke-width="3"/>`, width-260, width-224)
	fmt.Fprintf(&b, `<text x="%.1f" y="82" fill="#e7edf5" font-family="Iosevka Web, IBM Plex Sans, Segoe UI, sans-serif" font-size="13">%s</text>`, width-214, html.EscapeString(data.Legend))
	b.WriteString(`</svg>`)
	return b.String()
}

func svgPolyline(points []MetricPoint, x0, y0, w, h, xmax, ymax float64) string {
	var coords []string
	for _, point := range points {
		px := x0 + (float64(point.Step)/xmax)*w
		py := y0 + h - (point.Value/ymax)*h
		coords = append(coords, fmt.Sprintf("%.1f,%.1f", px, py))
	}
	return strings.Join(coords, " ")
}

func axisTicks(count int) []int {
	if count <= 10 {
		out := make([]int, count+1)
		for i := 0; i <= count; i++ {
			out[i] = i
		}
		return out
	}
	step := count / 10
	if step < 10 {
		step = 10
	}
	var ticks []int
	for i := 0; i <= count; i += step {
		ticks = append(ticks, i)
	}
	if ticks[len(ticks)-1] != count {
		ticks = append(ticks, count)
	}
	return ticks
}

func renderStateDiagramMermaid(spec *RequirementsModel) string {
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	for _, actor := range spec.Actors {
		fmt.Fprintf(&b, "    subgraph %s\n", actor.Name)
		b.WriteString("        direction TB\n")
		for _, state := range actor.States {
			for _, transition := range state.Transitions {
				for _, next := range transition.NextStates {
					fmt.Fprintf(&b, "        %s([%s]) -->|\"%s\"| %s([%s])\n",
						diagramID(actor.Name, state.Name), state.Name, mermaidLabel(transitionLabel(transition)), diagramID(actor.Name, next), next)
				}
			}
		}
		b.WriteString("    end\n")
	}
	return b.String()
}

func renderSequenceDiagramMermaid(spec *RequirementsModel) string {
	var b strings.Builder
	b.WriteString("sequenceDiagram\n")
	for _, actor := range spec.Actors {
		fmt.Fprintf(&b, "    participant %s\n", actor.Name)
	}
	seen := map[string]bool{}
	for _, actor := range spec.Actors {
		for _, state := range actor.States {
			for _, transition := range state.Transitions {
				for _, item := range actionItems(transition.ActionSpec) {
					if !isList(item) || len(item.Items) < 3 {
						continue
					}
					head, err := expectSymbol(item.Items[0], "action operator")
					if err != nil {
						continue
					}
					var targets []string
					var message Value
					switch head {
					case "send":
						if len(item.Items) != 3 {
							continue
						}
						target, err := expectSymbol(item.Items[1], "send target")
						if err != nil {
							continue
						}
						targets = []string{target}
						message = item.Items[2]
					case "send-any":
						if len(item.Items) < 4 {
							continue
						}
						for _, targetForm := range item.Items[1 : len(item.Items)-1] {
							target, err := expectSymbol(targetForm, "send-any target")
							if err != nil {
								targets = nil
								break
							}
							targets = append(targets, target)
						}
						message = item.Items[len(item.Items)-1]
					default:
						continue
					}
					for _, target := range targets {
						key := actor.Name + "->" + target + ":" + message.String()
						if seen[key] {
							continue
						}
						seen[key] = true
						fmt.Fprintf(&b, "    %s-->>%s: %s\n", actor.Name, target, message.String())
					}
				}
			}
		}
	}
	return b.String()
}

type actorVariableUsage struct {
	All    []string
	Reads  map[string]bool
	Writes map[string]bool
}

func renderClassDiagramMermaid(spec *RequirementsModel) string {
	var b strings.Builder
	b.WriteString("classDiagram\n")
	for _, actor := range spec.Actors {
		usage := analyzeActorVariableUsage(actor)
		fmt.Fprintf(&b, "    class %s {\n", diagramID(actor.Name))
		for _, state := range actor.States {
			if state.Control {
				fmt.Fprintf(&b, "        +%s : state\n", diagramID(state.Name))
			}
		}
		for _, name := range usage.All {
			fmt.Fprintf(&b, "        +%s : data\n", diagramID(name))
		}
		b.WriteString("    }\n")
		if actor.Role != "" {
			fmt.Fprintf(&b, "    <<%s>> %s\n", actor.Role, diagramID(actor.Name))
		}
	}
	return b.String()
}

func analyzeActorVariableUsage(actor *Actor) actorVariableUsage {
	writes := map[string]bool{"state": true}
	for key := range actor.Data {
		writes[key] = true
	}
	for _, state := range actor.States {
		for _, transition := range state.Transitions {
			collectActionWrites(transition.ActionSpec, writes)
		}
	}
	knownVars := copyStateSet(writes)
	reads := map[string]bool{}
	for _, state := range actor.States {
		for _, transition := range state.Transitions {
			collectGuardReads(transition.GuardSpec, knownVars, reads)
			collectActionReadsWrites(transition.ActionSpec, knownVars, reads, writes)
		}
	}
	all := sortedStateNames(writes)
	return actorVariableUsage{
		All:    all,
		Reads:  reads,
		Writes: writes,
	}
}

func sortedStateNames(in map[string]bool) []string {
	names := make([]string, 0, len(in))
	for name := range in {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func collectActionWrites(form Value, writes map[string]bool) {
	if !isList(form) || len(form.Items) == 0 {
		return
	}
	head, err := expectSymbol(form.Items[0], "action operator")
	if err != nil {
		return
	}
	switch head {
	case "do":
		for _, item := range form.Items[1:] {
			collectActionWrites(item, writes)
		}
	case "recv", "become", "set", "add", "sub", "md5", "rsa-raw", "cryptorandom", "sample-exponential":
		if len(form.Items) < 2 || form.Items[1].Kind != KindSymbol {
			return
		}
		name := form.Items[1].Text
		if head == "become" {
			name = "state"
		}
		writes[name] = true
		if head == "recv" {
			writes["sender"] = true
		}
	case "if":
		for _, item := range form.Items[2:] {
			collectActionWrites(item, writes)
		}
	}
}

func collectGuardReads(form Value, knownVars, reads map[string]bool) {
	if form.Kind == KindSymbol || form.Kind == KindBool || !isList(form) || len(form.Items) == 0 {
		return
	}
	head, err := expectSymbol(form.Items[0], "guard operator")
	if err != nil {
		return
	}
	switch head {
	case "and", "or":
		items := stripOptionalDescription(form.Items, 3)
		for _, item := range items[1:] {
			collectGuardReads(item, knownVars, reads)
		}
	case "not":
		items := stripOptionalDescription(form.Items, 2)
		if len(items) == 2 {
			collectGuardReads(items[1], knownVars, reads)
		}
	case "implies", "->":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) == 3 {
			collectGuardReads(items[1], knownVars, reads)
			collectGuardReads(items[2], knownVars, reads)
		}
	case "data=", "data>":
		items := stripOptionalDescription(form.Items, 3)
		if len(items) == 3 && items[1].Kind == KindSymbol {
			reads[items[1].Text] = true
			collectValueReads(items[2], knownVars, reads, nil)
		}
	}
}

func collectActionReadsWrites(form Value, knownVars, reads, writes map[string]bool) {
	if !isList(form) || len(form.Items) == 0 {
		return
	}
	head, err := expectSymbol(form.Items[0], "action operator")
	if err != nil {
		return
	}
	switch head {
	case "do":
		for _, item := range form.Items[1:] {
			collectActionReadsWrites(item, knownVars, reads, writes)
		}
	case "send", "send-any":
		if len(form.Items) == 3 {
			collectValueReads(form.Items[2], knownVars, reads, nil)
		} else if head == "send-any" && len(form.Items) >= 4 {
			collectValueReads(form.Items[len(form.Items)-1], knownVars, reads, nil)
		}
	case "recv":
		if len(form.Items) == 2 && form.Items[1].Kind == KindSymbol {
			writes[form.Items[1].Text] = true
			writes["sender"] = true
		}
	case "become":
		writes["state"] = true
	case "set":
		if len(form.Items) == 3 && form.Items[1].Kind == KindSymbol {
			writes[form.Items[1].Text] = true
			collectValueReads(form.Items[2], knownVars, reads, nil)
		}
	case "def":
		if len(form.Items) == 4 && isList(form.Items[2]) {
			params := map[string]bool{}
			for _, item := range form.Items[2].Items {
				if item.Kind == KindSymbol {
					params[item.Text] = true
				}
			}
			collectValueReads(form.Items[3], knownVars, reads, params)
		}
	case "if":
		if len(form.Items) >= 3 {
			collectGuardReads(form.Items[1], knownVars, reads)
			collectActionReadsWrites(form.Items[2], knownVars, reads, writes)
		}
		if len(form.Items) == 4 {
			collectActionReadsWrites(form.Items[3], knownVars, reads, writes)
		}
	case "add", "sub":
		if len(form.Items) == 3 && form.Items[1].Kind == KindSymbol {
			key := form.Items[1].Text
			reads[key] = true
			writes[key] = true
			collectValueReads(form.Items[2], knownVars, reads, nil)
		}
	case "md5", "cryptorandom", "sample-exponential":
		if len(form.Items) >= 2 && form.Items[1].Kind == KindSymbol {
			writes[form.Items[1].Text] = true
		}
		for _, item := range form.Items[2:] {
			collectValueReads(item, knownVars, reads, nil)
		}
	case "rsa-raw":
		if len(form.Items) == 5 && form.Items[1].Kind == KindSymbol {
			writes[form.Items[1].Text] = true
		}
		for _, item := range form.Items[2:] {
			collectValueReads(item, knownVars, reads, nil)
		}
	}
}

func collectValueReads(form Value, knownVars, reads, locals map[string]bool) {
	switch form.Kind {
	case KindSymbol:
		if locals != nil && locals[form.Text] {
			return
		}
		if knownVars[form.Text] {
			reads[form.Text] = true
		}
	case KindList:
		if len(form.Items) == 0 {
			return
		}
		if isListHead(form, "quote") {
			return
		}
		for _, item := range form.Items[1:] {
			collectValueReads(item, knownVars, reads, locals)
		}
	}
}

func diagramID(parts ...string) string {
	var b strings.Builder
	for _, part := range parts {
		for _, r := range part {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(r)
			} else {
				b.WriteRune('_')
			}
		}
	}
	return b.String()
}

func transitionLabel(transition Transition) string {
	var parts []string
	if transition.GuardSpec.String() != "true" {
		parts = append(parts, transition.GuardSpec.String())
	}
	for _, item := range actionItems(transition.ActionSpec) {
		if isListHead(item, "become") {
			continue
		}
		parts = append(parts, item.String())
	}
	if len(parts) == 0 {
		return "transition"
	}
	return strings.Join(parts, "\n")
}

func mermaidLabel(text string) string {
	return strings.ReplaceAll(html.EscapeString(text), "\n", "<br/>")
}

func formatPlotMermaidNumber(value float64) string {
	if math.Abs(value-math.Round(value)) < 1e-9 {
		return strconv.FormatInt(int64(math.Round(value)), 10)
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func renderPlotMermaid(data docPlotData) string {
	minValue := 0.0
	maxValue := 0.0
	steps := make([]string, 0, len(data.Series))
	values := make([]string, 0, len(data.Series))
	for _, point := range data.Series {
		if point.Value < minValue {
			minValue = point.Value
		}
		if point.Value > maxValue {
			maxValue = point.Value
		}
		steps = append(steps, strconv.Itoa(point.Step))
		values = append(values, formatPlotMermaidNumber(point.Value))
	}
	if minValue == maxValue {
		if minValue == 0 {
			maxValue = 1
		} else if minValue > 0 {
			minValue = 0
		} else {
			maxValue = 0
		}
	}

	var b strings.Builder
	b.WriteString("xychart-beta\n")
	fmt.Fprintf(&b, "    title %q\n", data.Title)
	fmt.Fprintf(&b, "    x-axis %q [%s]\n", "applied runtime step", strings.Join(steps, ", "))
	fmt.Fprintf(&b, "    y-axis %q %s --> %s\n", data.YLabel, formatPlotMermaidNumber(minValue), formatPlotMermaidNumber(maxValue))
	fmt.Fprintf(&b, "    line [%s]\n", strings.Join(values, ", "))
	return b.String()
}

func renderRequirementsMarkdown(spec *RequirementsModel) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Requirements Model\n\n```lisp\n%s\n```\n\n", spec.Lisp().String())
	b.WriteString("## State Diagram\n\n```mermaid\n")
	b.WriteString(renderStateDiagramMermaid(spec))
	b.WriteString("```\n\n## Message Diagram\n\n```mermaid\n")
	b.WriteString(renderSequenceDiagramMermaid(spec))
	b.WriteString("```\n\n## Class Diagram\n\n```mermaid\n")
	b.WriteString(renderClassDiagramMermaid(spec))
	b.WriteString("```\n\n")
	if len(spec.Assertions) > 0 {
		results, err := spec.CheckAssertions()
		if err != nil {
			return "", err
		}
		if len(results) > 0 {
			b.WriteString("## Assertions\n\n")
			for _, result := range results {
				status := "FAIL"
				if result.Holds {
					status = "PASS"
				}
				fmt.Fprintf(&b, "- `%s` `%s`\n", status, result.Assertion.Spec.Items[1].String())
			}
			b.WriteString("\n")
		}
	}
	if len(spec.Plots) > 0 {
		b.WriteString("## Line Graphs\n\n")
		for _, plot := range spec.Plots {
			data, err := plotDataForModel(spec, plot)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "### %s\n\n```lisp\n%s\n```\n\n```mermaid\n%s```\n\n", plot.Title, plot.Lisp().String(), renderPlotMermaid(data))
		}
	}
	channelPlots, err := mailboxPlotDataForModel(spec)
	if err != nil {
		return "", err
	}
	if len(channelPlots) > 0 {
		b.WriteString("## Channel Sizes\n\n")
		for _, data := range channelPlots {
			fmt.Fprintf(&b, "### %s\n\n```mermaid\n%s```\n\n", data.Title, renderPlotMermaid(data))
		}
	}
	return b.String(), nil
}

func renderRequirementsHTML(spec *RequirementsModel) (string, error) {
	var b strings.Builder
	b.WriteString("<!doctype html><html><head><meta charset=\"utf-8\"><title>Requirements Model</title></head><body>")
	b.WriteString("<h1>Requirements Model</h1>")
	fmt.Fprintf(&b, "<pre><code>%s</code></pre>", html.EscapeString(spec.Lisp().String()))
	fmt.Fprintf(&b, "<h2>State Diagram</h2><pre><code class=\"language-mermaid\">%s</code></pre>", html.EscapeString(renderStateDiagramMermaid(spec)))
	fmt.Fprintf(&b, "<h2>Message Diagram</h2><pre><code class=\"language-mermaid\">%s</code></pre>", html.EscapeString(renderSequenceDiagramMermaid(spec)))
	fmt.Fprintf(&b, "<h2>Class Diagram</h2><pre><code class=\"language-mermaid\">%s</code></pre>", html.EscapeString(renderClassDiagramMermaid(spec)))
	if len(spec.Assertions) > 0 {
		results, err := spec.CheckAssertions()
		if err != nil {
			return "", err
		}
		if len(results) > 0 {
			b.WriteString("<h2>Assertions</h2><ul>")
			for _, result := range results {
				status := "FAIL"
				if result.Holds {
					status = "PASS"
				}
				fmt.Fprintf(&b, "<li><code>%s</code> <code>%s</code></li>", status, html.EscapeString(result.Assertion.Spec.Items[1].String()))
			}
			b.WriteString("</ul>")
		}
	}
	if len(spec.Plots) > 0 {
		b.WriteString("<h2>Line Graphs</h2>")
		for _, plot := range spec.Plots {
			data, err := plotDataForModel(spec, plot)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "<h3>%s</h3><pre><code>%s</code></pre><pre><code class=\"language-mermaid\">%s</code></pre>", html.EscapeString(plot.Title), html.EscapeString(plot.Lisp().String()), html.EscapeString(renderPlotMermaid(data)))
		}
	}
	channelPlots, err := mailboxPlotDataForModel(spec)
	if err != nil {
		return "", err
	}
	if len(channelPlots) > 0 {
		b.WriteString("<h2>Channel Sizes</h2>")
		for _, data := range channelPlots {
			fmt.Fprintf(&b, "<h3>%s</h3><pre><code class=\"language-mermaid\">%s</code></pre>", html.EscapeString(data.Title), html.EscapeString(renderPlotMermaid(data)))
		}
	}
	b.WriteString("</body></html>")
	return b.String(), nil
}

func docPlotDataByName(name string, steps int) (docPlotData, error) {
	bindings, err := docPlotBindings()
	if err != nil {
		return docPlotData{}, err
	}
	binding, ok := bindings[name]
	if !ok {
		return docPlotData{}, fmt.Errorf("unknown doc plot %q", name)
	}
	plot := binding.Plot
	if steps > 0 {
		plot.Steps = steps
	}
	rt, err := binding.Runtime(plot.Steps)
	if err != nil {
		return docPlotData{}, err
	}
	series, ylabel, legend, err := docPlotSeries(rt, plot.Metric)
	if err != nil {
		return docPlotData{}, err
	}
	data := docPlotData{
		Name:      plot.Name,
		Title:     plot.Title,
		Subtitle:  fmt.Sprintf("%d-step %s", rt.Step, binding.Subtitle),
		Steps:     rt.Step,
		Metric:    plot.Metric,
		YLabel:    ylabel,
		Legend:    legend,
		Series:    series,
		ImageName: plot.Name + ".svg",
	}
	return data, nil
}

func main() {
	if handled, code := runFlagMode(os.Args[1:]); handled {
		if code != 0 {
			os.Exit(code)
		}
		return
	}
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "serve":
			addr := "127.0.0.1:8080"
			if len(os.Args) >= 3 {
				addr = os.Args[2]
			}
			fmt.Fprintf(os.Stderr, "serving go-ctl2 on http://%s\n", addr)
			if err := serveWeb(addr); err != nil {
				fmt.Fprintf(os.Stderr, "serve: %v\n", err)
				os.Exit(1)
			}
		case "render-markdown", "render-html":
			src, err := io.ReadAll(os.Stdin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "render: %v\n", err)
				os.Exit(1)
			}
			spec, err := CompileModel(string(src))
			if err != nil {
				fmt.Fprintf(os.Stderr, "render: %v\n", err)
				os.Exit(1)
			}
			var out string
			if os.Args[1] == "render-html" {
				out, err = renderRequirementsHTML(spec)
			} else {
				out, err = renderRequirementsMarkdown(spec)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "render: %v\n", err)
				os.Exit(1)
			}
			fmt.Print(out)
		case "interpret":
			src, err := io.ReadAll(os.Stdin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "interpret: %v\n", err)
				os.Exit(1)
			}
			markdown, _, err := renderInterpretation(string(src))
			if err != nil {
				fmt.Fprintf(os.Stderr, "interpret: %v\n", err)
				os.Exit(1)
			}
			fmt.Print(markdown)
		case "doc-xyplots-manifest":
			if err := emitDocPlotManifest(); err != nil {
				fmt.Fprintf(os.Stderr, "doc-xyplots-manifest: %v\n", err)
				os.Exit(1)
			}
		case "doc-xyplot-data":
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "usage: doc-xyplot-data <plot-name> [steps]")
				os.Exit(1)
			}
			steps := 0
			if len(os.Args) >= 4 {
				value, err := strconv.Atoi(os.Args[3])
				if err != nil {
					fmt.Fprintf(os.Stderr, "invalid step count %q: %v\n", os.Args[3], err)
					os.Exit(1)
				}
				steps = value
			}
			if err := emitDocPlotData(os.Args[2], steps); err != nil {
				fmt.Fprintf(os.Stderr, "doc-xyplot-data: %v\n", err)
				os.Exit(1)
			}
		case "doc-language-sections":
			out, err := renderDocLanguageSections()
			if err != nil {
				fmt.Fprintf(os.Stderr, "doc-language-sections: %v\n", err)
				os.Exit(1)
			}
			if len(os.Args) >= 3 {
				if err := os.WriteFile(os.Args[2], []byte(out), 0644); err != nil {
					fmt.Fprintf(os.Stderr, "doc-language-sections: %v\n", err)
					os.Exit(1)
				}
			} else {
				fmt.Print(out)
			}
		case "doc-example-sections":
			out, err := renderDocExampleSections()
			if err != nil {
				fmt.Fprintf(os.Stderr, "doc-example-sections: %v\n", err)
				os.Exit(1)
			}
			if len(os.Args) >= 3 {
				if err := os.WriteFile(os.Args[2], []byte(out), 0644); err != nil {
					fmt.Fprintf(os.Stderr, "doc-example-sections: %v\n", err)
					os.Exit(1)
				}
			} else {
				fmt.Print(out)
			}
		}
	}
}
