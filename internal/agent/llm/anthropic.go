package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// --- Anthropic Messages API types ---

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	ToolChoice  any                `json:"tool_choice,omitempty"`
	Temperature float64            `json:"temperature"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []anthropicBlock
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicResponse struct {
	ID         string           `json:"id"`
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// completeAnthropic converts OpenAI-format request to Anthropic Messages API,
// sends the request, and converts the response back to OpenAI format.
func (c *Client) completeAnthropic(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	aReq := buildAnthropicRequest(c.model, req)

	body, err := json.Marshal(aReq)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.endpoint, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

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

	var aResp anthropicResponse
	if err := json.Unmarshal(data, &aResp); err != nil {
		return nil, fmt.Errorf("parse anthropic response: %w", err)
	}

	return convertAnthropicResponse(&aResp), nil
}

// buildAnthropicRequest converts an OpenAI ChatRequest to an Anthropic request.
func buildAnthropicRequest(model string, req *ChatRequest) *anthropicRequest {
	aReq := &anthropicRequest{
		Model:       model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}
	if aReq.MaxTokens == 0 {
		aReq.MaxTokens = 4096
	}
	if req.Model != "" {
		aReq.Model = req.Model
	}

	// Extract system messages
	var systemParts []string
	for _, m := range req.Messages {
		if m.Role == "system" {
			systemParts = append(systemParts, m.Content)
		}
	}
	if len(systemParts) > 0 {
		aReq.System = strings.Join(systemParts, "\n\n")
	}

	// Convert non-system messages
	for _, m := range req.Messages {
		if m.Role == "system" {
			continue
		}
		aReq.Messages = append(aReq.Messages, convertMessageToAnthropic(m))
	}

	// Convert tools
	for _, t := range req.Tools {
		aReq.Tools = append(aReq.Tools, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}

	// Convert tool_choice: OpenAI "required" → Anthropic {"type": "any"}
	if req.ToolChoice == "required" {
		aReq.ToolChoice = map[string]string{"type": "any"}
	} else if req.ToolChoice != nil {
		aReq.ToolChoice = req.ToolChoice
	}

	return aReq
}

// convertMessageToAnthropic converts a single OpenAI message to Anthropic format.
func convertMessageToAnthropic(m Message) anthropicMessage {
	switch {
	// Assistant message with tool calls → content blocks
	case m.Role == "assistant" && len(m.ToolCalls) > 0:
		var blocks []anthropicBlock
		if m.Content != "" {
			blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
		}
		for _, tc := range m.ToolCalls {
			blocks = append(blocks, anthropicBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: json.RawMessage(tc.Function.Arguments),
			})
		}
		return anthropicMessage{Role: "assistant", Content: blocks}

	// Tool result → user message with tool_result block
	case m.Role == "tool":
		blocks := []anthropicBlock{{
			Type:      "tool_result",
			ToolUseID: m.ToolCallID,
			Content:   m.Content,
		}}
		return anthropicMessage{Role: "user", Content: blocks}

	// Plain text message
	default:
		return anthropicMessage{Role: m.Role, Content: m.Content}
	}
}

// convertAnthropicResponse converts an Anthropic response to OpenAI ChatResponse format.
func convertAnthropicResponse(aResp *anthropicResponse) *ChatResponse {
	msg := Message{Role: "assistant"}
	var textParts []string

	for _, block := range aResp.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      block.Name,
					Arguments: string(block.Input),
				},
			})
		}
	}
	msg.Content = strings.Join(textParts, "")

	finishReason := "stop"
	if aResp.StopReason == "tool_use" {
		finishReason = "tool_calls"
	}

	return &ChatResponse{
		ID: aResp.ID,
		Choices: []Choice{{
			Index:        0,
			Message:      msg,
			FinishReason: finishReason,
		}},
		Usage: Usage{
			PromptTokens:     aResp.Usage.InputTokens,
			CompletionTokens: aResp.Usage.OutputTokens,
			TotalTokens:      aResp.Usage.InputTokens + aResp.Usage.OutputTokens,
		},
	}
}
