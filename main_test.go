package main

import "testing"

func TestReadAcceptsManyExpressions(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "symbol", input: "actor", want: "actor"},
		{name: "number", input: "42", want: "42"},
		{name: "negative number", input: "-7", want: "-7"},
		{name: "string", input: `"hello"`, want: `"hello"`},
		{name: "bool true", input: "true", want: "true"},
		{name: "bool false", input: "false", want: "false"},
		{name: "empty list", input: "()", want: "()"},
		{name: "one item list", input: "(loop)", want: "(loop)"},
		{name: "plain s expr", input: "(send actor message)", want: "(send actor message)"},
		{name: "nested s expr", input: "(when (= kind \"tick\"))", want: "(when (= kind \"tick\"))"},
		{name: "operator head", input: "(= kind \"tick\")", want: "(= kind \"tick\")"},
		{name: "math symbols", input: "(+ 1 2)", want: "(+ 1 2)"},
		{name: "comparison symbols", input: "(<= retries 3)", want: "(<= retries 3)"},
		{name: "m expr simple", input: "send(actor, message)", want: "(send actor message)"},
		{name: "m expr no spaces", input: "send(actor,message)", want: "(send actor message)"},
		{name: "m expr with empty args", input: "now()", want: "(now)"},
		{name: "m expr nested", input: "when(=(kind, \"tick\"))", want: "(when (= kind \"tick\"))"},
		{name: "mixed syntax list body", input: "(receive mailbox when(=(kind, \"tick\")))", want: "(receive mailbox (when (= kind \"tick\")))"},
		{name: "mixed syntax m expr args", input: "send(actor, (payload kind value))", want: "(send actor (payload kind value))"},
		{name: "list as first m expr arg", input: "emit((quote hello), actor)", want: "(emit (quote hello) actor)"},
		{name: "deep nesting", input: "(begin (receive mailbox when(=(kind, \"tick\"))) (send worker (payload \"ok\")))", want: "(begin (receive mailbox (when (= kind \"tick\"))) (send worker (payload \"ok\")))"},
		{name: "symbol punctuation", input: "guard_ok?", want: "guard_ok?"},
		{name: "operator m expr head", input: "=(kind, \"tick\")", want: "(= kind \"tick\")"},
		{name: "nested operator m expr", input: "and(=(kind, \"tick\"), >(retries, 0))", want: "(and (= kind \"tick\") (> retries 0))"},
		{name: "whitespace before paren stays list", input: "(receive mailbox when (= kind \"tick\"))", want: "(receive mailbox when (= kind \"tick\"))"},
		{name: "quoted symbol", input: "'actor", want: "(quote actor)"},
		{name: "quoted number", input: "'42", want: "(quote 42)"},
		{name: "quoted string", input: "'\"hello\"", want: "(quote \"hello\")"},
		{name: "quoted empty list", input: "'()", want: "(quote ())"},
		{name: "quoted s expr", input: "'(send actor message)", want: "(quote (send actor message))"},
		{name: "quoted m expr", input: "'send(actor, message)", want: "(quote (send actor message))"},
		{name: "quote inside list", input: "(begin 'actor '(send actor message))", want: "(begin (quote actor) (quote (send actor message)))"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := Read(tc.input)
			if err != nil {
				t.Fatalf("Read(%q) returned error: %v", tc.input, err)
			}
			if got.String() != tc.want {
				t.Fatalf("Read(%q) = %s, want %s", tc.input, got.String(), tc.want)
			}
		})
	}
}

func TestReadSExprSymbolList(t *testing.T) {
	got, err := Read("(send actor message)")
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}

	want := List(
		Symbol("send"),
		Symbol("actor"),
		Symbol("message"),
	)

	if got.String() != want.String() {
		t.Fatalf("got %s, want %s", got.String(), want.String())
	}
}

func TestReadMExprNormalizesToList(t *testing.T) {
	got, err := Read("send(actor, message)")
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}

	want := List(
		Symbol("send"),
		Symbol("actor"),
		Symbol("message"),
	)

	if got.String() != want.String() {
		t.Fatalf("got %s, want %s", got.String(), want.String())
	}
}

func TestReadNestedMixedSyntax(t *testing.T) {
	got, err := Read("(receive mailbox (when (= kind \"tick\")))")
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}

	want := "(receive mailbox (when (= kind \"tick\")))"
	if got.String() != want {
		t.Fatalf("got %s, want %s", got.String(), want)
	}
}

func TestReadNestedMExprInsideSExpr(t *testing.T) {
	got, err := Read("(receive mailbox when(=(kind, \"tick\")))")
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}

	want := "(receive mailbox (when (= kind \"tick\")))"
	if got.String() != want {
		t.Fatalf("got %s, want %s", got.String(), want)
	}
}

func TestReadRejectsUnterminatedInput(t *testing.T) {
	_, err := Read("(send actor")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestReadRejectsBareQuote(t *testing.T) {
	_, err := Read("'")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRuntimeMessageChainABC(t *testing.T) {
	ping := Symbol("ping")

	runtime := NewRuntime(
		&Actor{
			Name: "A",
			States: []State{
				{
					Name:  "start",
					Guard: func(*Runtime, *Actor) bool { return true },
					Transitions: []Transition{
						{
							Name: "send-to-b",
							Guard: func(rt *Runtime, actor *Actor) bool {
								return actor.Data["sent"].String() != "true"
							},
							Action: func(rt *Runtime, actor *Actor) error {
								actor.Data["sent"] = Bool("true")
								return Send("B", ping)(rt, actor)
							},
						},
					},
				},
			},
		},
		&Actor{
			Name: "B",
			States: []State{
				{
					Name:  "relay",
					Guard: func(*Runtime, *Actor) bool { return true },
					Transitions: []Transition{
						{
							Name: "recv-from-a-send-to-c",
							Guard: func(rt *Runtime, actor *Actor) bool {
								for _, msg := range rt.Mailbox(actor.Name) {
									if msg.String() == ping.String() {
										return true
									}
								}
								return false
							},
							Action: Receive(MatchMessage(ping), func(rt *Runtime, actor *Actor, message Value) error {
								return Send("C", message)(rt, actor)
							}),
						},
					},
				},
			},
		},
		&Actor{
			Name: "C",
			States: []State{
				{
					Name:  "sink",
					Guard: func(*Runtime, *Actor) bool { return true },
					Transitions: []Transition{
						{
							Name: "recv-from-b",
							Guard: func(rt *Runtime, actor *Actor) bool {
								for _, msg := range rt.Mailbox(actor.Name) {
									if msg.String() == ping.String() {
										return true
									}
								}
								return false
							},
							Action: Receive(MatchMessage(ping), func(rt *Runtime, actor *Actor, message Value) error {
								actor.Data["received"] = message
								return nil
							}),
						},
					},
				},
			},
		},
	)

	if !runtime.HasReadyStep() {
		t.Fatal("expected an initial ready step")
	}

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("step A returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected A step to apply")
	}
	if len(runtime.Mailbox("B")) != 1 {
		t.Fatalf("expected B mailbox length 1, got %d", len(runtime.Mailbox("B")))
	}

	applied, err = runtime.StepActor(runtime.Actors[1])
	if err != nil {
		t.Fatalf("step B returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected B step to apply")
	}
	if len(runtime.Mailbox("B")) != 0 {
		t.Fatalf("expected B mailbox to be empty, got %d", len(runtime.Mailbox("B")))
	}
	if len(runtime.Mailbox("C")) != 1 {
		t.Fatalf("expected C mailbox length 1, got %d", len(runtime.Mailbox("C")))
	}

	applied, err = runtime.StepActor(runtime.Actors[2])
	if err != nil {
		t.Fatalf("step C returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected C step to apply")
	}
	if len(runtime.Mailbox("C")) != 0 {
		t.Fatalf("expected C mailbox to be empty, got %d", len(runtime.Mailbox("C")))
	}
	if got := runtime.Actors[2].Data["received"].String(); got != ping.String() {
		t.Fatalf("expected C to receive %s, got %s", ping.String(), got)
	}
	if runtime.HasReadyStep() {
		t.Fatal("did not expect any ready step after the chain completed")
	}
}

func TestCompileActorAndRunMessageChainABC(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(on true
						(next done)
						(send B ping)
						(become done)))
				(state done))
		`),
		MustCompileActor(`
			(actor B
				(state relay
					(on (mailbox ping)
						(next relay)
						(recv ping)
						(send C ping))))
		`),
		MustCompileActor(`
			(actor C
				(state sink
					(on (mailbox ping)
						(next sink)
						(recv ping)
						(set received ping))))
		`),
	)

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("step A returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected A step to apply")
	}
	if got := len(runtime.Mailbox("B")); got != 1 {
		t.Fatalf("expected B mailbox length 1, got %d", got)
	}

	applied, err = runtime.StepActor(runtime.Actors[1])
	if err != nil {
		t.Fatalf("step B returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected B step to apply")
	}
	if got := len(runtime.Mailbox("B")); got != 0 {
		t.Fatalf("expected B mailbox length 0, got %d", got)
	}
	if got := len(runtime.Mailbox("C")); got != 1 {
		t.Fatalf("expected C mailbox length 1, got %d", got)
	}

	applied, err = runtime.StepActor(runtime.Actors[2])
	if err != nil {
		t.Fatalf("step C returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected C step to apply")
	}
	if got := runtime.Actors[2].Data["received"].String(); got != "ping" {
		t.Fatalf("expected C to receive ping, got %s", got)
	}
}

func TestCompileActorRejectsUnknownAction(t *testing.T) {
	_, err := CompileActor(`
		(actor A
			(state start
				(on true
					(next start)
					(explode now))))
	`)
	if err == nil {
		t.Fatal("expected compile error, got nil")
	}
}

func TestTickLogsSchedulerError(t *testing.T) {
	runtime := NewRuntime(MustCompileActor(`
		(actor A
			(state start
				(on true (next start) (send B ping))))
	`))
	runtime.ChooseActorFn = func(*Runtime) int { return 99 }

	err := runtime.Tick()
	if err == nil {
		t.Fatal("expected tick error, got nil")
	}
	if len(runtime.Trace) == 0 {
		t.Fatal("expected tick error to be logged in trace")
	}
}

func TestCTLOnMessageChainABC(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(on true
						(next done)
						(send B ping)
						(become done)))
				(state done))
		`),
		MustCompileActor(`
			(actor B
				(state relay
					(on (mailbox ping)
						(next relay)
						(recv ping)
						(send C ping))))
		`),
		MustCompileActor(`
			(actor C
				(state sink
					(on (mailbox ping)
						(next sink)
						(recv ping)
						(set received ping))))
		`),
	)

	model, err := ExploreModel(runtime)
	if err != nil {
		t.Fatalf("ExploreModel returned error: %v", err)
	}

	cases := []struct {
		name    string
		formula string
		want    bool
	}{
		{name: "ex mailbox b ping", formula: "(ex (mailbox-has B ping))", want: true},
		{name: "ef c received ping", formula: "(ef (data= C received ping))", want: true},
		{name: "af c received ping", formula: "(af (data= C received ping))", want: true},
		{name: "ag not mailbox b ping", formula: "(ag (not (mailbox-has B ping)))", want: false},
		{name: "eu not received until received", formula: "(eu (not (data= C received ping)) (data= C received ping))", want: true},
		{name: "au not received until received", formula: "(au (not (data= C received ping)) (data= C received ping))", want: true},
		{name: "ax a done", formula: "(ax (in-state A done))", want: true},
		{name: "ef a done", formula: "(ef (in-state A done))", want: true},
		{name: "eg a done", formula: "(eg (in-state A done))", want: false},
		{name: "ag c sink", formula: "(ag (in-state C sink))", want: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			formula, err := CompileCTL(tc.formula)
			if err != nil {
				t.Fatalf("CompileCTL(%q) returned error: %v", tc.formula, err)
			}
			got := model.HoldsAtInitial(formula)
			if got != tc.want {
				t.Fatalf("formula %s: got %v, want %v", tc.formula, got, tc.want)
			}
		})
	}

	if len(model.Edges) == 0 {
		t.Fatal("expected explored edges to be recorded")
	}
	want := map[string]bool{
		"A|start|true":           false,
		"B|relay|(mailbox ping)": false,
		"C|sink|(mailbox ping)":  false,
	}
	for _, edge := range model.Edges {
		key := edge.ActorName + "|" + edge.StateName + "|" + edge.TransitionName
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for key, seen := range want {
		if !seen {
			t.Fatalf("expected explored edge metadata for %s", key)
		}
	}
}

func TestCTLDeadlockSelfLoopSupportsAX(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state idle))
		`),
	)

	model, err := ExploreModel(runtime)
	if err != nil {
		t.Fatalf("ExploreModel returned error: %v", err)
	}

	cases := []struct {
		formula string
		want    bool
	}{
		{formula: "(ax (in-state A idle))", want: true},
		{formula: "(ag (in-state A idle))", want: true},
		{formula: "(ex (not (in-state A idle)))", want: false},
	}

	for _, tc := range cases {
		formula, err := CompileCTL(tc.formula)
		if err != nil {
			t.Fatalf("CompileCTL(%q) returned error: %v", tc.formula, err)
		}
		got := model.HoldsAtInitial(formula)
		if got != tc.want {
			t.Fatalf("formula %s: got %v, want %v", tc.formula, got, tc.want)
		}
	}
}

func TestCompileCTLRejectsUnknownOperator(t *testing.T) {
	_, err := CompileCTL("(eventually (in-state A done))")
	if err == nil {
		t.Fatal("expected compile error, got nil")
	}
}

func TestRuntimeErrorsOnUndeclaredSuccessor(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(on true
						(next start)
						(become done)))
				(state done))
		`),
	)

	_, err := runtime.StepActor(runtime.Actors[0])
	if err == nil {
		t.Fatal("expected undeclared successor error, got nil")
	}
}

func TestRuntimeLispSerializationForCompiledModel(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(on true
						(next done)
						(send B ping)
						(become done)))
				(state done))
		`),
		MustCompileActor(`
			(actor B
				(state relay
					(on (mailbox ping)
						(next relay)
						(recv ping)
						(send C ping))))
		`),
	)

	got := runtime.Lisp().String()
	want := `(runtime (actor A (state start (on true (next done) (send B ping) (become done))) (state done)) (current-state A start) (data A (state start)) (mailbox A) (actor B (state relay (on (mailbox ping) (next relay) (recv ping) (send C ping)))) (current-state B relay) (data B (state relay)) (mailbox B))`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestActorLispFallbackSerialization(t *testing.T) {
	actor := Actor{
		Name: "A",
		States: []State{
			{
				Name: "idle",
				Transitions: []Transition{
					{Name: "noop", NextStates: []string{"idle"}},
				},
			},
		},
	}

	got := actor.Lisp().String()
	want := "(actor A (state idle (on noop (next idle))))"
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestRuntimeChosenActorMayNoOpWithoutDeadlock(t *testing.T) {
	runtime := NewRuntime(
		&Actor{
			Name: "A",
			States: []State{
				{
					Name:  "idle",
					Guard: func(*Runtime, *Actor) bool { return true },
				},
			},
		},
		&Actor{
			Name: "B",
			States: []State{
				{
					Name:  "ready",
					Guard: func(*Runtime, *Actor) bool { return true },
					Transitions: []Transition{
						{
							Name:   "mark-ready",
							Guard:  func(*Runtime, *Actor) bool { return true },
							Action: func(rt *Runtime, actor *Actor) error { actor.Data["done"] = Bool("true"); return nil },
						},
					},
				},
			},
		},
	)

	runtime.ChooseActorFn = func(*Runtime) int { return 0 }

	if !runtime.HasReadyStep() {
		t.Fatal("expected some actor to have a ready step")
	}

	if err := runtime.Tick(); err != nil {
		t.Fatalf("tick returned error: %v", err)
	}
	if runtime.Actors[1].Data["done"].Kind != KindInvalid {
		t.Fatal("expected B to remain untouched when A was chosen")
	}
	if !runtime.HasReadyStep() {
		t.Fatal("expected runtime to remain non-deadlocked after A no-op")
	}
}

func TestRuntimeDeadlockWhenNoStepIsReady(t *testing.T) {
	runtime := NewRuntime(
		&Actor{
			Name: "A",
			States: []State{
				{
					Name:  "idle",
					Guard: func(*Runtime, *Actor) bool { return true },
				},
			},
		},
		&Actor{
			Name: "B",
			States: []State{
				{
					Name:  "waiting",
					Guard: func(*Runtime, *Actor) bool { return true },
					Transitions: []Transition{
						{
							Name: "wait-forever",
							Guard: func(rt *Runtime, actor *Actor) bool {
								return len(rt.Mailbox(actor.Name)) > 0
							},
							Action: Receive(nil, nil),
						},
					},
				},
			},
		},
	)

	if runtime.HasReadyStep() {
		t.Fatal("expected deadlock when no transitions are ready")
	}
}
