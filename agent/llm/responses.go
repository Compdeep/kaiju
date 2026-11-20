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
)

// OpenAI's own API is called on /v1/responses, not /v1/chat/completions. Its
// newer models refuse function tools together with reasoning_effort on chat
// completions ("To use function tools, use /v1/responses or set
// reasoning_effort to 'none'"), and gpt-6.2-sol refuses "none", so on chat
// completions the planner, which always sends a tool, could not run on it at
// all. Every other OpenAI-compatible endpoint stays on chat completions.
//
// The caller's ChatRequest and the ChatResponse it gets back are unchanged:
// the request is translated on the way out and the reply on the way in.

// responsesURL is the Responses endpoint, for a base address with or without
// /v1.
func (c *Client) responsesURL() string {
	ep := strings.TrimRight(c.endpoint, "/")
	if strings.HasSuffix(ep, "/v1") {
		return ep + "/responses"
	}
	return ep + "/v1/responses"
}

// respRequest is the body for POST /v1/responses, narrowed to what a
// ChatRequest can carry.
type respRequest struct {
	Model       string     `json:"model"`
	Input       []any      `json:"input"`
	Tools       []respTool `json:"tools,omitempty"`
	ToolChoice  any        `json:"tool_choice,omitempty"`
	Temperature *float64   `json:"temperature,omitempty"`
	TopP        float64    `json:"top_p,omitempty"`
	// MaxOutputTokens is ChatRequest.MaxTokens under this endpoint's name.
	MaxOutputTokens int            `json:"max_output_tokens,omitempty"`
	Reasoning       *respReasoning `json:"reasoning,omitempty"`
	Text            *respText      `json:"text,omitempty"`
	PromptCacheKey  string         `json:"prompt_cache_key,omitempty"`
	Stream          bool           `json:"stream,omitempty"`
	// Store is false: a reply is not kept on OpenAI's side. Every call sends
	// its whole conversation, as on chat completions.
	Store bool `json:"store"`
}

// respTool is a function tool. Not nested under "function" as on chat
// completions.
type respTool struct {
	Type        string          `json:"type"` // "function"
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	// Strict is sent false. This endpoint defaults it to true, and refuses a
	// schema strict mode cannot carry; chat completions defaults it to false.
	Strict bool `json:"strict"`
}

type respReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type respText struct {
	Format any `json:"format"`
}

// respMessage is one message of the conversation as an input item.
type respMessage struct {
	Type    string `json:"type"` // "message"
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type respFunctionCall struct {
	Type      string `json:"type"` // "function_call"
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type respFunctionOutput struct {
	Type   string `json:"type"` // "function_call_output"
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

/*
 * responsesBody is req as a Responses request.
 * desc: A copy is built, so a retry of req still reads what it asked for.
 *
 *       Temperature and top_p go only to a model the catalog says does not
 *       think. OpenAI's thinking models refuse both on this endpoint
 *       ("Unsupported parameter: 'temperature' is not supported with this
 *       model"), and a model the catalog does not know is more likely one of
 *       them than not.
 * param: req - the request as the caller built it.
 * param: stream - whether the reply is streamed.
 * return: the body as JSON.
 */
func (c *Client) responsesBody(req *ChatRequest, stream bool) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = c.model
	}
	out := respRequest{
		Model:           model,
		Input:           responsesInput(req.Messages),
		ToolChoice:      responsesToolChoice(req.ToolChoice),
		MaxOutputTokens: req.MaxTokens,
		PromptCacheKey:  req.CacheKey,
		Stream:          stream,
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, respTool{Type: "function", Name: t.Function.Name,
			Description: t.Function.Description, Parameters: t.Function.Parameters})
	}
	if req.ReasoningEffort != "" {
		out.Reasoning = &respReasoning{Effort: req.ReasoningEffort}
	}
	if f, ok := c.facts(model); (ok && !f.Thinking.Default) || req.ReasoningEffort == "none" {
		temp := req.Temperature
		out.Temperature, out.TopP = &temp, req.TopP
	}
	if rf := req.ResponseFormat; rf != nil {
		if rf.Type == "json_schema" && rf.JSONSchema != nil {
			out.Text = &respText{Format: map[string]any{"type": "json_schema", "name": rf.JSONSchema.Name,
				"schema": rf.JSONSchema.Schema, "strict": rf.JSONSchema.Strict}}
		} else {
			out.Text = &respText{Format: map[string]any{"type": rf.Type}}
		}
	}
	return json.Marshal(&out)
}

/*
 * responsesInput is a conversation as Responses input items.
 * desc: A message stays a message under its own role, system included. An
 *       assistant turn's tool calls become function_call items after its text,
 *       and a tool reply becomes the function_call_output for its call.
 */
func responsesInput(msgs []Message) []any {
	var items []any
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			items = append(items, respFunctionOutput{Type: "function_call_output", CallID: m.ToolCallID, Output: m.Content})
		case "assistant":
			if m.Content != "" {
				items = append(items, respMessage{Type: "message", Role: "assistant", Content: m.Content})
			}
			for _, tc := range m.ToolCalls {
				items = append(items, respFunctionCall{Type: "function_call", CallID: tc.ID,
					Name: tc.Function.Name, Arguments: tc.Function.Arguments})
			}
		default:
			items = append(items, respMessage{Type: "message", Role: m.Role, Content: responsesContent(m)})
		}
	}
	return items
}

// responsesContent is a message's text, or its parts in this endpoint's names.
func responsesContent(m Message) any {
	if len(m.Parts) == 0 {
		return m.Content
	}
	parts := make([]map[string]any, 0, len(m.Parts))
	for _, p := range m.Parts {
		switch {
		case p.Type == "image_url" && p.ImageURL != nil:
			parts = append(parts, map[string]any{"type": "input_image", "image_url": p.ImageURL.URL})
		case p.Type == "text":
			parts = append(parts, map[string]any{"type": "input_text", "text": p.Text})
		}
	}
	return parts
}

// responsesToolChoice is tool_choice in this endpoint's shape. "auto",
// "required" and "none" are the same; a named function is not nested.
func responsesToolChoice(tc any) any {
	if name := forcedToolName(tc); name != "" {
		return map[string]any{"type": "function", "name": name}
	}
	return tc
}

// respReply is the reply from POST /v1/responses, narrowed to what a
// ChatResponse carries.
type respReply struct {
	ID                string         `json:"id"`
	Status            string         `json:"status"`
	Error             *ProviderError `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Output []respItem `json:"output"`
	Usage  respUsage  `json:"usage"`
}

type respItem struct {
	Type    string `json:"type"` // "message", "function_call", "reasoning"
	Content []struct {
		Type    string `json:"type"` // "output_text", "refusal"
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Summary   []struct {
		Text string `json:"text"`
	} `json:"summary"`
}

type respUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// usage is the reply's token counts as chat completions reports them, so the
// split reaches llm_debug the same way.
func (u respUsage) usage() Usage {
	out := Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, TotalTokens: u.TotalTokens}
	if u.OutputTokensDetails.ReasoningTokens > 0 {
		out.Details = &UsageDetails{ReasoningTokens: u.OutputTokensDetails.ReasoningTokens}
	}
	if u.InputTokensDetails.CachedTokens > 0 {
		out.Prompt = &PromptDetails{CachedTokens: u.InputTokensDetails.CachedTokens}
	}
	return out
}

// finish is the chat-completions finish_reason for a reply: "length" when it
// stopped at the reply cap, "tool_calls" when it called a tool, else "stop".
func (r *respReply) finish(calls bool) string {
	if r.Status == "incomplete" && r.IncompleteDetails != nil && r.IncompleteDetails.Reason == "max_output_tokens" {
		return "length"
	}
	if calls {
		return "tool_calls"
	}
	return "stop"
}

// chat is the reply as a ChatResponse. A refusal is kept as the text, so a
// caller reads what the model said rather than an empty answer.
func (r *respReply) chat() *ChatResponse {
	var text, thought strings.Builder
	var calls []ToolCall
	for _, it := range r.Output {
		switch it.Type {
		case "message":
			for _, p := range it.Content {
				text.WriteString(p.Text)
				text.WriteString(p.Refusal)
			}
		case "function_call":
			calls = append(calls, ToolCall{ID: it.CallID, Type: "function",
				Function: FunctionCall{Name: it.Name, Arguments: it.Arguments}})
		case "reasoning":
			for _, s := range it.Summary {
				thought.WriteString(s.Text)
			}
		}
	}
	m := Message{Role: "assistant", Content: text.String(), Reasoning: thought.String(), ToolCalls: calls}
	liftThinkingInto(&m)
	return &ChatResponse{ID: r.ID, Choices: []Choice{{Message: m, FinishReason: r.finish(len(calls) > 0)}},
		Usage: r.Usage.usage()}
}

// completeResponses is completeOpenAI's wire for OpenAI's own API. ctx
// already carries the call's deadline.
func (c *Client) completeResponses(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	body, err := c.responsesBody(req, false)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.responsesURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setAuthHeaders(httpReq)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, transportFailure(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &CallError{Kind: KindTransport, Err: fmt.Errorf("read response: %w", err)}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpFailure(resp.StatusCode, resp.Header, truncate(string(data), 300))
	}
	var r respReply
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	out := r.chat()
	if r.Error != nil || r.Status == "failed" {
		msg := r.Error.String()
		return out, upstreamFailureAt(msg, fmt.Errorf("provider returned an error with HTTP 200: %s", msg))
	}
	if len(r.Output) == 0 {
		return out, upstreamFailureAt(truncate(string(data), 400),
			fmt.Errorf("provider returned no output with HTTP 200: %s", truncate(string(data), 400)))
	}
	return out, nil
}

// streamResponses is completeStreamResp's wire for OpenAI's own API. Each
// event's data carries its own type, so the event lines are not read.
func (c *Client) streamResponses(ctx context.Context, req *ChatRequest, onChunk func(chunk, kind string)) (*ChatResponse, error) {
	body, err := c.responsesBody(req, true)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.responsesURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setAuthHeaders(httpReq)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, transportFailure(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return nil, httpFailure(resp.StatusCode, resp.Header, truncate(string(data), 300))
	}

	out := newStreamed(onChunk)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev struct {
			Type        string     `json:"type"`
			Delta       string     `json:"delta"`
			OutputIndex int        `json:"output_index"`
			Item        respItem   `json:"item"`
			Response    *respReply `json:"response"`
			Message     string     `json:"message"`
		}
		if json.Unmarshal([]byte(line[6:]), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.output_text.delta", "response.refusal.delta":
			out.text(ev.Delta)
		case "response.reasoning_summary_text.delta":
			out.thought(ev.Delta)
		case "response.output_item.added", "response.output_item.done":
			if ev.Item.Type == "function_call" {
				acc := out.tool(ev.OutputIndex)
				acc.ID, acc.Function.Name = ev.Item.CallID, ev.Item.Name
				if ev.Item.Arguments != "" {
					acc.Function.Arguments = ev.Item.Arguments // the whole, on done
				}
				out.finish = "tool_calls"
			}
		case "response.function_call_arguments.delta":
			out.tool(ev.OutputIndex).Function.Arguments += ev.Delta
		case "response.completed", "response.incomplete", "response.failed":
			if r := ev.Response; r != nil {
				out.id, out.usage = r.ID, r.Usage.usage()
				if f := r.finish(len(out.order) > 0); f == "length" {
					out.finish = f
				}
				if ev.Type == "response.failed" {
					msg := r.Error.String()
					return out.reply(), upstreamFailureAt(msg, fmt.Errorf("provider failed the response: %s", msg))
				}
			}
		case "error":
			return out.reply(), upstreamFailureAt(ev.Message, fmt.Errorf("provider sent an error mid-stream: %s", ev.Message))
		}
	}
	if err := scanner.Err(); err != nil {
		return out.reply(), err
	}
	out.bill(ctx)
	return out.reply(), nil
}
