package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed webapp/index.html webapp/app.css webapp/app.js docs/build/ir.html docs/build/ir.generated.md docs/dark.css docs/static/ctl_tree.svg
var webAppFS embed.FS
var envLog io.Writer = os.Stderr
var errBuiltinPassThrough = errors.New("builtin provider should fall through to configured llm")

type providerInfo struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Available bool     `json:"available"`
	Models    []string `json:"models"`
	Error     string   `json:"error,omitempty"`
}

type providerCatalog struct {
	Providers []providerInfo `json:"providers"`
	Default   string         `json:"default"`
}

type examplePayload struct {
	Title  string `json:"title"`
	Source string `json:"source"`
}

type renderRequest struct {
	Source string `json:"source"`
	Format string `json:"format"`
}

type renderResponse struct {
	Markdown string `json:"markdown"`
	HTML     string `json:"html"`
}

type chatTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Turn    int    `json:"turn,omitempty"`
}

type chatRequest struct {
	Provider string     `json:"provider"`
	Model    string     `json:"model"`
	Source   string     `json:"source"`
	Messages []chatTurn `json:"messages"`
}

type chatResponse struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Content  string `json:"content"`
}

type historyState struct {
	Conversations []map[string]interface{} `json:"conversations"`
	ActiveID      string                   `json:"activeId"`
}

type anthropicMessageResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Content interface{} `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type wikipediaSummaryResponse struct {
	Title     string `json:"title"`
	Thumbnail *struct {
		Source string `json:"source"`
	} `json:"thumbnail,omitempty"`
	OriginalImage *struct {
		Source string `json:"source"`
	} `json:"originalimage,omitempty"`
}

func serveWeb(addr string) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           newServerMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return server.ListenAndServe()
}

func newServerMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/app.css", handleAsset("webapp/app.css", "text/css; charset=utf-8"))
	mux.HandleFunc("/app.js", handleAsset("webapp/app.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("/docs", handleDocsHTML)
	mux.HandleFunc("/docs/", handleDocsHTML)
	mux.HandleFunc("/docs/ir.generated.md", handleAsset("docs/build/ir.generated.md", "text/markdown; charset=utf-8"))
	mux.HandleFunc("/docs/dark.css", handleAsset("docs/dark.css", "text/css; charset=utf-8"))
	mux.HandleFunc("/docs/static/ctl_tree.svg", handleAsset("docs/static/ctl_tree.svg", "image/svg+xml"))
	mux.HandleFunc("/api/examples", handleExamples)
	mux.HandleFunc("/api/providers", handleProviders)
	mux.HandleFunc("/api/history", handleHistory)
	mux.HandleFunc("/api/render", handleRender)
	mux.HandleFunc("/api/chat", handleChat)
	return mux
}

func handleDocsHTML(w http.ResponseWriter, _ *http.Request) {
	data, err := webAppFS.ReadFile("docs/build/ir.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	doc := strings.ReplaceAll(string(data), "../dark.css", "/docs/dark.css")
	doc = strings.ReplaceAll(doc, "../static/ctl_tree.svg", "/docs/static/ctl_tree.svg")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(doc))
}

func handleIndex(w http.ResponseWriter, _ *http.Request) {
	data, err := webAppFS.ReadFile("webapp/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func handleAsset(path, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		data, err := webAppFS.ReadFile(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	}
}

func handleExamples(w http.ResponseWriter, _ *http.Request) {
	examples, err := docExamples()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]examplePayload, 0, len(examples))
	for _, item := range examples {
		out = append(out, examplePayload{Title: item.Title, Source: item.Source})
	}
	writeJSON(w, out)
}

func handleProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, buildProviderCatalog(context.Background()))
}

func handleHistory(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state, err := readHistoryState()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, state)
	case http.MethodPut:
		var state historyState
		if err := json.NewDecoder(r.Body).Decode(&state); err != nil {
			writeJSONError(w, http.StatusBadRequest, err)
			return
		}
		if err := writeHistoryState(state); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	case http.MethodDelete:
		if err := clearHistoryState(); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleRender(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req renderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	markdown, html, err := renderInterpretation(req.Source)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, renderResponse{Markdown: markdown, HTML: html})
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Messages) == 0 {
		writeJSONError(w, http.StatusBadRequest, fmt.Errorf("chat request needs at least one message"))
		return
	}
	provider := req.Provider
	if provider == "" {
		provider = buildProviderCatalog(r.Context()).Default
	}
	model := req.Model
	if model == "" {
		model = defaultModelForProvider(provider)
	}
	content, err := chatWithProvider(r.Context(), provider, model, req.Source, req.Messages)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, explainChatError(err))
		return
	}
	writeJSON(w, chatResponse{Provider: provider, Model: model, Content: content})
}

func historyFilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "go-ctl2", "history.json")
}

func readHistoryState() (historyState, error) {
	path := historyFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return historyState{}, nil
		}
		return historyState{}, err
	}
	var state historyState
	if len(data) == 0 {
		return historyState{}, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return historyState{}, err
	}
	return state, nil
}

func writeHistoryState(state historyState) error {
	path := historyFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func clearHistoryState() error {
	path := historyFilePath()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func renderInterpretation(source string) (string, string, error) {
	spec, err := CompileModel(source)
	if err != nil {
		return "", "", err
	}
	markdown, err := renderRequirementsMarkdown(spec)
	if err != nil {
		return "", "", err
	}
	html, err := renderRequirementsHTML(spec)
	if err != nil {
		return "", "", err
	}
	return markdown, html, nil
}

func buildProviderCatalog(ctx context.Context) providerCatalog {
	providers := []providerInfo{
		{
			ID:        "builtin",
			Label:     "Built-in",
			Available: true,
			Models:    []string{"explain", "render", "tla"},
		},
		loadOpenAIProvider(ctx),
		loadAnthropicProvider(ctx),
	}
	defaultProvider := "builtin"
	for _, provider := range providers {
		if provider.Available && provider.ID != "builtin" {
			defaultProvider = provider.ID
			break
		}
	}
	return providerCatalog{Providers: providers, Default: defaultProvider}
}

func loadOpenAIProvider(ctx context.Context) providerInfo {
	_, available := lookupEnvLogged("OPENAI_API_KEY")
	info := providerInfo{
		ID:        "openai",
		Label:     "OpenAI",
		Available: available,
		Models:    []string{"gpt-5.2", "gpt-5-mini", "gpt-4.1"},
	}
	if !info.Available {
		info.Error = "set OPENAI_API_KEY"
		return info
	}
	models, err := fetchOpenAIModels(ctx)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	if len(models) > 0 {
		info.Models = models
	}
	return info
}

func loadAnthropicProvider(ctx context.Context) providerInfo {
	_, available := lookupEnvLogged("ANTHROPIC_API_KEY")
	info := providerInfo{
		ID:        "claude",
		Label:     "Claude",
		Available: available,
		Models:    []string{"claude-sonnet-4-20250514", "claude-opus-4-20250514"},
	}
	if !info.Available {
		info.Error = "set ANTHROPIC_API_KEY"
		return info
	}
	models, err := fetchAnthropicModels(ctx)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	if len(models) > 0 {
		info.Models = models
	}
	return info
}

func defaultModelForProvider(provider string) string {
	switch provider {
	case "openai":
		return "gpt-5.2"
	case "claude":
		return "claude-sonnet-4-20250514"
	case "builtin":
		return "explain"
	default:
		return "explain"
	}
}

func fetchOpenAIModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.openai.com/v1/models", nil)
	if err != nil {
		return nil, err
	}
	key, _ := lookupEnvLogged("OPENAI_API_KEY")
	req.Header.Set("Authorization", "Bearer "+key)
	body, err := doJSONRequest(req)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	var models []string
	for _, item := range payload.Data {
		if strings.HasPrefix(item.ID, "gpt-") || strings.HasPrefix(item.ID, "o") {
			models = append(models, item.ID)
		}
	}
	sort.Strings(models)
	if len(models) > 30 {
		models = models[:30]
	}
	return models, nil
}

func fetchAnthropicModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return nil, err
	}
	key, _ := lookupEnvLogged("ANTHROPIC_API_KEY")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	body, err := doJSONRequest(req)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	var models []string
	for _, item := range payload.Data {
		models = append(models, item.ID)
	}
	return models, nil
}

func chatWithProvider(ctx context.Context, provider, model, source string, messages []chatTurn) (string, error) {
	switch provider {
	case "builtin":
		content, err := builtinChatReply(model, source, messages)
		if err == nil {
			return content, nil
		}
		if !errors.Is(err, errBuiltinPassThrough) {
			return "", err
		}
		fallbackProvider := fallbackLLMProvider()
		if fallbackProvider == "" {
			return "No external LLM is configured for open-ended chat.\n\nSet `OPENAI_API_KEY` or `ANTHROPIC_API_KEY`, then pick that provider in the header. The built-in provider only handles local diagrams, CTL explainers, TLA sketches, and model interpretation.", nil
		}
		return chatWithProvider(ctx, fallbackProvider, defaultModelForProvider(fallbackProvider), source, messages)
	case "openai":
		content, err := chatWithOpenAI(ctx, model, source, messages)
		if err != nil {
			return "", err
		}
		content, err = maybeRepairModelReply(source, messages, content, func(retryMessages []chatTurn) (string, error) {
			return chatWithOpenAI(ctx, model, source, retryMessages)
		})
		if err != nil {
			return "", err
		}
		return maybeExecuteLispReply(source, messages, content)
	case "claude":
		content, err := chatWithAnthropic(ctx, model, source, messages)
		if err != nil {
			return "", err
		}
		content, err = maybeRepairModelReply(source, messages, content, func(retryMessages []chatTurn) (string, error) {
			return chatWithAnthropic(ctx, model, source, retryMessages)
		})
		if err != nil {
			return "", err
		}
		return maybeExecuteLispReply(source, messages, content)
	default:
		return "", fmt.Errorf("unknown provider %q", provider)
	}
}

func maybeRepairModelReply(source string, messages []chatTurn, content string, retry func([]chatTurn) (string, error)) (string, error) {
	modelSource, ok := extractModelSource(content)
	if !ok {
		return content, nil
	}
	if _, _, err := renderInterpretation(modelSource); err == nil {
		return content, nil
	}
	current := content
	currentSource := modelSource
	lastCompileErr := ""
	repairMessages := append([]chatTurn{}, messages...)
	for attempt := 1; attempt <= 3; attempt++ {
		_, _, err := renderInterpretation(currentSource)
		if err == nil {
			return current, nil
		}
		lastCompileErr = err.Error()
		repairMessages = append(repairMessages, chatTurn{
			Role:    "assistant",
			Content: current,
		}, chatTurn{
			Role:    "user",
			Content: repairPromptForModel(source, lastCompileErr, attempt),
		})
		repaired, retryErr := retry(repairMessages)
		if retryErr != nil {
			return current + "\n\nExplanation: the generated model did not compile, and the automatic repair request failed before a corrected model came back.\n\n```text\n" + retryErr.Error() + "\n```", nil
		}
		repairedSource, repairedOK := extractModelSource(repaired)
		if !repairedOK {
			current = repaired + "\n\nExplanation: the automatic repair attempt did not return a Lisp model. Do this: ask the LLM to return only one ```lisp fenced block."
			currentSource = ""
			continue
		}
		current = repaired
		currentSource = repairedSource
	}
	if strings.TrimSpace(currentSource) == "" {
		return current + "\n\nExplanation: I gave the LLM multiple chances to repair the model, but it never returned a checkable Lisp model.", nil
	}
	return current + "\n\nExplanation: I gave the LLM multiple chances to repair the model, but the final version still does not compile. Do this: inspect the compiler error below and ask for one more focused repair.\n\n```text\n" + lastCompileErr + "\n```", nil
}

func repairPromptForModel(source, compileErr string, attempt int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The Lisp model you just produced does not compile. This is automatic repair attempt %d.\n\n", attempt)
	b.WriteString("Compiler error:\n```text\n")
	b.WriteString(compileErr)
	b.WriteString("\n```\n\n")
	b.WriteString("Do this:\n")
	b.WriteString("- fix the model so it compiles under go-ctl2\n")
	b.WriteString("- preserve the user's intent\n")
	b.WriteString("- return only one corrected Lisp model in a ```lisp fenced block\n")
	b.WriteString("- do not explain the error without also returning the corrected model\n")
	b.WriteString("- use `in-state` and `mailbox-has` for CTL atoms when needed\n\n")
	b.WriteString("Current model context:\n```lisp\n")
	b.WriteString(source)
	b.WriteString("\n```")
	return b.String()
}

func extractModelSource(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "(model") {
		return trimmed, true
	}
	parts := strings.Split(content, "```")
	for i := 1; i < len(parts); i += 2 {
		part := parts[i]
		newline := strings.Index(part, "\n")
		lang := ""
		body := part
		if newline >= 0 {
			lang = strings.TrimSpace(part[:newline])
			body = part[newline+1:]
		}
		body = strings.TrimSpace(body)
		if body == "" || !strings.HasPrefix(body, "(model") {
			continue
		}
		switch strings.ToLower(lang) {
		case "", "lisp", "scheme", "clojure":
			return body, true
		}
	}
	return "", false
}

func maybeExecuteLispReply(defaultModelSource string, messages []chatTurn, content string) (string, error) {
	if _, ok := extractModelSource(content); ok {
		return content, nil
	}
	formulaSource, logicKind := extractLogicFormula(content)
	if formulaSource == "" {
		return content, nil
	}
	modelSource, modelLabel := resolveEvaluationModelSource(defaultModelSource, content, messages)
	if strings.TrimSpace(modelSource) == "" {
		return content + "\n\nEngine execution note:\n\nI found Lisp in the reply, but I do not know which model to evaluate it against. Reference a prior model message like `A12` or rely on the current Model tab.", nil
	}
	spec, err := CompileModel(modelSource)
	if err != nil {
		return content + "\n\nEngine execution note:\n\nI found Lisp in the reply, but the referenced model does not compile yet.\n\n```text\n" + err.Error() + "\n```", nil
	}
	explored, err := ExploreModel(spec.Runtime())
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(content)
	b.WriteString("\n\n## Engine Execution\n\n")
	b.WriteString("- model source: ")
	b.WriteString(modelLabel)
	b.WriteString("\n")
	switch logicKind {
	case "mu":
		formula, err := CompileMu(formulaSource)
		if err != nil {
			b.WriteString("- result: could not parse the mu-calculus formula\n\n```text\n")
			b.WriteString(err.Error())
			b.WriteString("\n```")
			return b.String(), nil
		}
		fmt.Fprintf(&b, "- logic: raw modal mu-calculus\n- formula: `%s`\n- holds at the initial state: `%t`\n", formulaSource, explored.HoldsMuAtInitial(formula))
	case "ctl":
		formula, err := CompileCTL(formulaSource)
		if err != nil {
			b.WriteString("- result: could not parse the CTL formula\n\n```text\n")
			b.WriteString(err.Error())
			b.WriteString("\n```")
			return b.String(), nil
		}
		fmt.Fprintf(&b, "- logic: CTL\n- formula: `%s`\n- holds at the initial state: `%t`\n", formulaSource, explored.HoldsAtInitial(formula))
	}
	return b.String(), nil
}

func explainChatError(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return fmt.Errorf("chat failed, but no error details were returned. Retry the request, and if it repeats, check the selected provider configuration")
	}
	if strings.Contains(strings.ToLower(msg), "connection refused") {
		return fmt.Errorf("chat failed because the selected provider or local server could not be reached. Check that the server is running and that any required API endpoint is reachable\n\ntechnical detail: %s", msg)
	}
	if strings.Contains(strings.ToLower(msg), "api key") || strings.Contains(strings.ToLower(msg), "unauthorized") {
		return fmt.Errorf("chat failed because the selected LLM provider is not configured correctly. Set the required API key and retry\n\ntechnical detail: %s", msg)
	}
	return fmt.Errorf("chat failed before a usable answer could be returned. Retry once, and if it repeats, inspect the technical detail below\n\ntechnical detail: %s", msg)
}

func builtinChatReply(model, source string, messages []chatTurn) (string, error) {
	last := messages[len(messages)-1].Content
	lower := strings.ToLower(last)
	if reply, ok, err := maybeEvaluateLogicRequest(source, messages); ok {
		return reply, err
	}
	if strings.Contains(lower, "sequence diagram") && strings.Contains(lower, "just talked about") {
		return renderConversationSequence(messages), nil
	}
	if sequence := simpleSequencePrompt(last); sequence != nil {
		return renderSimpleSequence(sequence), nil
	}
	if sequenceClarificationPrompt(lower) {
		return renderSequenceClarificationQuestion(), nil
	}
	if isCTLClarificationRequest(lower) {
		return renderCTLClarification(), nil
	}
	if shape := shapeSubject(lower); shape != "" {
		return renderShapeReply(shape), nil
	}
	if subject := imageSubject(lower, last); subject != "" {
		return renderPictureReply(subject)
	}
	if strings.Contains(lower, "what is ctl") || strings.Contains(lower, "computation tree logic") || (strings.Contains(lower, "ctl") && strings.Contains(lower, "diagram")) {
		return renderCTLExplainer(), nil
	}
	if strings.Contains(lower, "show me this as tla") || strings.Contains(lower, "as tla") || model == "tla" {
		spec, err := CompileModel(source)
		if err != nil {
			return "", err
		}
		return renderTLASketch(spec), nil
	}
	if strings.Contains(lower, "render") || strings.Contains(lower, "interpret") || model == "render" {
		markdown, _, err := renderInterpretation(source)
		return markdown, err
	}
	if isCurrentEventsModelRequest(lower) && fallbackLLMProvider() == "" {
		return renderCurrentEventsClarification(), nil
	}
	return "", errBuiltinPassThrough
}

func maybeEvaluateLogicRequest(source string, messages []chatTurn) (string, bool, error) {
	last := messages[len(messages)-1]
	lower := strings.ToLower(last.Content)
	if !looksLikeLogicEvaluationRequest(lower) {
		return "", false, nil
	}
	modelSource, modelLabel := resolveEvaluationModelSource(source, last.Content, messages)
	if strings.TrimSpace(modelSource) == "" {
		return "I can evaluate CTL or raw modal mu-calculus against a model, but I do not know which model you mean yet.\n\nPoint me at the current Model tab or reference a prior message like `A12` that contains a `(model ...)` block.", true, nil
	}
	formulaSource, logicKind, formulaLabel := resolveEvaluationFormula(last.Content, messages)
	if formulaSource == "" {
		return "I can evaluate logic against the model, but I do not know which formula you want.\n\nGive me a formula directly, for example `check (possibly (in-state Server done))`, or reference a prior message like `A7` that contains the CTL or mu-calculus formula.", true, nil
	}
	spec, err := CompileModel(modelSource)
	if err != nil {
		return "I found the target model, but it does not compile yet.\n\nDo this: fix the model first, then ask me to evaluate the logic again.\n\n```text\n" + err.Error() + "\n```", true, nil
	}
	explored, err := ExploreModel(spec.Runtime())
	if err != nil {
		return "", true, err
	}
	var b strings.Builder
	switch logicKind {
	case "mu":
		formula, err := CompileMu(formulaSource)
		if err != nil {
			return "I found the model, but the mu-calculus formula does not parse.\n\nDo this: rewrite it as a raw modal mu-calculus formula such as `(mu X (or (in-state A done) (diamond X)))`.\n\n```text\n" + err.Error() + "\n```", true, nil
		}
		holds := explored.HoldsMuAtInitial(formula)
		fmt.Fprintf(&b, "I evaluated the raw modal mu-calculus formula against %s.\n\n", modelLabel)
		fmt.Fprintf(&b, "- formula source: %s\n", formulaLabel)
		fmt.Fprintf(&b, "- logic: raw modal mu-calculus\n")
		fmt.Fprintf(&b, "- formula text: `%s`\n", formulaSource)
		fmt.Fprintf(&b, "- normalized formula: `%s`\n", formula.String())
		fmt.Fprintf(&b, "- holds at the initial state: `%t`\n", holds)
	case "ctl":
		formula, err := CompileCTL(formulaSource)
		if err != nil {
			return "I found the model, but the CTL formula does not parse.\n\nDo this: rewrite it as a CTL formula such as `(possibly (in-state Server done))`.\n\n```text\n" + err.Error() + "\n```", true, nil
		}
		holds := explored.HoldsAtInitial(formula)
		fmt.Fprintf(&b, "I evaluated the CTL formula against %s.\n\n", modelLabel)
		fmt.Fprintf(&b, "- formula source: %s\n", formulaLabel)
		fmt.Fprintf(&b, "- logic: CTL\n")
		fmt.Fprintf(&b, "- formula text: `%s`\n", formulaSource)
		fmt.Fprintf(&b, "- normalized formula: `%s`\n", formula.String())
		fmt.Fprintf(&b, "- holds at the initial state: `%t`\n", holds)
	default:
		return "I found a formula-shaped expression, but I cannot tell whether you meant CTL or raw modal mu-calculus.\n\nDo this: mention `CTL` or `mu-calculus` explicitly, or use a raw mu operator like `(mu X ...)` / `(nu X ...)`.", true, nil
	}
	fmt.Fprintf(&b, "- model source: %s\n", modelLabel)
	b.WriteString("\nIf you want, ask next for `the satisfying states`, `the lowered mu-calculus form`, or `the same check against A12`.\n")
	return b.String(), true, nil
}

func looksLikeLogicEvaluationRequest(lower string) bool {
	actionWords := []string{"evaluate", "check", "does", "whether", "holds", "hold?", "assert"}
	action := false
	for _, keyword := range actionWords {
		if strings.Contains(lower, keyword) {
			action = true
			break
		}
	}
	if !action {
		return false
	}
	logicWords := []string{"ctl", "mu-calculus", "mu calculus", "(mu ", "(nu ", "formula", "logic", "against"}
	for _, keyword := range logicWords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func resolveEvaluationModelSource(currentSource, prompt string, messages []chatTurn) (string, string) {
	if modelSource, ok := extractModelSource(prompt); ok {
		return modelSource, "the current message"
	}
	for _, turn := range referencedTurns(prompt, messages) {
		if modelSource, ok := extractModelSource(turn.Content); ok {
			return modelSource, turnLabel(turn)
		}
	}
	if strings.TrimSpace(currentSource) != "" {
		return currentSource, "the current Model tab"
	}
	return "", ""
}

func resolveEvaluationFormula(prompt string, messages []chatTurn) (string, string, string) {
	if formula, kind := extractLogicFormula(prompt); formula != "" {
		return formula, kind, "the current message"
	}
	for _, turn := range referencedTurns(prompt, messages) {
		if formula, kind := extractLogicFormula(turn.Content); formula != "" {
			return formula, kind, turnLabel(turn)
		}
	}
	return "", "", ""
}

func referencedTurns(prompt string, messages []chatTurn) []chatTurn {
	matches := map[int]bool{}
	upper := strings.ToUpper(prompt)
	for i := 0; i < len(upper)-1; i++ {
		prefix := upper[i]
		if prefix != 'A' && prefix != 'U' {
			continue
		}
		j := i + 1
		for j < len(upper) && upper[j] >= '0' && upper[j] <= '9' {
			j++
		}
		if j == i+1 {
			continue
		}
		n, err := strconv.Atoi(upper[i+1 : j])
		if err == nil {
			matches[n] = true
		}
		i = j - 1
	}
	var out []chatTurn
	for _, message := range messages {
		if message.Turn > 0 && matches[message.Turn] {
			out = append(out, message)
		}
	}
	return out
}

func turnLabel(turn chatTurn) string {
	prefix := "U"
	if turn.Role == "assistant" {
		prefix = "A"
	}
	if turn.Turn > 0 {
		return prefix + strconv.Itoa(turn.Turn)
	}
	return "the referenced message"
}

func extractLogicFormula(content string) (string, string) {
	for _, form := range extractParenthesizedForms(content) {
		trimmed := strings.TrimSpace(form)
		if trimmed == "" || strings.HasPrefix(trimmed, "(model") {
			continue
		}
		if _, err := CompileMu(trimmed); err == nil {
			return trimmed, "mu"
		}
		if _, err := CompileCTL(trimmed); err == nil {
			return trimmed, "ctl"
		}
	}
	return "", ""
}

func extractParenthesizedForms(text string) []string {
	var out []string
	start := -1
	depth := 0
	inString := false
	escaped := false
	for i, r := range text {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inString = false
			}
			continue
		}
		if r == '"' {
			inString = true
			continue
		}
		if r == '(' {
			if depth == 0 {
				start = i
			}
			depth++
			continue
		}
		if r == ')' && depth > 0 {
			depth--
			if depth == 0 && start >= 0 {
				out = append(out, text[start:i+1])
				start = -1
			}
		}
	}
	return out
}

type simpleSequence struct {
	From    string
	To      string
	Message string
}

func fallbackLLMProvider() string {
	if _, ok := lookupEnvLogged("OPENAI_API_KEY"); ok {
		return "openai"
	}
	if _, ok := lookupEnvLogged("ANTHROPIC_API_KEY"); ok {
		return "claude"
	}
	return ""
}

func isCurrentEventsModelRequest(lower string) bool {
	return (strings.Contains(lower, "currently happening") || strings.Contains(lower, "current") || strings.Contains(lower, "today")) &&
		(strings.Contains(lower, "create a model") || strings.Contains(lower, "model ")) &&
		(strings.Contains(lower, "war") || strings.Contains(lower, "prices") || strings.Contains(lower, "iran") || strings.Contains(lower, "organizations"))
}

func simpleSequencePrompt(original string) *simpleSequence {
	text := strings.TrimSpace(original)
	if text == "" {
		return nil
	}
	lower := strings.ToLower(text)
	markers := []string{" sends a message to ", " sends to ", " send a message to ", " send to "}
	for _, marker := range markers {
		idx := strings.Index(lower, marker)
		if idx < 0 {
			continue
		}
		from := strings.TrimSpace(text[:idx])
		rest := strings.TrimSpace(text[idx+len(marker):])
		if from == "" || rest == "" {
			return nil
		}
		to := rest
		message := "message"
		for _, separator := range []string{":", "\n"} {
			if split := strings.Index(rest, separator); split >= 0 {
				to = strings.TrimSpace(rest[:split])
				message = strings.TrimSpace(rest[split+1:])
				break
			}
		}
		if to == "" {
			return nil
		}
		if message == "" {
			message = "message"
		}
		return &simpleSequence{
			From:    cleanSequenceActor(from),
			To:      cleanSequenceActor(to),
			Message: cleanSequenceLabel(message),
		}
	}
	return nil
}

func sequenceClarificationPrompt(lower string) bool {
	if strings.Contains(lower, "sequence diagram") {
		return true
	}
	if strings.Contains(lower, " sends ") || strings.Contains(lower, " send ") {
		return true
	}
	if strings.Contains(lower, "message to") || strings.Contains(lower, "messages to") {
		return true
	}
	return false
}

func cleanSequenceActor(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Trim(name, "\"'`.,;:!?()[]{}")
	if name == "" {
		return "Actor"
	}
	return name
}

func cleanSequenceLabel(label string) string {
	label = strings.TrimSpace(label)
	label = strings.Trim(label, "\"'`")
	label = strings.ReplaceAll(label, "\n", " ")
	label = strings.Join(strings.Fields(label), " ")
	if label == "" {
		return "message"
	}
	return label
}

func renderSimpleSequence(seq *simpleSequence) string {
	var b strings.Builder
	b.WriteString("Here is that interaction as a sequence diagram:\n\n")
	b.WriteString("```mermaid\n")
	b.WriteString("sequenceDiagram\n")
	fmt.Fprintf(&b, "    participant %s\n", chatMermaidIdentifier(seq.From))
	fmt.Fprintf(&b, "    participant %s\n", chatMermaidIdentifier(seq.To))
	fmt.Fprintf(&b, "    %s->>%s: %s\n", chatMermaidIdentifier(seq.From), chatMermaidIdentifier(seq.To), chatMermaidLabel(seq.Message))
	b.WriteString("```\n\n")
	fmt.Fprintf(&b, "%s sends %s to %s.", seq.From, chatMermaidInlineLabel(seq.Message), seq.To)
	return b.String()
}

func renderSequenceClarificationQuestion() string {
	var b strings.Builder
	b.WriteString("I think you may be asking for an interaction diagram, but I am not confident about the participants or message labels.\n\n")
	b.WriteString("Please restate it in one of these shapes:\n\n")
	b.WriteString("- `A sends a message to B: ping`\n")
	b.WriteString("- `draw a sequence diagram where Client asks Server for quote`\n")
	b.WriteString("- `A -> B: ping, then B -> C: relay`\n\n")
	b.WriteString("If you want, give me the participants and the messages in plain English and I will draw it.")
	return b.String()
}

func renderCurrentEventsClarification() string {
	var b strings.Builder
	b.WriteString("I can help with that, but I need one clarification first.\n\n")
	b.WriteString("Do you want:\n\n")
	b.WriteString("- a live current-events answer from OpenAI or Claude, using up-to-date information, or\n")
	b.WriteString("- a hypothetical go-ctl2 model built only from the situation as you describe it here\n\n")
	b.WriteString("Right now no external LLM is configured, so I can only do the second one locally unless you set `OPENAI_API_KEY` or `ANTHROPIC_API_KEY`.")
	return b.String()
}

func chatMermaidIdentifier(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Actor"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "Actor"
	}
	return b.String()
}

func chatMermaidLabel(text string) string {
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\"", "'")
	return text
}

func chatMermaidInlineLabel(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "a message"
	}
	return "`" + chatMermaidLabel(text) + "`"
}

func isCTLClarificationRequest(lower string) bool {
	return (strings.Contains(lower, "ctl") || strings.Contains(lower, "proposition") || strings.Contains(lower, "state")) &&
		(strings.Contains(lower, "confused") || strings.Contains(lower, "it should") || strings.Contains(lower, "every state") || strings.Contains(lower, "true or false"))
}

func renderCTLClarification() string {
	var b strings.Builder
	b.WriteString("Yes. That objection is right, and the earlier diagram was too loose.\n\n")
	b.WriteString("In Kripke semantics, each state carries a valuation of atomic propositions. ")
	b.WriteString("So a better picture is not \"state `t1` means proposition `t1`\". ")
	b.WriteString("A better picture is \"state `t1` is a world, and inside that world some propositions are true and others are false.\"\n\n")
	b.WriteString("For example:\n\n")
	b.WriteString("```svg\n")
	b.WriteString("<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"640\" height=\"260\" viewBox=\"0 0 640 260\">\n")
	b.WriteString("  <rect width=\"640\" height=\"260\" fill=\"#0c1420\"/>\n")
	b.WriteString("  <g stroke=\"#8bc3ff\" stroke-width=\"4\" fill=\"none\">\n")
	b.WriteString("    <path d=\"M100 130 L260 70\"/>\n")
	b.WriteString("    <path d=\"M100 130 L260 190\"/>\n")
	b.WriteString("    <path d=\"M380 70 L540 70\"/>\n")
	b.WriteString("    <path d=\"M380 190 L540 190\"/>\n")
	b.WriteString("  </g>\n")
	b.WriteString("  <g fill=\"#172132\" stroke=\"#ffb347\" stroke-width=\"3\">\n")
	b.WriteString("    <circle cx=\"100\" cy=\"130\" r=\"28\"/>\n")
	b.WriteString("    <rect x=\"260\" y=\"32\" width=\"120\" height=\"76\" rx=\"14\"/>\n")
	b.WriteString("    <rect x=\"260\" y=\"152\" width=\"120\" height=\"76\" rx=\"14\"/>\n")
	b.WriteString("    <rect x=\"540\" y=\"32\" width=\"68\" height=\"76\" rx=\"14\"/>\n")
	b.WriteString("    <rect x=\"540\" y=\"152\" width=\"68\" height=\"76\" rx=\"14\"/>\n")
	b.WriteString("  </g>\n")
	b.WriteString("  <g fill=\"#edf3fb\" font-family=\"Georgia, serif\" font-size=\"18\" text-anchor=\"middle\">\n")
	b.WriteString("    <text x=\"100\" y=\"136\">s</text>\n")
	b.WriteString("    <text x=\"320\" y=\"58\">t1</text>\n")
	b.WriteString("    <text x=\"320\" y=\"78\">p = true</text>\n")
	b.WriteString("    <text x=\"320\" y=\"98\">q = false</text>\n")
	b.WriteString("    <text x=\"320\" y=\"178\">t2</text>\n")
	b.WriteString("    <text x=\"320\" y=\"198\">p = false</text>\n")
	b.WriteString("    <text x=\"320\" y=\"218\">q = true</text>\n")
	b.WriteString("    <text x=\"574\" y=\"58\">p</text>\n")
	b.WriteString("    <text x=\"574\" y=\"178\">q</text>\n")
	b.WriteString("  </g>\n")
	b.WriteString("</svg>\n")
	b.WriteString("```\n\n")
	b.WriteString("That is the important distinction:\n\n")
	b.WriteString("- states are worlds or runtime snapshots\n")
	b.WriteString("- propositions are predicates evaluated at those worlds\n")
	b.WriteString("- `EX p` means there exists a successor state where proposition `p` is true\n")
	b.WriteString("- `AG p` means on every reachable path, at every state, `p` remains true\n\n")
	b.WriteString("So you were right to reject the earlier reading. ")
	b.WriteString("The node labels should have been world labels, with proposition truth shown inside each world or attached as annotations.\n\n")
	b.WriteString("If you want, I can now redraw the CTL explainer with:\n\n")
	b.WriteString("1. states as boxes containing valuations\n")
	b.WriteString("2. actor-runtime states from your Lisp model\n")
	b.WriteString("3. a side-by-side comparison of Kripke states vs propositions")
	return b.String()
}

func renderTLASketch(spec *RequirementsModel) string {
	var actorNames []string
	for _, actor := range spec.Actors {
		actorNames = append(actorNames, actor.Name)
	}
	var b strings.Builder
	b.WriteString("A rough TLA+ sketch for the current model:\n\n")
	b.WriteString("```tla\n")
	b.WriteString("---- MODULE Requirements ----\n")
	fmt.Fprintf(&b, "CONSTANT Actors\nASSUME Actors = {%s}\n\n", strings.Join(actorNames, ", "))
	b.WriteString("VARIABLES state, mailbox, data\n\n")
	b.WriteString("Init ==\n")
	for i, actor := range spec.Actors {
		prefix := "    /\\"
		if i == 0 {
			prefix = "    /\\"
		}
		fmt.Fprintf(&b, "%s state[%q] = %q\n", prefix, actor.Name, actor.States[0].Name)
	}
	b.WriteString("    /\\ mailbox \\in [Actors -> Seq(STRING)]\n")
	b.WriteString("    /\\ data \\in [Actors -> [STRING -> STRING]]\n\n")
	b.WriteString("Next ==\n")
	b.WriteString("    \\E a \\in Actors:\n")
	b.WriteString("        \\/ \\* one actor step fires\n")
	b.WriteString("        \\/ \\* sends append to mailbox[target]\n")
	b.WriteString("        \\/ \\* recvs consume from mailbox[a]\n")
	b.WriteString("        \\/ \\* become updates state[a]\n\n")
	b.WriteString("Spec == Init /\\ [][Next]_<<state, mailbox, data>>\n")
	b.WriteString("====\n")
	b.WriteString("```\n\n")
	b.WriteString("This is intentionally a sketch, not a full translation. It preserves the same state/mailer/control structure so you can compare the IR to a TLA-style presentation.")
	return b.String()
}

func shapeSubject(lower string) string {
	shapes := []string{"circle", "diamond", "square", "triangle"}
	for _, shape := range shapes {
		if strings.Contains(lower, "draw a "+shape) || strings.TrimSpace(lower) == shape {
			return shape
		}
	}
	return ""
}

func renderShapeReply(shape string) string {
	var body string
	switch shape {
	case "circle":
		body = "<circle cx=\"120\" cy=\"120\" r=\"72\" fill=\"none\" stroke=\"#8bc3ff\" stroke-width=\"8\"/>"
	case "diamond":
		body = "<polygon points=\"120,34 206,120 120,206 34,120\" fill=\"none\" stroke=\"#8bc3ff\" stroke-width=\"8\"/>"
	case "square":
		body = "<rect x=\"48\" y=\"48\" width=\"144\" height=\"144\" fill=\"none\" stroke=\"#8bc3ff\" stroke-width=\"8\"/>"
	case "triangle":
		body = "<polygon points=\"120,34 206,206 34,206\" fill=\"none\" stroke=\"#8bc3ff\" stroke-width=\"8\"/>"
	default:
		body = "<circle cx=\"120\" cy=\"120\" r=\"72\" fill=\"none\" stroke=\"#8bc3ff\" stroke-width=\"8\"/>"
	}
	return "```svg\n<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"240\" height=\"240\" viewBox=\"0 0 240 240\"><rect width=\"240\" height=\"240\" fill=\"#0c1420\"/>" + body + "</svg>\n```"
}

func imageSubject(lower, original string) string {
	prefixes := []string{
		"show me a picture of ",
		"show me a photo of ",
		"show me an image of ",
		"picture of ",
		"photo of ",
		"image of ",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(original[len(prefix):])
		}
	}
	return ""
}

func renderPictureReply(subject string) (string, error) {
	imageURL, pageTitle, err := fetchWikipediaImage(subject)
	if err != nil {
		return "", err
	}
	if imageURL == "" {
		return fmt.Sprintf("I could not find an image for `%s`.\n\nTry a more specific page title or ask for a diagram instead.", subject), nil
	}
	return fmt.Sprintf("Here is an image for `%s` from Wikipedia:\n\n![%s](%s)\n\nSource page: `%s`", subject, subject, imageURL, pageTitle), nil
}

func fetchWikipediaImage(subject string) (string, string, error) {
	title := strings.ReplaceAll(strings.TrimSpace(subject), " ", "_")
	if title == "" {
		return "", "", nil
	}
	endpoint := "https://en.wikipedia.org/api/rest_v1/page/summary/" + url.PathEscape(title)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "go-ctl2/0.1 (+https://example.invalid/go-ctl2; self-contained requirements explorer)")
	req.Header.Set("Accept", "application/json")
	body, err := doJSONRequest(req)
	if err != nil {
		return "", title, err
	}
	var payload wikipediaSummaryResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", err
	}
	if payload.OriginalImage != nil && payload.OriginalImage.Source != "" {
		return payload.OriginalImage.Source, payload.Title, nil
	}
	if payload.Thumbnail != nil && payload.Thumbnail.Source != "" {
		return payload.Thumbnail.Source, payload.Title, nil
	}
	return "", payload.Title, nil
}

func renderConversationSequence(messages []chatTurn) string {
	var b strings.Builder
	b.WriteString("Here is a sequence diagram of the recent conversation:\n\n")
	b.WriteString("```mermaid\n")
	b.WriteString("sequenceDiagram\n")
	b.WriteString("    participant User\n")
	b.WriteString("    participant Assistant\n")
	start := 0
	if len(messages) > 8 {
		start = len(messages) - 8
	}
	for _, message := range messages[start:] {
		line := conversationSnippet(message.Content)
		if message.Role == "assistant" {
			fmt.Fprintf(&b, "    Assistant-->>User: %s\n", line)
		} else {
			fmt.Fprintf(&b, "    User->>Assistant: %s\n", line)
		}
	}
	b.WriteString("```\n")
	return b.String()
}

func conversationSnippet(text string) string {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, "\n", " ")
	if len(text) > 72 {
		text = text[:69] + "..."
	}
	text = strings.ReplaceAll(text, "\"", "'")
	return text
}

func renderCTLExplainer() string {
	var b strings.Builder
	b.WriteString("CTL is Computation Tree Logic. It talks about a branching tree of possible futures rather than one linear trace.\n\n")
	b.WriteString("- `EX p` means there exists a next state where `p` holds\n")
	b.WriteString("- `AX p` means every next state satisfies `p`\n")
	b.WriteString("- `EF p` means some branch eventually reaches `p`\n")
	b.WriteString("- `AG p` means every branch preserves `p` forever\n\n")
	b.WriteString("Here is the shape CTL reasons about:\n\n")
	b.WriteString("```svg\n")
	b.WriteString("<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"520\" height=\"260\" viewBox=\"0 0 520 260\">\n")
	b.WriteString("  <rect width=\"520\" height=\"260\" fill=\"#0c1420\"/>\n")
	b.WriteString("  <g stroke=\"#8bc3ff\" stroke-width=\"4\" fill=\"none\">\n")
	b.WriteString("    <path d=\"M260 44 L140 118\"/>\n")
	b.WriteString("    <path d=\"M260 44 L260 118\"/>\n")
	b.WriteString("    <path d=\"M260 44 L380 118\"/>\n")
	b.WriteString("    <path d=\"M140 118 L88 206\"/>\n")
	b.WriteString("    <path d=\"M140 118 L192 206\"/>\n")
	b.WriteString("    <path d=\"M380 118 L328 206\"/>\n")
	b.WriteString("    <path d=\"M380 118 L432 206\"/>\n")
	b.WriteString("  </g>\n")
	b.WriteString("  <g fill=\"#172132\" stroke=\"#ffb347\" stroke-width=\"3\">\n")
	b.WriteString("    <circle cx=\"260\" cy=\"44\" r=\"22\"/>\n")
	b.WriteString("    <circle cx=\"140\" cy=\"118\" r=\"20\"/>\n")
	b.WriteString("    <circle cx=\"260\" cy=\"118\" r=\"20\"/>\n")
	b.WriteString("    <circle cx=\"380\" cy=\"118\" r=\"20\"/>\n")
	b.WriteString("    <circle cx=\"88\" cy=\"206\" r=\"18\"/>\n")
	b.WriteString("    <circle cx=\"192\" cy=\"206\" r=\"18\"/>\n")
	b.WriteString("    <circle cx=\"328\" cy=\"206\" r=\"18\"/>\n")
	b.WriteString("    <circle cx=\"432\" cy=\"206\" r=\"18\"/>\n")
	b.WriteString("  </g>\n")
	b.WriteString("  <g fill=\"#edf3fb\" font-family=\"Georgia, serif\" font-size=\"18\" text-anchor=\"middle\">\n")
	b.WriteString("    <text x=\"260\" y=\"50\">s</text>\n")
	b.WriteString("    <text x=\"140\" y=\"124\">t1</text>\n")
	b.WriteString("    <text x=\"260\" y=\"124\">t2</text>\n")
	b.WriteString("    <text x=\"380\" y=\"124\">t3</text>\n")
	b.WriteString("    <text x=\"88\" y=\"212\">p</text>\n")
	b.WriteString("    <text x=\"192\" y=\"212\">not p</text>\n")
	b.WriteString("    <text x=\"328\" y=\"212\">p</text>\n")
	b.WriteString("    <text x=\"432\" y=\"212\">q</text>\n")
	b.WriteString("  </g>\n")
	b.WriteString("</svg>\n")
	b.WriteString("```\n\n")
	b.WriteString("And here is the same idea as a branching diagram:\n\n")
	b.WriteString("```mermaid\n")
	b.WriteString("flowchart TD\n")
	b.WriteString("    s((s))\n")
	b.WriteString("    s --> t1((t1))\n")
	b.WriteString("    s --> t2((t2))\n")
	b.WriteString("    s --> t3((t3))\n")
	b.WriteString("    t1 --> p1((p))\n")
	b.WriteString("    t1 --> np1((not p))\n")
	b.WriteString("    t3 --> p2((p))\n")
	b.WriteString("    t3 --> q((q))\n")
	b.WriteString("```\n\n")
	b.WriteString("You can read those operators against the picture:\n\n")
	b.WriteString("- `EX p` is true at `s` if one immediate child is labeled `p`\n")
	b.WriteString("- `AX p` is true at `s` only if all immediate children satisfy `p`\n")
	b.WriteString("- `EF q` is true if some branch can eventually reach `q`\n")
	b.WriteString("- `AG p` is false if any branch can fall into `not p`\n\n")
	b.WriteString("If you want, ask next: `show me CTL versus LTL` or `show me CTL on my current Lisp model`.")
	return b.String()
}

func chatWithOpenAI(ctx context.Context, model, source string, messages []chatTurn) (string, error) {
	requestBody := map[string]interface{}{
		"model":    model,
		"messages": openAIChatMessages(source, messages),
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	key, _ := lookupEnvLogged("OPENAI_API_KEY")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	respBody, err := doJSONRequest(req)
	if err != nil {
		return "", err
	}
	var payload openAIChatResponse
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return "", err
	}
	if payload.Error != nil {
		return "", fmt.Errorf(payload.Error.Message)
	}
	if len(payload.Choices) == 0 {
		return "", fmt.Errorf("openai returned no choices")
	}
	return openAIContentText(payload.Choices[0].Message.Content), nil
}

func openAIChatMessages(source string, messages []chatTurn) []map[string]string {
	out := []map[string]string{
		{
			"role":    "system",
			"content": chatSystemPrompt(source),
		},
	}
	for _, item := range messages {
		out = append(out, map[string]string{
			"role":    item.Role,
			"content": item.Content,
		})
	}
	return out
}

func openAIContentText(content interface{}) string {
	switch value := content.(type) {
	case string:
		return value
	case []interface{}:
		var parts []string
		for _, item := range value {
			if obj, ok := item.(map[string]interface{}); ok {
				if text, ok := obj["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return fmt.Sprint(content)
	}
}

func chatWithAnthropic(ctx context.Context, model, source string, messages []chatTurn) (string, error) {
	requestBody := map[string]interface{}{
		"model":      model,
		"max_tokens": 2048,
		"system":     chatSystemPrompt(source),
		"messages":   anthropicMessages(messages),
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	key, _ := lookupEnvLogged("ANTHROPIC_API_KEY")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	respBody, err := doJSONRequest(req)
	if err != nil {
		return "", err
	}
	var payload anthropicMessageResponse
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return "", err
	}
	if payload.Error != nil {
		return "", fmt.Errorf(payload.Error.Message)
	}
	var parts []string
	for _, item := range payload.Content {
		if item.Type == "text" {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

func anthropicMessages(messages []chatTurn) []map[string]string {
	out := make([]map[string]string, 0, len(messages))
	for _, item := range messages {
		role := item.Role
		if role != "assistant" {
			role = "user"
		}
		out = append(out, map[string]string{
			"role":    role,
			"content": item.Content,
		})
	}
	return out
}

func chatSystemPrompt(source string) string {
	var b strings.Builder
	b.WriteString("You are helping the user reason about a go-ctl2 Lisp requirements model.\n")
	b.WriteString("Answer in Markdown.\n")
	b.WriteString("Answer naturally. The app renders normal Markdown text, Markdown images and gifs like ![alt](url), Mermaid fenced blocks, SVG fenced blocks, HTML fenced blocks, fenced `canvas` blocks for JavaScript canvas animations, and LaTeX math using $...$ or $$...$$.\n")
	b.WriteString("The UI is dark mode. For SVG, HTML, Mermaid, and canvas output, make the result readable on a dark background: use explicit high-contrast colors, and for canvas especially, paint a visible background instead of assuming a white page.\n")
	b.WriteString("For canvas animations, respect the provided `width` and `height`. Keep the drawing on-screen, choose coordinates from those dimensions, and avoid placing shapes or labels outside the visible canvas.\n")
	b.WriteString("Be concrete and refer to the current model, not generic theory.\n")
	b.WriteString("If asked to restate or translate the model, preserve the actual control states, mailboxes, and assertions.\n")
	b.WriteString("When critiquing a model, explicitly check whether it is deterministic or actually branches, whether EF/AF or similar properties collapse because there is only one execution, whether queue capacities matter, whether consequences are represented as checkable state/data instead of just labels, and whether the actors really model organizations versus abstract systems.\n")
	b.WriteString("If the model is too weak to support the requested conclusion, say exactly what is missing and propose concrete actor/data/branching/assertion changes.\n")
	b.WriteString("Never place `send`, `send-any`, or `recv` inside `if`, `do`, or any loop-like wrapper. Keep communication as top-level edge actions, and use `become` to split control flow into extra states when you need to stage work around a potentially blocking step.\n")
	b.WriteString("The runtime supports `(print value)` for trace output and the list operators `(cons head tail)`, `(car xs)`, and `(cdr xs)`.\n")
	b.WriteString("If you return Lisp, keep it executable. Return a full `(model ...)` when proposing a model. If you return one CTL or raw modal mu-calculus formula, the app may evaluate it locally against a referenced prior model such as `A12` or against the current Model tab by default.\n")
	b.WriteString("The detailed reference docs are available in the app at /docs and /docs/ir.generated.md. Point the user there when they need the full language reference or semantics.\n")
	b.WriteString("You are given both the raw Lisp and the current rendition produced by the compiler. Prefer the rendition when discussing what the tool currently understands, but fall back to the raw Lisp when proposing edits.\n\n")
	b.WriteString("Current Lisp model:\n\n")
	b.WriteString("```lisp\n")
	b.WriteString(source)
	b.WriteString("\n```\n\n")
	b.WriteString(currentRenderedContext(source))
	return b.String()
}

func currentRenderedContext(source string) string {
	spec, err := CompileModel(source)
	if err != nil {
		return "Current compiler result:\n\n```text\n" + err.Error() + "\n```\n"
	}
	markdown, err := renderRequirementsMarkdown(spec)
	if err != nil {
		return "Current compiler result:\n\n```text\n" + err.Error() + "\n```\n"
	}
	return "Current rendered interpretation:\n\n```markdown\n" + markdown + "\n```\n"
}

func doJSONRequest(req *http.Request) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	return body, nil
}

func lookupEnvLogged(name string) (string, bool) {
	value, ok := os.LookupEnv(name)
	status := "unset"
	if ok && value != "" {
		status = "set"
	}
	fmt.Fprintf(envLog, "env lookup %s: %s. To set it: export %s=...\n", name, status, name)
	return value, ok && value != ""
}

func writeJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONError(w http.ResponseWriter, status int, err error) {
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"error": err.Error()})
}

func runFlagMode(args []string) (bool, int) {
	if len(args) == 0 || !strings.HasPrefix(args[0], "-") {
		return false, 0
	}
	fs := flag.NewFlagSet("go-ctl2", flag.ContinueOnError)
	mode := fs.String("mode", "serve", "mode: serve or interpret")
	format := fs.String("format", "markdown", "interpretation format: markdown or html")
	listen := fs.String("listen", "127.0.0.1:8080", "listen address for server mode")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "flags: %v\n", err)
		return true, 2
	}
	switch *mode {
	case "serve":
		fmt.Fprintf(os.Stderr, "serving go-ctl2 on http://%s\n", *listen)
		if err := serveWeb(*listen); err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			return true, 1
		}
		return true, 0
	case "interpret":
		src, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "interpret: %v\n", err)
			return true, 1
		}
		markdown, html, err := renderInterpretation(string(src))
		if err != nil {
			fmt.Fprintf(os.Stderr, "interpret: %v\n", err)
			return true, 1
		}
		if *format == "html" {
			fmt.Print(html)
		} else {
			fmt.Print(markdown)
		}
		return true, 0
	default:
		fmt.Fprintf(os.Stderr, "flags: unsupported mode %q\n", *mode)
		return true, 2
	}
}
