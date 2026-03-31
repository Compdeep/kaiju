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
)

// Message is a single chat message in the OpenAI format.
type Message struct {
	Role       string     `json:"role"`                  // "system", "user", "assistant", "tool"
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // set when role == "tool"
	Name       string     `json:"name,omitempty"`         // function name for tool results
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
		req.Header.Set("HTTP-Referer", "https://github.com/user/kaiju")
		req.Header.Set("X-Title", "Kaiju")
	}
}

// Complete sends a chat completion request and returns the response.
// Routes to the appropriate provider backend.
func (c *Client) Complete(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if c.provider == ProviderAnthropic {
		return c.completeAnthropic(ctx, req)
	}
	return c.completeOpenAI(ctx, req)
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

// CompleteStream sends a streaming chat completion request and calls onChunk
// for each text delta. Returns the full accumulated text. Only supports
// OpenAI-compatible endpoints (including OpenRouter).
func (c *Client) CompleteStream(ctx context.Context, req *ChatRequest, onChunk func(chunk string)) (string, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	req.Stream = true

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.chatURL(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	c.setAuthHeaders(httpReq)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(data), 300))
	}

	// Parse SSE stream: each line is "data: {...}" with delta.content
	var full strings.Builder
	scanner := bufio.NewScanner(resp.Body)
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
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			text := chunk.Choices[0].Delta.Content
			full.WriteString(text)
			if onChunk != nil {
				onChunk(text)
			}
		}
	}

	return full.String(), scanner.Err()
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
