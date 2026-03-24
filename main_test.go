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
		{name: "decimal number", input: "0.5", want: "0.5"},
		{name: "negative number", input: "-7", want: "-7"},
		{name: "negative decimal", input: "-0.25", want: "-0.25"},
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
										{name: "symbol punctuation", input: "guard_ok?", want: "guard_ok?"},
				{name: "whitespace before paren stays list", input: "(receive mailbox when (= kind \"tick\"))", want: "(receive mailbox when (= kind \"tick\"))"},
		{name: "quoted symbol", input: "'actor", want: "(quote actor)"},
		{name: "quoted number", input: "'42", want: "(quote 42)"},
		{name: "quoted string", input: "'\"hello\"", want: "(quote \"hello\")"},
		{name: "quoted empty list", input: "'()", want: "(quote ())"},
		{name: "quoted s expr", input: "'(send actor message)", want: "(quote (send actor message))"},
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

func TestReadRejectsMExpr(t *testing.T) {
	_, err := Read("send(actor, message)")
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
					(edge true
						(send B ping)
						(become done)))
				(state done))
		`),
		MustCompileActor(`
			(actor B
				(state relay
					(edge true
						(recv msg)
						(send C msg)
						(become relay))))
		`),
		MustCompileActor(`
			(actor C
				(state sink
					(edge true
						(recv received)
						(become sink))))
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

	applied, err = runtime.StepActor(runtime.Actors[1])
	if err != nil {
		t.Fatalf("step B send returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected B send step to apply")
	}
	if got := len(runtime.Mailbox("C")); got != 1 {
		t.Fatalf("expected C mailbox length 1 after B send, got %d", got)
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
				(edge true
					(explode now))))
	`)
	if err == nil {
		t.Fatal("expected compile error, got nil")
	}
}

func TestCompileActorRejectsEdgeWithoutBecome(t *testing.T) {
	_, err := CompileActor(`
		(actor A
			(state start
				(edge true
					(set x 1))))
	`)
	if err == nil {
		t.Fatal("expected compile error, got nil")
	}
}

func TestTickLogsSchedulerError(t *testing.T) {
	runtime := NewRuntime(MustCompileActor(`
		(actor A
			(state start
				(edge true (send B ping) (become start))))
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
					(edge true
						(send B ping)
						(become done)))
				(state done))
		`),
		MustCompileActor(`
			(actor B
				(state relay
					(edge true
						(recv msg)
						(send C msg)
						(become relay))))
		`),
		MustCompileActor(`
			(actor C
				(state sink
					(edge true
						(recv received)
						(become sink))))
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
		{name: "implies", formula: "(implies (in-state A start) (ef (in-state C sink)))", want: true},
		{name: "and or", formula: "(and (in-state A start) (or (ef (in-state A done)) (ef (in-state C sink))))", want: true},
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
		"A|start|true":          false,
		"B|relay|true":          false,
		"B|relay__wait_0|true":  false,
		"C|sink|true":           false,
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

func TestMuCalculusReachabilityMatchesCTL(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(edge true
						(become done)))
				(state done))
		`),
	)
	model, err := ExploreModel(runtime)
	if err != nil {
		t.Fatalf("ExploreModel returned error: %v", err)
	}

	mu, err := CompileMu("(mu X (or (in-state A done) (diamond X)))")
	if err != nil {
		t.Fatalf("CompileMu returned error: %v", err)
	}
	if !model.HoldsMuAtInitial(mu) {
		t.Fatal("expected raw mu-calculus reachability formula to hold")
	}

	ctl, err := CompileCTL("(ef (in-state A done))")
	if err != nil {
		t.Fatalf("CompileCTL returned error: %v", err)
	}
	if model.HoldsAtInitial(ctl) != model.HoldsMuAtInitial(lowerCTL(ctl)) {
		t.Fatal("expected CTL lowering to mu-calculus to preserve truth at the initial state")
	}
}

func TestRuntimeErrorsOnUndeclaredSuccessor(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(edge true
						(become missing)))
				(state done))
		`),
	)

	_, err := runtime.StepActor(runtime.Actors[0])
	if err == nil {
		t.Fatal("expected undeclared successor error, got nil")
	}
}

func TestZeroCapacityMailboxRendezvous(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(edge true
						(send B ping)
						(become done)))
				(state done))
		`),
		MustCompileActor(`
			(actor B
				(state relay
					(edge true
						(recv received)
						(become got)))
				(state got))
		`),
	)
	runtime.MailboxCaps["B"] = 0

	if !runtime.HasReadyStep() {
		t.Fatal("expected rendezvous send to be ready")
	}

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("step A returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected A step to apply through rendezvous")
	}
	if got := runtime.Actors[0].Data["state"].String(); got != "done" {
		t.Fatalf("expected A to move to done, got %s", got)
	}
	if got := runtime.Actors[1].Data["state"].String(); got != "got" {
		t.Fatalf("expected B to move to got, got %s", got)
	}
	if got := runtime.Actors[1].Data["received"].String(); got != "ping" {
		t.Fatalf("expected B to receive ping, got %s", got)
	}
	if got := len(runtime.Mailbox("B")); got != 0 {
		t.Fatalf("expected zero-capacity mailbox to remain empty, got %d", got)
	}
}

func TestZeroCapacityMailboxDeadlocksWithoutReceiver(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(edge true
						(send B ping)
						(become done)))
				(state done))
		`),
		MustCompileActor(`
			(actor B
				(state idle))
		`),
	)
	runtime.MailboxCaps["B"] = 0

	if runtime.HasReadyStep() {
		t.Fatal("did not expect any ready step without a rendezvous receiver")
	}

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("step A returned error: %v", err)
	}
	if applied {
		t.Fatal("expected A to yield when no zero-capacity receiver is ready")
	}
}

func TestCompileActorNormalizesSendAfterLeadingAction(t *testing.T) {
	actor, err := CompileActor(`
		(actor A
			(state start
				(edge true
					(set before 1)
					(send B ping)
					(become done)))
			(state done))
	`)
	if err != nil {
		t.Fatalf("CompileActor returned error: %v", err)
	}
	found := false
	for _, state := range actor.States {
		if state.Name == "start__wait_0" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected normalized wait state to be generated")
	}
}

func TestCompileActorAllowsNestedRecvAfterGuard(t *testing.T) {
	actor, err := CompileActor(`
		(actor A
			(state start
				(edge true
					(if true
						(recv msg)
						(become done))))
			(state done))
	`)
	if err != nil {
		t.Fatalf("CompileActor returned error: %v", err)
	}
	if actor == nil {
		t.Fatal("expected actor, got nil")
	}
}

func TestCompileActorAllowsLeadingSendFollowedByLocalActions(t *testing.T) {
	actor, err := CompileActor(`
		(actor A
			(state start
				(edge true
					(send B ping)
					(set after 1)
					(become done)))
			(state done))
	`)
	if err != nil {
		t.Fatalf("CompileActor returned error: %v", err)
	}
	if actor == nil {
		t.Fatal("expected actor, got nil")
	}
}

func TestActionIfControlFlow(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(edge true
						(if (data= flag yes)
							(set branch then)
							(set branch else))
						(become done)))
				(state done))
		`),
	)
	runtime.Actors[0].Data["flag"] = Symbol("yes")

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("step A returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected conditional transition to apply")
	}
	if got := runtime.Actors[0].Data["branch"].String(); got != "then" {
		t.Fatalf("expected then branch, got %s", got)
	}
}

func TestUnicodeGuardOperators(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(edge (implies (and (data= flag yes) true) (or (data= flag yes) false))
						(set branch taken)
						(become done)))
				(state done))
		`),
	)
	runtime.Actors[0].Data["flag"] = Symbol("yes")

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("step A returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected guard transition to apply")
	}
	if got := runtime.Actors[0].Data["branch"].String(); got != "taken" {
		t.Fatalf("expected guard branch, got %s", got)
	}
}

func TestBuiltinMD5AndRawRSA(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor Crypto
				(state start
					(edge true
						(md5 digest payload)
						(rsa-raw cipher modulus exponent message)
						(become done)))
				(state done))
		`),
	)
	runtime.Actors[0].Data["payload"] = String("abc")
	runtime.Actors[0].Data["modulus"] = Number("33")
	runtime.Actors[0].Data["exponent"] = Number("3")
	runtime.Actors[0].Data["message"] = Number("4")

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("step returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected builtin transition to apply")
	}
	if got := runtime.Actors[0].Data["digest"].String(); got != `"900150983cd24fb0d6963f7d28e17f72"` {
		t.Fatalf("unexpected md5 digest: %s", got)
	}
	if got := runtime.Actors[0].Data["cipher"].String(); got != "31" {
		t.Fatalf("unexpected rsa-raw result: %s", got)
	}
}

func TestBuiltinCryptoRandom(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor Crypto
				(state start
					(edge true
						(cryptorandom nonce 8)
						(become done)))
				(state done))
		`),
	)

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("step returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected cryptorandom transition to apply")
	}
	got := runtime.Actors[0].Data["nonce"]
	if got.Kind != KindString {
		t.Fatalf("expected nonce to be a string, got kind %d", got.Kind)
	}
	if len(got.Text) != 16 {
		t.Fatalf("expected 8 random bytes as 16 hex chars, got %q", got.Text)
	}
}

func TestDiceRangeGuards(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(edge (dice-range 0.0 0.5)
						(set branch low)
						(become done))
					(edge (dice-range 0.5 1.0)
						(set branch high)
						(become done)))
				(state done))
		`),
	)
	runtime.Dice = func() float64 { return 0.75 }

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("step returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected dice-range transition to apply")
	}
	if got := runtime.Actors[0].Data["branch"].String(); got != "high" {
		t.Fatalf("expected high branch, got %s", got)
	}
}

func TestDiceGuardsDefaultTrueWhenDiceUnset(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(edge (dice-range 0.0 0.5)
						(set branch passthrough)
						(become done)))
				(state done))
		`),
	)
	runtime.Dice = nil

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("step returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected dice-range guard to pass when Dice is unset")
	}
	if got := runtime.Actors[0].Data["branch"].String(); got != "passthrough" {
		t.Fatalf("expected passthrough branch, got %s", got)
	}
}

func TestDiceSymbolDefaultTrueWhenDiceUnset(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(edge dice
						(set branch passthrough)
						(become done)))
				(state done))
		`),
	)
	runtime.Dice = nil

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("step returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected dice guard to pass when Dice is unset")
	}
	if got := runtime.Actors[0].Data["branch"].String(); got != "passthrough" {
		t.Fatalf("expected passthrough branch, got %s", got)
	}
}

func TestPredicateDescriptionOnGuard(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(edge (data= flag yes "flag is set")
						(set branch taken)
						(become done)))
				(state done))
		`),
	)
	runtime.Actors[0].Data["flag"] = Symbol("yes")

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("step returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected described guard to apply")
	}
	if got := runtime.Actors[0].Data["branch"].String(); got != "taken" {
		t.Fatalf("expected taken branch, got %s", got)
	}
}

func TestSampleExponentialBuiltin(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(edge true
						(sample-exponential wait 2.0)
						(become done)))
				(state done))
		`),
	)
	runtime.Dice = func() float64 { return 0.5 }

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("step returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected sample-exponential transition to apply")
	}
	got, err := valueFloat(runtime.Actors[0].Data["wait"])
	if err != nil {
		t.Fatalf("wait is not a float-valued number: %v", err)
	}
	if got <= 0 {
		t.Fatalf("expected positive exponential sample, got %v", got)
	}
}

func TestPureLispBuiltinsAndDef(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(edge true
						(def tail (xs) (cdr xs))
						(set xs '(a b c))
						(set ys (cons z xs))
						(set head (car ys))
						(set rest (tail ys))
						(become done)))
				(state done))
		`),
	)

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("step returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected pure lisp transition to apply")
	}
	if got := runtime.Actors[0].Data["head"].String(); got != "z" {
		t.Fatalf("expected head=z, got %s", got)
	}
	if got := runtime.Actors[0].Data["rest"].String(); got != "(a b c)" {
		t.Fatalf("expected rest=(a b c), got %s", got)
	}
	if got := runtime.Actors[0].Data["ys"].String(); got != "(z a b c)" {
		t.Fatalf("expected ys=(z a b c), got %s", got)
	}
}

func TestDecisionProcessMixedDeterminismAndRandomness(t *testing.T) {
	makeRuntime := func(dice float64) *Runtime {
		runtime := NewRuntime(
			MustCompileActor(`
				(actor Client
					(state start
						(edge true
							(send Server req)
							(become done)))
					(state done))
			`),
			MustCompileActor(`
				(actor Server
				(state wait
						(edge (dice-range 0.0 0.5)
							(recv msg)
							(set outcome accepted)
							(become accepted))
						(edge (dice-range 0.5 1.0)
							(recv msg)
							(set outcome rejected)
							(become rejected)))
					(state accepted)
					(state rejected))
			`),
		)
		runtime.Dice = func() float64 { return dice }
		return runtime
	}

	accepted := makeRuntime(0.25)
	applied, err := accepted.StepActor(accepted.Actors[0])
	if err != nil {
		t.Fatalf("accepted client step returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected accepted client step to apply")
	}
	applied, err = accepted.StepActor(accepted.Actors[1])
	if err != nil {
		t.Fatalf("accepted server step returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected accepted server step to apply")
	}
	if got := accepted.Actors[1].Data["state"].String(); got != "accepted" {
		t.Fatalf("expected accepted branch, got %s", got)
	}
	if got := accepted.Actors[1].Data["outcome"].String(); got != "accepted" {
		t.Fatalf("expected accepted outcome, got %s", got)
	}

	rejected := makeRuntime(0.75)
	applied, err = rejected.StepActor(rejected.Actors[0])
	if err != nil {
		t.Fatalf("rejected client step returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected rejected client step to apply")
	}
	applied, err = rejected.StepActor(rejected.Actors[1])
	if err != nil {
		t.Fatalf("rejected server step returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected rejected server step to apply")
	}
	if got := rejected.Actors[1].Data["state"].String(); got != "rejected" {
		t.Fatalf("expected rejected branch, got %s", got)
	}
	if got := rejected.Actors[1].Data["outcome"].String(); got != "rejected" {
		t.Fatalf("expected rejected outcome, got %s", got)
	}
}

func TestPredicateDescriptionOnCTL(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state done))
		`),
	)
	model, err := ExploreModel(runtime)
	if err != nil {
		t.Fatalf("ExploreModel returned error: %v", err)
	}

	formula, err := CompileCTL(`(ag (in-state A done "actor A remains done") "global invariant")`)
	if err != nil {
		t.Fatalf("CompileCTL returned error: %v", err)
	}
	if !model.HoldsAtInitial(formula) {
		t.Fatal("expected described CTL predicate to hold")
	}
}

func TestRuntimeLogsEventsAndBuildsSeries(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(edge true
						(send B ping)
						(become done)))
				(state done))
		`),
		MustCompileActor(`
			(actor B
				(state relay
					(edge true
						(recv msg)
						(send C msg)
						(become relay))))
		`),
		MustCompileActor(`
			(actor C
				(state sink
					(edge true
						(recv received)
						(become sink))))
		`),
	)

	steps := []*Actor{runtime.Actors[0], runtime.Actors[1], runtime.Actors[1], runtime.Actors[2]}
	for i, actor := range steps {
		applied, err := runtime.StepActor(actor)
		if err != nil {
			t.Fatalf("step %d returned error: %v", i+1, err)
		}
		if !applied {
			t.Fatalf("expected step %d to apply", i+1)
		}
	}

	if got := runtime.Step; got != 4 {
		t.Fatalf("expected step counter 4, got %d", got)
	}
	if got := len(runtime.Events); got != 8 {
		t.Fatalf("expected 8 events, got %d", got)
	}

	wantKinds := []EventKind{
		EventTransition,
		EventSend,
		EventTransition,
		EventReceive,
		EventTransition,
		EventSend,
		EventTransition,
		EventReceive,
	}
	for i, want := range wantKinds {
		if got := runtime.Events[i].Kind; got != want {
			t.Fatalf("event %d kind = %s, want %s", i, got, want)
		}
	}

	sendCounts := runtime.EventCountSeries(EventSend, nil)
	if len(sendCounts) != 2 {
		t.Fatalf("expected 2 send count points, got %d", len(sendCounts))
	}
	if sendCounts[0] != (MetricPoint{Step: 1, Value: 1}) {
		t.Fatalf("unexpected first send count point: %+v", sendCounts[0])
	}
	if sendCounts[1] != (MetricPoint{Step: 3, Value: 2}) {
		t.Fatalf("unexpected second send count point: %+v", sendCounts[1])
	}

	sendRates := runtime.EventRateSeries(EventSend, nil, 2)
	wantRates := []MetricPoint{
		{Step: 1, Value: 1},
		{Step: 2, Value: 0.5},
		{Step: 3, Value: 0.5},
		{Step: 4, Value: 0.5},
	}
	if len(sendRates) != len(wantRates) {
		t.Fatalf("expected %d send rate points, got %d", len(wantRates), len(sendRates))
	}
	for i, want := range wantRates {
		if sendRates[i] != want {
			t.Fatalf("send rate point %d = %+v, want %+v", i, sendRates[i], want)
		}
	}

	toCCounts := runtime.EventCountSeries(EventSend, func(event Event) bool {
		return event.PeerName == "C"
	})
	if len(toCCounts) != 1 || toCCounts[0] != (MetricPoint{Step: 3, Value: 1}) {
		t.Fatalf("unexpected filtered send counts: %+v", toCCounts)
	}
}

func TestMM1QueueBranching(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor Client
				(state loop
					(edge (dice-range 0.0 0.5)
						(set last "sleep")
						(become loop))
					(edge (dice-range 0.5 1.0)
						(send Server req)
						(set last "send")
						(become loop))))
		`),
		MustCompileActor(`
			(actor Server
				(state wait
					(edge (mailbox req)
						(recv msg)
						(add count 1)
						(set elapsed 0)
						(become wait))
					(edge (and (mailbox tick) (data> count 0) (data= elapsed 1))
						(recv msg)
						(sub count 1)
						(set elapsed 0)
						(become wait))
					(edge (and (mailbox tick) (data> count 0))
						(recv msg)
						(add elapsed 1)
						(become wait))
					(edge (mailbox tick)
						(recv msg)
						(become wait))))
		`),
		MustCompileActor(`
			(actor Ticker
				(state loop
					(edge true
						(send Server tick)
						(become loop))))
		`),
	)
	runtime.Actors[1].Data["count"] = Number("0")
	runtime.Actors[1].Data["elapsed"] = Number("0")

	dice := []float64{0.75, 0.75, 0.25}
	runtime.Dice = func() float64 {
		if len(dice) == 0 {
			return 0.75
		}
		next := dice[0]
		dice = dice[1:]
		return next
	}

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("client send branch returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected client send branch to apply")
	}
	if got := runtime.Actors[0].Data["last"].String(); got != `"send"` {
		t.Fatalf("expected client last=send, got %s", got)
	}
	if got := len(runtime.Mailbox("Server")); got != 1 {
		t.Fatalf("expected server mailbox length 1 after send, got %d", got)
	}

	applied, err = runtime.StepActor(runtime.Actors[1])
	if err != nil {
		t.Fatalf("server receive req returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected server req receive to apply")
	}
	if got := runtime.Actors[1].Data["count"].String(); got != "1" {
		t.Fatalf("expected server count=1 after req, got %s", got)
	}

	applied, err = runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("client sleep branch returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected client sleep branch to apply")
	}
	if got := runtime.Actors[0].Data["last"].String(); got != `"sleep"` {
		t.Fatalf("expected client last=sleep, got %s", got)
	}
	if got := len(runtime.Mailbox("Server")); got != 0 {
		t.Fatalf("expected no new req while client slept, got mailbox length %d", got)
	}

	applied, err = runtime.StepActor(runtime.Actors[2])
	if err != nil {
		t.Fatalf("ticker first tick returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected ticker first tick to apply")
	}
	applied, err = runtime.StepActor(runtime.Actors[1])
	if err != nil {
		t.Fatalf("server first tick handling returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected server first tick handling to apply")
	}
	if got := runtime.Actors[1].Data["count"].String(); got != "1" {
		t.Fatalf("expected server count to remain 1 after first tick, got %s", got)
	}
	if got := runtime.Actors[1].Data["elapsed"].String(); got != "1" {
		t.Fatalf("expected server elapsed=1 after first tick, got %s", got)
	}

	applied, err = runtime.StepActor(runtime.Actors[2])
	if err != nil {
		t.Fatalf("ticker second tick returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected ticker second tick to apply")
	}
	applied, err = runtime.StepActor(runtime.Actors[1])
	if err != nil {
		t.Fatalf("server second tick handling returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected server second tick handling to apply")
	}
	if got := runtime.Actors[1].Data["count"].String(); got != "0" {
		t.Fatalf("expected server count=0 after service completion, got %s", got)
	}
	if got := runtime.Actors[1].Data["elapsed"].String(); got != "0" {
		t.Fatalf("expected server elapsed reset to 0, got %s", got)
	}
}

func TestRuntimeLispSerializationForCompiledModel(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(edge true
						(send B ping)
						(become done)))
				(state done))
		`),
		MustCompileActor(`
			(actor B
				(state relay
					(edge true
						(recv msg)
						(send C msg)
						(become relay))))
		`),
	)

	got := runtime.Lisp().String()
	want := `(runtime (actor A (state start (edge true (send B ping) (become done))) (state done)) (current-state A start) (data A (state start)) (mailbox A) (actor B (state relay (edge true (recv msg) (send C msg) (become relay)))) (current-state B relay) (data B (state relay)) (mailbox B))`
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
	want := "(actor A (state idle (edge noop)))"
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
