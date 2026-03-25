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
	"sort"
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
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, chatResponse{Provider: provider, Model: model, Content: content})
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
		return chatWithOpenAI(ctx, model, source, messages)
	case "claude":
		return chatWithAnthropic(ctx, model, source, messages)
	default:
		return "", fmt.Errorf("unknown provider %q", provider)
	}
}

func builtinChatReply(model, source string, messages []chatTurn) (string, error) {
	last := messages[len(messages)-1].Content
	lower := strings.ToLower(last)
	if strings.Contains(lower, "sequence diagram") && strings.Contains(lower, "just talked about") {
		return renderConversationSequence(messages), nil
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
	return "", errBuiltinPassThrough
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
	b.WriteString("Answer naturally. The app renders normal Markdown text, Markdown images and gifs like ![alt](url), Mermaid fenced blocks, SVG fenced blocks, HTML fenced blocks, and LaTeX math using $...$ or $$...$$.\n")
	b.WriteString("Be concrete and refer to the current model, not generic theory.\n")
	b.WriteString("If asked to restate or translate the model, preserve the actual control states, mailboxes, and assertions.\n")
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
