package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

var (
	docPlotMode = flag.String("doc-plot-mode", "", "internal helper for docs plot generation")
	docPlotOut  = flag.String("doc-plot-out", "", "internal helper output path for docs plot generation")
	docPlotName = flag.String("doc-plot-name", "", "internal helper plot name for docs plot generation")
)

func TestEmitDocPlotDataForDocs(t *testing.T) {
	mode := *docPlotMode
	if mode == "" {
		t.Skip("doc plot helper")
	}
	if *docPlotOut == "" {
		t.Fatal("--doc-plot-out must be set")
	}

	var data interface{}
	var err error
	switch mode {
	case "manifest":
		data, err = docPlotManifestData()
	case "data":
		data, err = docPlotDataByName(*docPlotName, 0)
	default:
		t.Fatalf("unsupported CTL_DOC_PLOT_MODE %q", mode)
	}
	if err != nil {
		t.Fatalf("doc plot helper returned error: %v", err)
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	if err := os.WriteFile(*docPlotOut, bytes, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
}

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

func TestReadIgnoresDoubleSemicolonComments(t *testing.T) {
	got, err := Read(`
		;; actor comment
		(send actor message) ;; trailing comment
	`)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if got.String() != "(send actor message)" {
		t.Fatalf("got %s, want %s", got.String(), "(send actor message)")
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
							Action: Receive(MatchMessage(ping), func(rt *Runtime, actor *Actor, message Value, _ string) error {
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
							Action: Receive(MatchMessage(ping), func(rt *Runtime, actor *Actor, message Value, _ string) error {
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
		{name: "ef mailbox c ping", formula: "(ef (mailbox-has C ping))", want: true},
		{name: "af mailbox c ping", formula: "(af (mailbox-has C ping))", want: true},
		{name: "ag not mailbox b ping", formula: "(ag (not (mailbox-has B ping)))", want: false},
		{name: "eu not sink until sink", formula: "(eu (not (in-state C sink)) (in-state C sink))", want: true},
		{name: "au not sink until sink", formula: "(au (not (in-state C sink)) (in-state C sink))", want: true},
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
		"A|start|true":       false,
		"B|relay|true":       false,
		"B|relay__wait|true": false,
		"C|sink|true":        false,
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

func TestExploreModelProjectsOnlyVisibleBehavior(t *testing.T) {
	spec, err := CompileModel(`
		(model
			(actor Queue
				(data x 7)
				(data cap 10)
				(state run
					(edge (not (data= x 10))
						(add x 1)
						(become run))
					(edge (data> x 0)
						(sub x 1)
						(become run))))
			(instance Q Queue (queue 10)))
	`)
	if err != nil {
		t.Fatalf("CompileModel returned error: %v", err)
	}
	model, err := ExploreModel(spec.Runtime())
	if err != nil {
		t.Fatalf("ExploreModel returned error: %v", err)
	}

	if len(model.States) != 1 {
		t.Fatalf("expected one visible state after projecting away internal variables, got %d", len(model.States))
	}
	succs := model.Successors[model.InitialID]
	if len(succs) != 1 || succs[0] != model.InitialID {
		t.Fatalf("expected one self-loop successor at the visible initial state, got %#v", succs)
	}
}

func TestCompileModelChecksEmbeddedAssertions(t *testing.T) {
	spec := MustCompileModel(`
		(model
			(actor Worker
				(state start
					(edge true
						(become done)))
				(state done))
			(instance A Worker (queue 1))
			(assert (ef (in-state A done)))
			(assert (ag (in-state A start))))
	`)

	results, err := spec.CheckAssertions()
	if err != nil {
		t.Fatalf("CheckAssertions returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 assertion results, got %d", len(results))
	}
	if !results[0].Holds {
		t.Fatal("expected first embedded assertion to hold")
	}
	if results[1].Holds {
		t.Fatal("expected second embedded assertion to fail")
	}
	want := `(model (actor Worker (state start (edge true (become done))) (state done)) (instance A Worker (queue 1)) (assert (ef (in-state A done))) (assert (ag (in-state A start))))`
	if got := spec.Lisp().String(); got != want {
		t.Fatalf("unexpected model serialization: %s", got)
	}
	if got := spec.Actors[0].Role; got != "Worker" {
		t.Fatalf("expected actor role Worker, got %q", got)
	}
}

func TestCompileModelCapturesXYPlot(t *testing.T) {
	spec := MustCompileModel(`
		(model
			(actor LoopingWorker
				(state loop
					(edge true
						(become loop))))
			(instance A LoopingWorker (queue 1))
			(steps 100)
			(xyplot outstanding
				(title "Outstanding Messages By Step")
				(metric sent-minus-received)))
	`)

	if len(spec.Plots) != 1 {
		t.Fatalf("expected 1 plot, got %d", len(spec.Plots))
	}
	plot := spec.Plots[0]
	if plot.Name != "outstanding" {
		t.Fatalf("unexpected plot name %q", plot.Name)
	}
	if plot.Title != "Outstanding Messages By Step" {
		t.Fatalf("unexpected plot title %q", plot.Title)
	}
	if spec.Steps != 100 {
		t.Fatalf("unexpected model steps %d", spec.Steps)
	}
	if plot.Metric != "sent-minus-received" {
		t.Fatalf("unexpected plot metric %q", plot.Metric)
	}
}

func TestRenderRequirementsMarkdownFromModel(t *testing.T) {
	spec := MustCompileModel(`
		(model
			(actor Sender
				(state start
					(edge true
						(send Receiver msg)
						(become done)))
				(state done))
			(actor Receiver
				(state sink
					(edge true
						(recv msg)
						(become sink))))
			(instance A Sender (queue 1) (Receiver B))
			(instance B Receiver (queue 1))
			(steps 1)
			(xyplot sent
				(title "Send Rate")
				(metric send-rate)))
	`)

	got, err := renderRequirementsMarkdown(spec)
	if err != nil {
		t.Fatalf("renderRequirementsMarkdown returned error: %v", err)
	}
	for _, want := range []string{
		"# Requirements Model",
		"## Line Graphs",
		"## Channel Sizes",
		"## State Diagram",
		"## Message Diagram",
		"## Class Diagram",
		"(xyplot sent",
		"```mermaid",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected rendered markdown to contain %q", want)
		}
	}
}

func TestDocBakeryModelAssertionsPassAsVisibleBehaviorCatalog(t *testing.T) {
	spec, err := docBakeryModel()
	if err != nil {
		t.Fatalf("docBakeryModel returned error: %v", err)
	}
	if len(spec.Assertions) < 20 {
		t.Fatalf("expected many bakery assertions, got %d", len(spec.Assertions))
	}

	results, err := spec.CheckAssertions()
	if err != nil {
		t.Fatalf("CheckAssertions returned error: %v", err)
	}

	for _, result := range results {
		if !result.Holds {
			t.Fatalf("expected bakery assertion to hold: %s", result.Assertion.Spec.Items[1].String())
		}
	}

	rendered, err := renderRequirementsMarkdown(spec)
	if err != nil {
		t.Fatalf("renderRequirementsMarkdown returned error: %v", err)
	}
	for _, want := range []string{
		"(assert (possibly (in-state CustomerA bought_rye)))",
		"`PASS` `(possibly (in-state CustomerA bought_rye))`",
		"`PASS` `(not (possibly (in-state CustomerC bought_rye)))`",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected rendered bakery markdown to contain %q", want)
		}
	}
}

func TestDocMessageModelContainsManyPassingAssertions(t *testing.T) {
	spec, err := docMessageModel()
	if err != nil {
		t.Fatalf("docMessageModel returned error: %v", err)
	}
	if len(spec.Assertions) < 10 {
		t.Fatalf("expected many message-model assertions, got %d", len(spec.Assertions))
	}
	results, err := spec.CheckAssertions()
	if err != nil {
		t.Fatalf("CheckAssertions returned error: %v", err)
	}
	for _, result := range results {
		if !result.Holds {
			t.Fatalf("expected message-model assertion to hold: %s", result.Assertion.Spec.Items[1].String())
		}
	}
	rendered, err := renderRequirementsMarkdown(spec)
	if err != nil {
		t.Fatalf("renderRequirementsMarkdown returned error: %v", err)
	}
	for _, want := range []string{
		"`PASS` `(next-always (in-state Client done))`",
		"`PASS` `(eventually (mailbox-has Server (quote (message (type ping)))))`",
		"`PASS` `(not (possibly (mailbox-has Server (quote (message (type pong))))))`",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected rendered message markdown to contain %q", want)
		}
	}
}

func TestServerServesEmbeddedIndex(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	newServerMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / returned status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "go-ctl2") {
		t.Fatalf("expected embedded index html, got %q", rec.Body.String())
	}
}

func TestServerServesEmbeddedDocs(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/docs", nil)
	rec := httptest.NewRecorder()
	newServerMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /docs returned status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Actor IR, CTL, and Diagram Strategy") {
		t.Fatalf("expected embedded docs html, got %q", rec.Body.String())
	}
}

func TestServerRenderAPI(t *testing.T) {
	body := `{"source":"(model (actor Worker (state start (edge true (become done))) (state done)) (instance A Worker (queue 1)))"}`
	req := httptest.NewRequest(http.MethodPost, "/api/render", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	newServerMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/render returned status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Requirements Model") {
		t.Fatalf("expected rendered interpretation payload, got %s", rec.Body.String())
	}
}

func TestServerHistoryAPI(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	putBody := `{"conversations":[{"id":"c1","title":"Saved"}],"activeId":"c1"}`
	putReq := httptest.NewRequest(http.MethodPut, "/api/history", strings.NewReader(putBody))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	newServerMux().ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT /api/history returned status %d: %s", putRec.Code, putRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	getRec := httptest.NewRecorder()
	newServerMux().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /api/history returned status %d: %s", getRec.Code, getRec.Body.String())
	}
	if !strings.Contains(getRec.Body.String(), `"activeId":"c1"`) {
		t.Fatalf("expected stored history payload, got %s", getRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/history", nil)
	deleteRec := httptest.NewRecorder()
	newServerMux().ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/history returned status %d: %s", deleteRec.Code, deleteRec.Body.String())
	}

	getAfterDelete := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	getAfterDeleteRec := httptest.NewRecorder()
	newServerMux().ServeHTTP(getAfterDeleteRec, getAfterDelete)
	if !strings.Contains(getAfterDeleteRec.Body.String(), `"conversations":null`) && !strings.Contains(getAfterDeleteRec.Body.String(), `"conversations":[]`) {
		t.Fatalf("expected cleared history payload, got %s", getAfterDeleteRec.Body.String())
	}
}

func TestProviderCatalogIncludesBuiltin(t *testing.T) {
	catalog := buildProviderCatalog(t.Context())
	if catalog.Default == "" {
		t.Fatal("expected default provider")
	}
	found := false
	for _, provider := range catalog.Providers {
		if provider.ID == "builtin" && provider.Available {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected builtin provider in %+v", catalog.Providers)
	}
}

func TestChatSystemPromptMentionsDocs(t *testing.T) {
	prompt := chatSystemPrompt("(model)")
	for _, want := range []string{"/docs", "/docs/ir.generated.md", "fenced `canvas` blocks", "deterministic or actually branches", "queue capacities matter", "use `become` to split control flow", "(print value)", "(cons head tail)", "against a referenced prior model such as `A12`", "The UI is dark mode", "paint a visible background", "respect the provided `width` and `height`", "avoid placing shapes or labels outside the visible canvas"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to mention %q, got %s", want, prompt)
		}
	}
}

func TestChatSystemPromptIncludesLispAndRenderedInterpretation(t *testing.T) {
	source := `
		(model
			(actor Worker
				(state start
					(edge true
						(become done)))
				(state done))
			(instance A Worker (queue 1)))
	`
	prompt := chatSystemPrompt(source)
	for _, want := range []string{"Current Lisp model:", "```lisp", "(instance A Worker (queue 1))", "Current rendered interpretation:", "```markdown", "# Requirements Model"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got %s", want, prompt)
		}
	}
}

func TestChatSystemPromptIncludesCompilerErrorsWhenModelIsInvalid(t *testing.T) {
	prompt := chatSystemPrompt("(model (instance A MissingRole (queue 1)))")
	for _, want := range []string{"Current compiler result:", "```text", "unknown actor role"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got %s", want, prompt)
		}
	}
}

func TestLookupEnvLoggedReportsHowToSet(t *testing.T) {
	old := envLog
	defer func() { envLog = old }()
	var buf bytes.Buffer
	envLog = &buf
	t.Setenv("OPENAI_API_KEY", "")
	lookupEnvLogged("OPENAI_API_KEY")
	got := buf.String()
	if !strings.Contains(got, "env lookup OPENAI_API_KEY: unset") {
		t.Fatalf("expected unset log, got %q", got)
	}
	if !strings.Contains(got, "export OPENAI_API_KEY=...") {
		t.Fatalf("expected set hint, got %q", got)
	}
}

func TestBuiltinChatDrawCircleReturnsSVG(t *testing.T) {
	out, err := builtinChatReply("explain", "(model)", []chatTurn{{Role: "user", Content: "draw a circle"}})
	if err != nil {
		t.Fatalf("builtinChatReply returned error: %v", err)
	}
	if !strings.Contains(out, "```svg") || !strings.Contains(out, "<circle") {
		t.Fatalf("expected svg circle response, got %s", out)
	}
}

func TestBuiltinChatDrawDiamondReturnsSVG(t *testing.T) {
	out, err := builtinChatReply("explain", "(model)", []chatTurn{{Role: "user", Content: "draw a diamond"}})
	if err != nil {
		t.Fatalf("builtinChatReply returned error: %v", err)
	}
	if !strings.Contains(out, "```svg") || !strings.Contains(out, "<polygon") {
		t.Fatalf("expected svg diamond response, got %s", out)
	}
}

func TestBuiltinChatCTLExplainerReturnsDiagrams(t *testing.T) {
	out, err := builtinChatReply("explain", "(model)", []chatTurn{{Role: "user", Content: "what is CTL? can you draw me some diagrams of how it works?"}})
	if err != nil {
		t.Fatalf("builtinChatReply returned error: %v", err)
	}
	for _, want := range []string{"Computation Tree Logic", "```svg", "```mermaid", "`EX p`"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected CTL explainer to contain %q, got %s", want, out)
		}
	}
}

func TestBuiltinChatCTLClarificationStaysOnTopic(t *testing.T) {
	out, err := builtinChatReply("explain", "(model (actor A (state start)))", []chatTurn{
		{Role: "user", Content: "what is CTL? can you draw me some diagrams of how it works?"},
		{Role: "assistant", Content: "CTL is branching-time logic."},
		{Role: "user", Content: "i am confused by the nodes t1, t2, t3. it should be more like... every state has a set of propositions that are true or false; the same set in every one."},
	})
	if err != nil {
		t.Fatalf("builtinChatReply returned error: %v", err)
	}
	if strings.Contains(out, "The current requirement compiles") {
		t.Fatalf("expected conversational clarification, got %s", out)
	}
	for _, want := range []string{"Kripke semantics", "states are worlds", "```svg"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected clarification to contain %q, got %s", want, out)
		}
	}
}

func TestBuiltinChatConversationSequenceReturnsMermaid(t *testing.T) {
	out, err := builtinChatReply("explain", "(model)", []chatTurn{
		{Role: "user", Content: "what is CTL?"},
		{Role: "assistant", Content: "CTL is branching-time logic."},
		{Role: "user", Content: "draw a sequence diagram of what we just talked about"},
	})
	if err != nil {
		t.Fatalf("builtinChatReply returned error: %v", err)
	}
	if !strings.Contains(out, "```mermaid") || !strings.Contains(out, "sequenceDiagram") {
		t.Fatalf("expected conversation sequence diagram, got %s", out)
	}
}

func TestBuiltinChatRecognizesSimpleSequencePrompt(t *testing.T) {
	out, err := builtinChatReply("explain", "(model)", []chatTurn{
		{Role: "user", Content: "A sends a message to B: ping"},
	})
	if err != nil {
		t.Fatalf("builtinChatReply returned error: %v", err)
	}
	for _, want := range []string{"```mermaid", "sequenceDiagram", "A->>B: ping"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected simple sequence output to contain %q, got %s", want, out)
		}
	}
}

func TestSimpleSequencePromptParsesNaturalLanguage(t *testing.T) {
	got := simpleSequencePrompt("Client sends a message to Server: quote request")
	if got == nil {
		t.Fatal("expected simple sequence prompt to parse")
	}
	if got.From != "Client" || got.To != "Server" || got.Message != "quote request" {
		t.Fatalf("unexpected parsed sequence: %+v", got)
	}
}

func TestBuiltinChatAsksClarifyingQuestionForAmbiguousSequencePrompt(t *testing.T) {
	out, err := builtinChatReply("explain", "(model)", []chatTurn{
		{Role: "user", Content: "draw a sequence diagram"},
	})
	if err != nil {
		t.Fatalf("builtinChatReply returned error: %v", err)
	}
	for _, want := range []string{"I think you may be asking for an interaction diagram", "A sends a message to B: ping"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected clarification to contain %q, got %s", want, out)
		}
	}
}

func TestBuiltinChatAsksClarifyingQuestionForCurrentEventsModelWithoutLLM(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	out, err := builtinChatReply("explain", "(model)", []chatTurn{
		{Role: "user", Content: "Create a model of what is currently happening with the war in Iran and its effect on oil prices."},
	})
	if err != nil {
		t.Fatalf("builtinChatReply returned error: %v", err)
	}
	for _, want := range []string{"I can help with that, but I need one clarification first.", "live current-events answer", "hypothetical go-ctl2 model"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected current-events clarification to contain %q, got %s", want, out)
		}
	}
}

func TestBuiltinChatEvaluatesCTLAgainstCurrentModel(t *testing.T) {
	out, err := builtinChatReply("explain", `
		(model
			(actor Worker
				(state start
					(edge true
						(become done)))
				(state done))
			(instance A Worker (queue 1)))
	`, []chatTurn{
		{Role: "user", Turn: 1, Content: "check CTL formula (ef (in-state A done))"},
	})
	if err != nil {
		t.Fatalf("builtinChatReply returned error: %v", err)
	}
	for _, want := range []string{"I evaluated the CTL formula", "`true`", "(ef (in-state A done))", "the current Model tab"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected evaluation output to contain %q, got %s", want, out)
		}
	}
}

func TestBuiltinChatEvaluatesMuFormulaFromReferencedTurn(t *testing.T) {
	out, err := builtinChatReply("explain", `
		(model
			(actor Worker
				(state start
					(edge true
						(become done)))
				(state done))
			(instance A Worker (queue 1)))
	`, []chatTurn{
		{Role: "assistant", Turn: 7, Content: "```lisp\n(mu X (or (in-state A done) (diamond X)))\n```"},
		{Role: "user", Turn: 8, Content: "evaluate mu-calculus formula in A7 against the current model"},
	})
	if err != nil {
		t.Fatalf("builtinChatReply returned error: %v", err)
	}
	for _, want := range []string{"raw modal mu-calculus", "formula source: A7", "`true`"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected mu evaluation output to contain %q, got %s", want, out)
		}
	}
}

func TestBuiltinChatFallsBackToConfiguredLLMNotice(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	out, err := chatWithProvider(t.Context(), "builtin", "explain", "(model)", []chatTurn{
		{Role: "user", Content: "tell me a joke about distributed systems"},
	})
	if err != nil {
		t.Fatalf("chatWithProvider returned error: %v", err)
	}
	if !strings.Contains(out, "No external LLM is configured for open-ended chat.") {
		t.Fatalf("expected fallback setup note, got %s", out)
	}
	if strings.Contains(out, "switch into model mode") {
		t.Fatalf("expected no mode speech, got %s", out)
	}
}

func TestExtractModelSourceFindsFencedLispModel(t *testing.T) {
	content := "Here is the model:\n\n```lisp\n(model (actor A (state start)) (instance X A (queue 1)))\n```"
	got, ok := extractModelSource(content)
	if !ok {
		t.Fatal("expected to extract model source")
	}
	if !strings.Contains(got, "(instance X A (queue 1))") {
		t.Fatalf("unexpected extracted model: %s", got)
	}
}

func TestExplainChatErrorAlwaysAddsExplanation(t *testing.T) {
	err := explainChatError(fmt.Errorf("401 unauthorized"))
	if err == nil {
		t.Fatal("expected wrapped error")
	}
	for _, want := range []string{"configured correctly", "technical detail"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected wrapped error to contain %q, got %s", want, err.Error())
		}
	}
}

func TestMaybeRepairModelReplyRetriesUntilModelCompiles(t *testing.T) {
	initial := "```lisp\n(model (actor Worker (state start)) (instance A Worker (queue 1)) (assert (ef (state= A start))))\n```"
	var attempts int
	got, err := maybeRepairModelReply(`
		(model
			(actor Worker
				(state start
					(edge true
						(become done)))
				(state done))
			(instance A Worker (queue 1)))
	`, []chatTurn{{Role: "user", Content: "make me a model"}}, initial, func(retryMessages []chatTurn) (string, error) {
		attempts++
		if attempts == 1 {
			return "```lisp\n(model (actor Worker (state start)) (instance A Worker (queue 1)) (assert (ef (state= A start))))\n```", nil
		}
		return "```lisp\n(model (actor Worker (state start (edge true (become done))) (state done)) (instance A Worker (queue 1)) (assert (ef (in-state A done))))\n```", nil
	})
	if err != nil {
		t.Fatalf("maybeRepairModelReply returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 repair attempts, got %d", attempts)
	}
	if !strings.Contains(got, "(assert (ef (in-state A done)))") {
		t.Fatalf("expected repaired model, got %s", got)
	}
}

func TestMaybeExecuteLispReplyEvaluatesAgainstDefaultModel(t *testing.T) {
	model := `
		(model
			(actor Worker
				(state start
					(edge true
						(become done)))
				(state done))
			(instance A Worker (queue 1)))
	`
	got, err := maybeExecuteLispReply(model, nil, "```lisp\n(ef (in-state A done))\n```")
	if err != nil {
		t.Fatalf("maybeExecuteLispReply returned error: %v", err)
	}
	for _, want := range []string{"## Engine Execution", "model source: the current Model tab", "- logic: CTL", "- formula: `(ef (in-state A done))`", "- holds at the initial state: `true`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected output to contain %q, got %s", want, got)
		}
	}
}

func TestMaybeExecuteLispReplyEvaluatesAgainstReferencedModel(t *testing.T) {
	messages := []chatTurn{
		{
			Role:    "assistant",
			Turn:    12,
			Content: "```lisp\n(model (actor Worker (state start (edge true (become done))) (state done)) (instance A Worker (queue 1)))\n```",
		},
	}
	got, err := maybeExecuteLispReply("", messages, "Evaluate this against A12:\n```lisp\n(ef (in-state A done))\n```")
	if err != nil {
		t.Fatalf("maybeExecuteLispReply returned error: %v", err)
	}
	for _, want := range []string{"## Engine Execution", "model source: A12", "- logic: CTL", "- holds at the initial state: `true`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected output to contain %q, got %s", want, got)
		}
	}
}

func TestFallbackLLMProviderPrefersOpenAIThenClaude(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	if got := fallbackLLMProvider(); got != "" {
		t.Fatalf("expected no fallback provider, got %q", got)
	}
	t.Setenv("ANTHROPIC_API_KEY", "claude-key")
	if got := fallbackLLMProvider(); got != "claude" {
		t.Fatalf("expected claude fallback, got %q", got)
	}
	t.Setenv("OPENAI_API_KEY", "openai-key")
	if got := fallbackLLMProvider(); got != "openai" {
		t.Fatalf("expected openai fallback, got %q", got)
	}
}

func TestImageSubjectRecognizesPictureRequests(t *testing.T) {
	got := imageSubject("show me a picture of t-pain", "show me a picture of T-Pain")
	if got != "T-Pain" {
		t.Fatalf("expected T-Pain, got %q", got)
	}
}

func TestMailboxSizeSeriesTracksQueuedMessages(t *testing.T) {
	spec := MustCompileModel(`
		(model
			(actor Sender
				(state start
					(edge true
						(send Receiver ping)
						(become sent)))
				(state sent
					(edge true
						(send Receiver pong)
						(become done)))
				(state done))
			(actor Receiver
				(state wait
					(edge true
						(recv msg)
						(become got-one)))
				(state got-one
					(edge true
						(recv msg)
						(become done)))
				(state done))
			(instance A Sender (queue 1) (Receiver B))
			(instance B Receiver (queue 2)))
	`)

	rt, err := runModelForPlot(spec, 4)
	if err != nil {
		t.Fatalf("runModelForPlot returned error: %v", err)
	}
	got := rt.MailboxSizeSeries("B")
	want := []MetricPoint{
		{Step: 0, Value: 0},
		{Step: 1, Value: 1},
		{Step: 2, Value: 0},
		{Step: 3, Value: 1},
		{Step: 4, Value: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d mailbox points, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mailbox point %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCompileModelRejectsUnknownActorRole(t *testing.T) {
	_, err := CompileModel(`
		(model
			(instance A MissingRole (queue 1)))
	`)
	if err == nil {
		t.Fatal("expected unknown actor role error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown actor role") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileModelAcceptsDoubleSemicolonComments(t *testing.T) {
	spec, err := CompileModel(`
		;; top-level comment
		(model
			(actor Worker
				;; start state
				(state start
					(edge true
						(become done))) ;; done edge
				(state done))
			(instance A Worker (queue 1))) ;; instance comment
	`)
	if err != nil {
		t.Fatalf("CompileModel returned error: %v", err)
	}
	if len(spec.Actors) != 1 {
		t.Fatalf("expected 1 actor, got %d", len(spec.Actors))
	}
}

func TestCompileModelRejectsNestedSendInsideIf(t *testing.T) {
	_, err := CompileModel(`
		(model
			(actor Worker
				(state start
					(edge true
						(if true
							(send Peer ping)
							(become done))
						(become done)))
				(state done))
			(actor PeerRole
				(state idle))
			(instance A Worker (queue 1) (PeerRole B))
			(instance B PeerRole (queue 1)))
	`)
	if err == nil {
		t.Fatal("expected nested send error, got nil")
	}
	for _, want := range []string{"top-level edge action", "use `become` to split the control flow"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %v", want, err)
		}
	}
}

func TestCompileModelRequiresActorDeclarations(t *testing.T) {
	_, err := CompileModel(`
		(model
			(actor Worker
				(state start
					(edge true
						(become done)))
				(state done)))
	`)
	if err == nil {
		t.Fatal("expected missing declaration error, got nil")
	}
	if !strings.Contains(err.Error(), "no actor instances") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "Do this:") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileModelRequiresInstanceQueueLength(t *testing.T) {
	_, err := CompileModel(`
		(model
			(actor Worker
				(state start
					(edge true
						(become done)))
				(state done))
			(instance A Worker))
	`)
	if err == nil {
		t.Fatal("expected missing queue length error, got nil")
	}
	if !strings.Contains(err.Error(), "instance has the wrong shape") && !strings.Contains(err.Error(), "instance queue has the wrong shape") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestModelRuntimeUsesInstanceQueueLength(t *testing.T) {
	spec := MustCompileModel(`
		(model
			(actor Sender
				(state start
					(edge true
						(send Receiver ping)
						(become done)))
				(state done))
			(actor Receiver
				(state idle
					(edge true
						(become idle))))
			(instance A Sender (queue 2) (Receiver B))
			(instance B Receiver (queue 0)))
	`)

	runtime := spec.Runtime()
	if got := runtime.MailboxCaps["A"]; got != 2 {
		t.Fatalf("expected A queue length 2, got %d", got)
	}
	if got := runtime.MailboxCaps["B"]; got != 0 {
		t.Fatalf("expected B queue length 0, got %d", got)
	}
}

func TestRenderClassDiagramMermaidShowsActorVariableReadsAndWrites(t *testing.T) {
	spec := MustCompileModel(`
		(model
			(actor Worker
				(data count 0)
				(state start
					(edge (data> count 0)
						(set next count)
						(send Sink msg)
						(add count 1)
						(become done)))
				(state done))
			(actor Sink
				(state idle
					(edge true
						(recv msg)
						(become idle))))
			(instance A Worker (queue 1) (Sink B))
			(instance B Sink (queue 1)))
	`)

	got := renderClassDiagramMermaid(spec)
	for _, want := range []string{
		"classDiagram",
		"<<Worker>> A",
		"+start : state",
		"+count : data",
		"+next : data",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected class diagram to contain %q, got:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"reads/writes",
		"Avarcount",
		"Avarnext",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("did not expect class diagram to contain %q, got:\n%s", unwanted, got)
		}
	}
	if strings.Contains(got, "ping") {
		t.Fatalf("did not expect literal message symbol to appear as a variable, got:\n%s", got)
	}
}

func TestRenderDocExampleSectionsReturns(t *testing.T) {
	out, err := renderDocExampleSections()
	if err != nil {
		t.Fatalf("renderDocExampleSections returned error: %v", err)
	}
	for _, want := range []string{
		"## Message Chain Example",
		"### Input Lisp",
		"### Rendered Output",
		"#### State Diagram",
		"#### CTL Outcomes",
		"```mermaid",
		"xychart-beta",
		"## Bakery Visible-Behavior Example",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected generated example sections to contain %q", want)
		}
	}
}

func TestRenderDocLanguageSectionsGroupsCTLAndMu(t *testing.T) {
	out, err := renderDocLanguageSections()
	if err != nil {
		t.Fatalf("renderDocLanguageSections returned error: %v", err)
	}
	for _, want := range []string{
		"## Branching-Time Logic Forms",
		"### CTL Surface Forms",
		"### Raw Modal μ-Calculus Forms",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected generated language sections to contain %q", want)
		}
	}
}

func TestCompileModelRequiresPeerRoleFill(t *testing.T) {
	_, err := CompileModel(`
		(model
			(actor Sender
				(state start
					(edge true
						(send ReceiverRole ping)
						(become done)))
				(state done))
			(actor ReceiverRole
				(state done
					(edge true
						(become done))))
			(instance A Sender (queue 1))
			(instance B ReceiverRole (queue 1)))
	`)
	if err == nil {
		t.Fatal("expected missing peer role fill error, got nil")
	}
	if !strings.Contains(err.Error(), "must fill peer role ReceiverRole") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileModelRejectsPeerRoleFillToWrongRole(t *testing.T) {
	_, err := CompileModel(`
		(model
			(actor Sender
				(state start
					(edge true
						(send ReceiverRole ping)
						(become done)))
				(state done))
			(actor ReceiverRole
				(state done
					(edge true
						(become done))))
			(actor OtherRole
				(state done
					(edge true
						(become done))))
			(instance A Sender (queue 1) (ReceiverRole C))
			(instance C OtherRole (queue 1)))
	`)
	if err == nil {
		t.Fatal("expected wrong-role fill error, got nil")
	}
	if !strings.Contains(err.Error(), "playing role OtherRole") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileModelAllowsCircularPeerRoleFills(t *testing.T) {
	spec := MustCompileModel(`
		(model
			(actor PingRole
				(state loop
					(edge true
						(send PongRole ping)
						(become loop))))
			(actor PongRole
				(state loop
					(edge true
						(send PingRole pong)
						(become loop))))
			(instance Ping PingRole (queue 1) (PongRole Pong))
			(instance Pong PongRole (queue 1) (PingRole Ping)))
	`)
	if len(spec.Actors) != 2 {
		t.Fatalf("expected 2 actors, got %d", len(spec.Actors))
	}
}

func TestCompileModelRejectsSendToMultiBoundRole(t *testing.T) {
	_, err := CompileModel(`
		(model
			(actor SenderRole
				(state start
					(edge true
						(send ReceiverRole ping)
						(become done)))
				(state done))
			(actor ReceiverRole
				(state ready))
			(instance Sender SenderRole (queue 1) (ReceiverRole ReceiverA ReceiverB))
			(instance ReceiverA ReceiverRole (queue 1))
			(instance ReceiverB ReceiverRole (queue 1)))
	`)
	if err == nil {
		t.Fatal("expected ambiguous send error, got nil")
	}
	if !strings.Contains(err.Error(), "replace `send` with `send-any`") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "Do this:") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSendAnyUsesRoleSetAndRecvSetsSender(t *testing.T) {
	spec := MustCompileModel(`
		(model
			(actor BakeryRole
				(state start
					(edge true
						(send-any TruckRole batch)
						(become done)))
				(state done))
			(actor TruckRole
				(data last 0)
				(state ready
					(edge true
						(recv msg)
						(set last msg)
						(become ready))))
			(instance Bakery BakeryRole (queue 1) (TruckRole TruckA TruckB))
			(instance TruckA TruckRole (queue 1))
			(instance TruckB TruckRole (queue 1)))
	`)

	runtime := spec.Runtime()
	bakery := runtime.actorByName("Bakery")
	if bakery == nil {
		t.Fatal("missing Bakery actor")
	}
	applied, err := runtime.StepActor(bakery)
	if err != nil {
		t.Fatalf("bakery step returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected bakery step to apply")
	}
	if len(runtime.Mailbox("TruckA")) != 1 {
		t.Fatalf("expected TruckA mailbox to have one message, got %d", len(runtime.Mailbox("TruckA")))
	}
	if len(runtime.Mailbox("TruckB")) != 0 {
		t.Fatalf("expected TruckB mailbox to stay empty, got %d", len(runtime.Mailbox("TruckB")))
	}

	truckA := runtime.actorByName("TruckA")
	if truckA == nil {
		t.Fatal("missing TruckA actor")
	}
	applied, err = runtime.StepActor(truckA)
	if err != nil {
		t.Fatalf("truck step returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected truck step to apply")
	}
	if got := truckA.Data["last"].String(); got != "batch" {
		t.Fatalf("TruckA.last = %s, want batch", got)
	}
	if got := truckA.Data["sender"].String(); got != "Bakery" {
		t.Fatalf("TruckA.sender = %s, want Bakery", got)
	}
}

func TestMultipleRoleInstancesKeepLocalVariablesIndependent(t *testing.T) {
	spec := MustCompileModel(`
		(model
			(actor ProductionRole
				(data baked 0)
				(state start
					(edge true
						(send TruckRole batch)
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
			(instance Production ProductionRole (queue 1) (TruckRole TruckNorth))
			(instance TruckNorth TruckRole (queue 1) (StoreRole StoreA))
			(instance TruckSouth TruckRole (queue 1) (StoreRole StoreB))
			(instance StoreA StoreRole (queue 1) (CustomerBaseRole CustomerA))
			(instance StoreB StoreRole (queue 1) (CustomerBaseRole CustomerB))
			(instance StoreC StoreRole (queue 1) (CustomerBaseRole CustomerC))
			(instance CustomerA CustomerBaseRole (queue 1))
			(instance CustomerB CustomerBaseRole (queue 1))
			(instance CustomerC CustomerBaseRole (queue 1)))
	`)

	runtime := spec.Runtime()
	order := []string{"Production", "TruckNorth", "TruckNorth", "StoreA", "StoreA", "CustomerA"}
	for i, name := range order {
		actor := runtime.actorByName(name)
		if actor == nil {
			t.Fatalf("missing actor %s", name)
		}
		applied, err := runtime.StepActor(actor)
		if err != nil {
			t.Fatalf("step %d for %s returned error: %v", i+1, name, err)
		}
		if !applied {
			t.Fatalf("expected step %d for %s to apply", i+1, name)
		}
	}

	assertData := func(actorName, key, want string) {
		actor := runtime.actorByName(actorName)
		if actor == nil {
			t.Fatalf("missing actor %s", actorName)
		}
		if got := actor.Data[key].String(); got != want {
			t.Fatalf("%s.%s = %s, want %s", actorName, key, got, want)
		}
	}

	assertData("Production", "baked", "1")
	assertData("TruckNorth", "deliveries", "1")
	assertData("TruckSouth", "deliveries", "0")
	assertData("StoreA", "inventory", "0")
	assertData("StoreA", "sold", "1")
	assertData("StoreB", "inventory", "0")
	assertData("StoreB", "sold", "0")
	assertData("StoreC", "inventory", "0")
	assertData("StoreC", "sold", "0")
	assertData("CustomerA", "served", "1")
	assertData("CustomerB", "served", "0")
	assertData("CustomerC", "served", "0")
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
	_, err := CompileCTL("(someday (in-state A done))")
	if err == nil {
		t.Fatal("expected compile error, got nil")
	}
}

func TestCompileCTLRejectsInternalDataPredicates(t *testing.T) {
	_, err := CompileCTL("(ef (data= Q x 8))")
	if err == nil {
		t.Fatal("expected CompileCTL to reject internal data predicates")
	}
	if !strings.Contains(err.Error(), "visible behavior") {
		t.Fatalf("expected visible behavior guidance, got %v", err)
	}
}

func TestCompileMuRejectsInternalDataPredicates(t *testing.T) {
	_, err := CompileMu("(mu X (or (data= Q x 8) (diamond X)))")
	if err == nil {
		t.Fatal("expected CompileMu to reject internal data predicates")
	}
	if !strings.Contains(err.Error(), "visible behavior") {
		t.Fatalf("expected visible behavior guidance, got %v", err)
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
		if state.Name == "start__wait" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected normalized wait state to be generated")
	}
}

func TestCompileActorRejectsNestedRecvAfterGuard(t *testing.T) {
	_, err := CompileActor(`
		(actor A
			(state start
				(edge true
					(if true
						(recv msg)
						(become done))))
			(state done))
	`)
	if err == nil {
		t.Fatal("expected nested recv error, got nil")
	}
	if !strings.Contains(err.Error(), "top-level edge action") {
		t.Fatalf("unexpected error: %v", err)
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

func TestPrintBuiltinWritesRuntimeTrace(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state start
					(edge true
						(set xs '(b c))
						(print (cons a xs))
						(become done)))
				(state done))
		`),
	)

	applied, err := runtime.StepActor(runtime.Actors[0])
	if err != nil {
		t.Fatalf("step returned error: %v", err)
	}
	if !applied {
		t.Fatal("expected print transition to apply")
	}
	if len(runtime.Trace) == 0 {
		t.Fatal("expected print to append to runtime trace")
	}
	last := runtime.Trace[len(runtime.Trace)-1]
	if !strings.Contains(last, "print: actor=A value=(a b c)") {
		t.Fatalf("expected printed value in trace, got %s", last)
	}
}

func TestDefRejectsHiddenActions(t *testing.T) {
	_, err := CompileActor(`
		(actor A
			(state start
				(edge true
					(def bad () (send B ping))
					(become done)))
			(state done))
	`)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "forbidden form `send`") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "keep `def` bodies pure") {
		t.Fatalf("error should explain what to do next: %v", err)
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

func TestCTLMailboxHasMatchesQuotedListLiteral(t *testing.T) {
	runtime := NewRuntime(
		MustCompileActor(`
			(actor A
				(state done))
		`),
	)
	runtime.Enqueue("A", MustRead(`(message (type ping))`), "Sender")

	model, err := ExploreModel(runtime)
	if err != nil {
		t.Fatalf("ExploreModel returned error: %v", err)
	}

	formula, err := CompileCTL(`(ef (mailbox-has A '(message (type ping))))`)
	if err != nil {
		t.Fatalf("CompileCTL returned error: %v", err)
	}
	if !model.HoldsAtInitial(formula) {
		t.Fatal("expected quoted mailbox predicate to hold")
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

func TestCurrentStateRequiresExplicitStateValue(t *testing.T) {
	runtime := NewRuntime(
		&Actor{
			Name: "A",
			States: []State{
				{
					Name:  "idle",
					Guard: func(*Runtime, *Actor) bool { return false },
				},
				{
					Name:  "other",
					Guard: func(*Runtime, *Actor) bool { return true },
				},
			},
		},
	)

	if got := runtime.CurrentState(runtime.Actors[0]); got == nil || got.Name != "idle" {
		t.Fatalf("expected initial explicit state to be idle, got %#v", got)
	}

	delete(runtime.Actors[0].Data, "state")

	if got := runtime.CurrentState(runtime.Actors[0]); got != nil {
		t.Fatalf("expected no current state without explicit actor.Data[state], got %s", got.Name)
	}
}
