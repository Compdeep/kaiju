package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/internal/tokens"
)

// Message is a single chat message in the OpenAI format.
//
// Content is the plain-text body (the common case). For multimodal input, set
// Parts instead: it carries an OpenAI content-parts array (text + image_url) and,
// when non-empty, is what gets serialized as `content`. Parts is marshal-only
// (json:"-") and never persisted — the agent's session stores text, and images
// are re-supplied per request by the host (Makeen), never held in kaiju.
type Message struct {
	Role       string        `json:"role"`                   // "system", "user", "assistant", "tool"
	Content    string        `json:"content,omitempty"`
	Parts      []ContentPart `json:"-"`                      // multimodal parts; overrides Content when set
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
			Timeout: 120 * time.Second,
		},
	}
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
	// OpenRouter requires HTTP-Referer and X-Title for ranking/attribution
	if c.provider == ProviderOpenRouter {
		req.Header.Set("HTTP-Referer", "https://github.com/Compdeep/kaiju")
		req.Header.Set("X-Title", "Kaiju")
	}
}

// Complete sends a chat completion request and returns the response.
// Routes to the appropriate provider backend.
func (c *Client) Complete(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	var resp *ChatResponse
	var err error
	if c.provider == ProviderAnthropic {
		resp, err = c.completeAnthropic(ctx, req)
	} else {
		resp, err = c.completeOpenAI(ctx, req)
	}
	// Single token-accounting chokepoint: every non-streamed LLM call for both
	// providers passes through here, and the ctx carries the (category,
	// principal) tags set upstream. Streamed calls (CompleteStream) currently
	// carry no Usage and are undercounted — see the note there.
	if err == nil && resp != nil {
		tokens.AddSplit(ctx, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	}
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
func (c *Client) CompleteStream(ctx context.Context, req *ChatRequest, onChunk func(chunk string)) (string, error) {
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
func (c *Client) CompleteStreamResp(ctx context.Context, req *ChatRequest, onChunk func(chunk string)) (*ChatResponse, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	req.Stream = true
	req.StreamOptions = &StreamOptions{IncludeUsage: true}

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

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(data), 300))
	}

	var content strings.Builder
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
		if ch.Delta.Content != "" {
			content.WriteString(ch.Delta.Content)
			if onChunk != nil {
				onChunk(ch.Delta.Content)
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
	return &ChatResponse{
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: content.String(), ToolCalls: toolCalls},
			FinishReason: finish,
		}},
		Usage: usage,
	}, nil
}

// EmbedRequest is the body for POST /v1/embeddings.
type EmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// EmbedResponse is the response from POST /v1/embeddings.
type EmbedResponse struct {
	Data []EmbedData `json:"data"`
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
