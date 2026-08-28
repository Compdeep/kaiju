package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/tokens"
)

// Message is a single chat message in the OpenAI format.
//
// Content is the plain-text body (the common case). For multimodal input, set
// Parts instead: it carries an OpenAI content-parts array (text + image_url) and,
// when non-empty, is what gets serialized as `content`. Parts is marshal-only
// (json:"-") and never persisted — the agent's session stores text, and images
// are re-supplied per request by the host (Makeen), never held in kaiju.
type Message struct {
	Role       string        `json:"role"` // "system", "user", "assistant", "tool"
	Content    string        `json:"content,omitempty"`
	Reasoning  string        `json:"reasoning,omitempty"` // hidden reasoning from thinking models (reasoning field + lifted <think>…</think>)
	Parts      []ContentPart `json:"-"`                   // multimodal parts; overrides Content when set
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"` // set when role == "tool"
	Name       string        `json:"name,omitempty"`         // function name for tool results
}

// ContentPart is one element of a multimodal message content array.
type ContentPart struct {
	Type     string    `json:"type"`                // "text" | "image_url"
	Text     string    `json:"text,omitempty"`      // when Type == "text"
	ImageURL *ImageURL `json:"image_url,omitempty"` // when Type == "image_url"
}

// ImageURL holds an image reference — an https URL or a base64 data: URI.
type ImageURL struct {
	URL string `json:"url"`
}

// AttachImages folds images (https URLs or base64 data: URIs) into the last
// user message as OpenAI content parts — existing text first, then each image.
// No-op when there's no image or no user message. Mutates msgs in place.
func AttachImages(msgs []Message, images []string) {
	if len(images) == 0 {
		return
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		parts := make([]ContentPart, 0, len(images)+1)
		if msgs[i].Content != "" {
			parts = append(parts, ContentPart{Type: "text", Text: msgs[i].Content})
		}
		for _, img := range images {
			parts = append(parts, ContentPart{Type: "image_url", ImageURL: &ImageURL{URL: img}})
		}
		msgs[i].Parts = parts
		msgs[i].Content = ""
		return
	}
}

// MarshalJSON emits `content` as the parts array when Parts is set, else as the
// plain Content string — so text-only messages are byte-for-byte unchanged and
// existing readers (which never see Parts) are unaffected.
func (m Message) MarshalJSON() ([]byte, error) {
	type wire struct {
		Role       string     `json:"role"`
		Content    any        `json:"content,omitempty"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
		Name       string     `json:"name,omitempty"`
	}
	w := wire{Role: m.Role, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, Name: m.Name}
	if len(m.Parts) > 0 {
		w.Content = m.Parts
	} else {
		w.Content = m.Content
	}
	return json.Marshal(w)
}

// ToolCall represents a function call requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the function name and JSON-encoded arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON string
}

// ToolDef describes an available function for the model.
type ToolDef struct {
	Type     string      `json:"type"` // "function"
	Function FunctionDef `json:"function"`
}

// FunctionDef describes a function's name, description, and JSON Schema parameters.
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema object
}

// ChatRequest is the body for POST /v1/chat/completions.
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []ToolDef `json:"tools,omitempty"`
	ToolChoice  any       `json:"tool_choice,omitempty"` // "auto", "required", "none", or {"type":"function","function":{"name":"X"}}
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
	// StreamOptions asks the provider (OpenAI/OpenRouter) to emit a terminal
	// usage frame during streaming, so streamed calls are billed like normal ones.
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`

	// ResponseFormat asks the provider to hold the model to a schema. Set by
	// Complete when a caller declared one forced tool — see structured.go. A
	// caller may also set it directly.
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

// ForceToolChoice returns a tool_choice value that REQUIRES the model to call
// exactly the named tool — not merely "call some tool" (that's "required"). It
// uses the OpenAI/OpenRouter shape ({"type":"function","function":{"name":X}});
// the Anthropic client re-maps it to that provider's {"type":"tool","name":X}.
// Used to pin the planner to `plan` so a weak model can't emit a real tool call
// (e.g. web_search) directly instead of wrapping it in a plan.
func ForceToolChoice(name string) any {
	return map[string]any{"type": "function", "function": map[string]any{"name": name}}
}

// forcedToolName extracts the tool name from a ForceToolChoice value, or "" if
// the value isn't that shape. Used by providers that need to re-map it.
func forcedToolName(tc any) string {
	m, ok := tc.(map[string]any)
	if !ok || m["type"] != "function" {
		return ""
	}
	fn, ok := m["function"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := fn["name"].(string)
	return name
}

// StreamOptions controls streaming behavior for OpenAI-compatible endpoints.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ChatResponse is the response from POST /v1/chat/completions.
type ChatResponse struct {
	ID      string   `json:"id"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice is a single completion choice.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"` // "stop", "tool_calls", "length"
}

// Usage reports token counts.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Provider constants.
const (
	ProviderOpenAI     = "openai"
	ProviderAnthropic  = "anthropic"
	ProviderOpenRouter = "openrouter"
)

// Client calls an LLM chat completions endpoint (OpenAI or Anthropic).
type Client struct {
	provider string
	endpoint string
	apiKey   string
	model    string
	http     *http.Client
	limits   ModelLimits
}

// ModelLimits reports what a model can take in and give back, in tokens. Zero
// for either means the caller does not know, not that the limit is zero.
type ModelLimits func(model string) (contextTokens, maxOutputTokens int)

/*
 * Limits tells a client what its models can do.
 * desc: With it, every call this client makes sizes its reply against the model
 *       that will answer. Without it — the default — every request goes exactly
 *       as its caller wrote it.
 *
 *       Set on the client rather than at each call site because a caller that
 *       has to remember is a caller that forgets: nine of eighteen call sites
 *       sized their reply and nine did not, including the one asking for the
 *       largest reply in the engine.
 * param: fn - the lookup, or nil to leave requests alone.
 * return: the client, so this reads as part of construction.
 */
func (c *Client) Limits(fn ModelLimits) *Client {
	c.limits = fn
	return c
}

/*
 * WindowFor reports what this client's model can take in and give back.
 * desc: The same lookup Limits installed, asked rather than applied — for a
 *       caller that has to size something itself before it builds a request.
 *       A tool splitting a document into pieces is the case: it needs to know
 *       how large a piece may be, and the answer is a property of the model
 *       that will read it, not of the tool.
 *
 *       Both zero when no lookup was installed, or when the lookup does not
 *       know this model. A caller that gets zero has to decide for itself what
 *       to do about an unknown model; there is no default here, because a
 *       number invented at this level would be wrong for every caller
 *       differently.
 * return: the model's input and output limits in tokens, or 0, 0.
 */
func (c *Client) WindowFor() (contextTokens, maxOutputTokens int) {
	if c == nil || c.limits == nil {
		return 0, 0
	}
	return c.limits(c.Model())
}

/*
 * Transport replaces how this client's requests reach the endpoint.
 * desc: Complete, CompleteStream and Embed all send through the same http.Client,
 *       so supplying its transport once covers every call this client makes. An
 *       application whose model is not at the other end of an ordinary socket
 *       supplies its own here, and nothing else about the client changes: the
 *       request is still ordinary HTTP, and chunked replies still stream.
 *
 *       http.RoundTripper rather than an interface of our own, because the thing
 *       being replaced is already named in the standard library and a second name
 *       for it would need an adapter at every call site for no gain.
 * param: rt - the transport, or nil to keep the default.
 * return: the client, so this reads as part of construction.
 */
func (c *Client) Transport(rt http.RoundTripper) *Client {
	if rt != nil {
		c.http.Transport = rt
	}
	return c
}

// NewClient creates a Client targeting an OpenAI-compatible endpoint.
func NewClient(endpoint, apiKey, model string) *Client {
	return NewClientWithProvider(ProviderOpenAI, endpoint, apiKey, model)
}

// NewClientWithProvider creates a Client with an explicit provider ("openai" or "anthropic").
func NewClientWithProvider(provider, endpoint, apiKey, model string) *Client {
	if provider == "" {
		provider = ProviderOpenAI
	}
	return &Client{
		provider: provider,
		endpoint: endpoint,
		apiKey:   apiKey,
		model:    model,
		http: &http.Client{
			Timeout: 180 * time.Second,
		},
	}
}

// Model reports the model this client sends to when a request names none.
// Complete fills req.Model from it, so a caller that needs to know the model
// before the call — to size the reply against it, for instance — has to ask.
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.model
}

// chatURL returns the chat completions endpoint, handling providers that
// already include /v1 in their base URL (like OpenRouter).
func (c *Client) chatURL() string {
	ep := strings.TrimRight(c.endpoint, "/")
	if strings.HasSuffix(ep, "/v1") {
		return ep + "/chat/completions"
	}
	return ep + "/v1/chat/completions"
}

// embedURL returns the embeddings endpoint.
func (c *Client) embedURL() string {
	ep := strings.TrimRight(c.endpoint, "/")
	if strings.HasSuffix(ep, "/v1") {
		return ep + "/embeddings"
	}
	return ep + "/v1/embeddings"
}

// setAuthHeaders sets provider-appropriate auth headers on the request.
func (c *Client) setAuthHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	// OpenRouter requires HTTP-Referer and X-Title for ranking/attribution.
	// Which application gets the credit is the embedding application's to
	// decide, so both are settable — see SetAttribution.
	if c.provider == ProviderOpenRouter {
		referer, title := attribution()
		req.Header.Set("HTTP-Referer", referer)
		req.Header.Set("X-Title", title)
	}
}

// Complete sends a chat completion request and returns the response.
// Routes to the appropriate provider backend.
func (c *Client) Complete(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	c.capReply(req)

	// A stage asking for one shape gets the wire that enforces it, and gets its
	// reply back in the shape it asked for. Nothing above this knows — see
	// structured.go for what this buys and what it was measured against.
	replaced := asSchemaRequest(req, c.provider)

	var resp *ChatResponse
	var err error
	if c.provider == ProviderAnthropic {
		resp, err = c.completeAnthropic(ctx, req)
	} else {
		resp, err = c.completeOpenAI(ctx, req)
		// A model behind this endpoint that has no structured-output support
		// rejects the rewrite outright. Put the request back the way the caller
		// wrote it and send it once more: the rewrite buys enforcement, and it
		// must never cost a stage the ability to run at all.
		if replaced != nil && rejectsSchemas(err) {
			asToolRequestAgain(req, replaced)
			replaced = nil
			resp, err = c.completeOpenAI(ctx, req)
		}
	}
	// Single token-accounting chokepoint: every non-streamed LLM call for both
	// providers passes through here, and the ctx carries the (category,
	// principal) tags set upstream. Streamed calls (CompleteStream) currently
	// carry no Usage and are undercounted — see the note there.
	if err == nil && resp != nil {
		asToolReply(resp, replaced)
		tokens.AddSplit(ctx, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	}
	// The same chokepoint, for observation rather than accounting. Fires on
	// failure too, so an embedding application logging calls sees the ones
	// that errored — usually the interesting ones.
	emitCall(ctx, req, resp, err)
	return resp, err
}

// completeOpenAI sends a request to an OpenAI-compatible /v1/chat/completions endpoint.
func (c *Client) completeOpenAI(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if req.Model == "" {
		req.Model = c.model
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.chatURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setAuthHeaders(httpReq)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(data), 300))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(data, &chatResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &chatResp, nil
}

// CompleteStream streams a chat completion and calls onChunk for each text
// delta, returning the full accumulated text. Thin wrapper over
// CompleteStreamResp for callers that only need the text.
func (c *Client) CompleteStream(ctx context.Context, req *ChatRequest, onChunk func(chunk, kind string)) (string, error) {
	c.capReply(req)
	resp, err := c.CompleteStreamResp(ctx, req, onChunk)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", nil
	}
	return resp.Choices[0].Message.Content, nil
}

// CompleteStreamResp streams a chat completion (OpenAI SSE format, incl.
// OpenRouter), calling onChunk for each text delta so callers can render tokens
// live. Unlike CompleteStream's old text-only parse, it ALSO assembles any tool
// calls (from indexed argument fragments) and captures token usage via
// stream_options.include_usage — so a streamed turn supports tools and is billed
// through the same token counter as a non-streamed call. Returns a ChatResponse
// shaped exactly like Complete's, so callers can treat streamed and non-streamed
// turns identically.
func (c *Client) CompleteStreamResp(ctx context.Context, req *ChatRequest, onChunk func(chunk, kind string)) (*ChatResponse, error) {
	c.capReply(req)
	resp, err := c.completeStreamResp(ctx, req, onChunk)
	// One emit covering every return path in the implementation below,
	// including the early transport and HTTP failures.
	emitCall(ctx, req, resp, err)
	return resp, err
}

// completeStreamResp is the implementation. The exported wrapper above adds
// the observer notification, so the six return paths in here do not each
// need one.
func (c *Client) completeStreamResp(ctx context.Context, req *ChatRequest, onChunk func(chunk, kind string)) (*ChatResponse, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	req.Stream = true
	req.StreamOptions = &StreamOptions{IncludeUsage: true}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	// DEBUG (KAIJU_DUMP_REQUEST != "0"): the EXACT bytes sent to the model on a
	// streaming call (model + temperature + full messages), for byte-for-byte replay.
	// Last streaming call in a run is the aggregator, so this captures its real
	// request. Overwritten each call.
	if os.Getenv("KAIJU_DUMP_REQUEST") != "0" {
		_ = os.WriteFile("/tmp/kaiju-last-stream-request.json", body, 0o644)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.chatURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setAuthHeaders(httpReq)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(data), 300))
	}

	var content strings.Builder
	var reasoning strings.Builder
	toolsByIndex := map[int]*ToolCall{}
	var order []int // tool-call indices in first-seen order
	var usage Usage
	finish := "stop"

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // tool-arg frames can be long
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := line[6:]
		if payload == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					Reasoning string `json:"reasoning"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *Usage `json:"usage"` // present only in the terminal frame
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.FinishReason != "" {
			finish = ch.FinishReason
		}
		if ch.Delta.Reasoning != "" {
			reasoning.WriteString(ch.Delta.Reasoning)
			if onChunk != nil {
				onChunk(ch.Delta.Reasoning, "reasoning")
			}
		}
		if ch.Delta.Content != "" {
			content.WriteString(ch.Delta.Content)
			if onChunk != nil {
				onChunk(ch.Delta.Content, "content")
			}
		}
		// Tool calls stream as indexed deltas: id/name arrive once, arguments in
		// fragments. Accumulate per index.
		for _, tc := range ch.Delta.ToolCalls {
			acc, ok := toolsByIndex[tc.Index]
			if !ok {
				acc = &ToolCall{Type: "function"}
				toolsByIndex[tc.Index] = acc
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				acc.ID = tc.ID
			}
			if tc.Type != "" {
				acc.Type = tc.Type
			}
			if tc.Function.Name != "" {
				acc.Function.Name = tc.Function.Name
			}
			acc.Function.Arguments += tc.Function.Arguments
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Bill the streamed call through the same counter as non-streamed ones, so
	// chat/aggregator streaming is no longer undercounted.
	if usage.PromptTokens > 0 || usage.CompletionTokens > 0 {
		tokens.AddSplit(ctx, usage.PromptTokens, usage.CompletionTokens)
	}

	var toolCalls []ToolCall
	for _, idx := range order {
		toolCalls = append(toolCalls, *toolsByIndex[idx])
	}
	finalContent := content.String()
	reasonStr := reasoning.String()
	// Some models stream reasoning inline as <think>…</think> in the content
	// (rather than a reasoning field). Lift it out so content stays clean and the
	// thinking is captured either way.
	if clean, think := extractThink(finalContent); think != "" {
		finalContent = clean
		if reasonStr != "" {
			reasonStr += "\n"
		}
		reasonStr += think
	}
	return &ChatResponse{
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: finalContent, Reasoning: reasonStr, ToolCalls: toolCalls},
			FinishReason: finish,
		}},
		Usage: usage,
	}, nil
}

// extractThink lifts <think>…</think> blocks out of s, returning the cleaned text
// (blocks removed) and the concatenated thinking. Handles multiple blocks and an
// unterminated trailing <think> (streamed but cut off).
func extractThink(s string) (clean, think string) {
	if !strings.Contains(s, "<think>") {
		return s, ""
	}
	var out, th strings.Builder
	rest := s
	for {
		i := strings.Index(rest, "<think>")
		if i < 0 {
			out.WriteString(rest)
			break
		}
		out.WriteString(rest[:i])
		rest = rest[i+len("<think>"):]
		j := strings.Index(rest, "</think>")
		if j < 0 {
			th.WriteString(rest) // unterminated → treat remainder as thinking
			break
		}
		th.WriteString(rest[:j])
		rest = rest[j+len("</think>"):]
	}
	return strings.TrimSpace(out.String()), strings.TrimSpace(th.String())
}

// EmbedRequest is the body for POST /v1/embeddings.
type EmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// EmbedResponse is the response from POST /v1/embeddings.
type EmbedResponse struct {
	Data []EmbedData `json:"data"`

	// Usage is what the call cost. An embeddings endpoint reports only the
	// input side — there is no completion — so CompletionTokens is always zero
	// and TotalTokens equals PromptTokens.
	Usage Usage `json:"usage"`
}

// EmbedData is a single embedding result.
type EmbedData struct {
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

// Embed sends a batch of texts to the embeddings endpoint and returns vectors.
// Always uses the OpenAI-compatible path (Anthropic has no embedding API).
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	reqBody := EmbedRequest{
		Model: c.model,
		Input: texts,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.embedURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setAuthHeaders(httpReq)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(data), 300))
	}

	var embedResp EmbedResponse
	if err := json.Unmarshal(data, &embedResp); err != nil {
		return nil, fmt.Errorf("parse embed response: %w", err)
	}

	// Counted here for the same reason Complete counts at its own return: this
	// is the one place every embedding call passes through, and the ctx carries
	// the tags set upstream. Without it the spend is invisible to
	// tokens.Snapshot, which is what a dashboard reads — so an operator sees
	// every chat call and none of these, while a run that ranks tools against
	// a request embeds that request and every tool description.
	//
	// Not emitted to the CallObserver. That takes a chat request and a chat
	// response, and an embedding is neither — see observer.go.
	tokens.AddSplit(ctx, embedResp.Usage.PromptTokens, embedResp.Usage.CompletionTokens)

	// Sort by index and extract vectors
	vectors := make([][]float64, len(texts))
	for _, d := range embedResp.Data {
		if d.Index >= 0 && d.Index < len(vectors) {
			vectors[d.Index] = d.Embedding
		}
	}

	return vectors, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
