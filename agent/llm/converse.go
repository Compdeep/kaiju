package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// Bedrock's Converse API: Amazon's own request shape, for the models Bedrock
// serves only there (Llama, Nova). A Bedrock client sends a model here when the
// catalog says Converse; every other model stays on Chat Completions. Same key,
// same region; the host is bedrock-runtime.

type converseRequest struct {
	Messages        []converseMessage `json:"messages"`
	System          []converseBlock   `json:"system,omitempty"`
	InferenceConfig converseInference `json:"inferenceConfig"`
	ToolConfig      *converseTools    `json:"toolConfig,omitempty"`
	Additional      map[string]any    `json:"additionalModelRequestFields,omitempty"`
}

type converseMessage struct {
	Role    string          `json:"role"`
	Content []converseBlock `json:"content"`
}

// converseBlock is one content block, in a request or a reply. Exactly one
// field is set.
type converseBlock struct {
	Text       string              `json:"text,omitempty"`
	Image      *converseImage      `json:"image,omitempty"`
	ToolUse    *converseToolUse    `json:"toolUse,omitempty"`
	ToolResult *converseToolResult `json:"toolResult,omitempty"`
	Reasoning  *converseReasoning  `json:"reasoningContent,omitempty"`
}

type converseImage struct {
	Format string `json:"format"`
	Source struct {
		Bytes string `json:"bytes"` // base64
	} `json:"source"`
}

type converseToolUse struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type converseToolResult struct {
	ToolUseID string          `json:"toolUseId"`
	Content   []converseBlock `json:"content"`
}

// converseReasoning is thinking text: under reasoningText in a reply, flat in
// a streamed delta.
type converseReasoning struct {
	Text          string `json:"text"`
	ReasoningText *struct {
		Text string `json:"text"`
	} `json:"reasoningText"`
}

func (r *converseReasoning) text() string {
	if r == nil {
		return ""
	}
	if r.ReasoningText != nil {
		return r.ReasoningText.Text
	}
	return r.Text
}

type converseInference struct {
	MaxTokens   int     `json:"maxTokens,omitempty"`
	Temperature float64 `json:"temperature"`
	TopP        float64 `json:"topP,omitempty"`
}

type converseTools struct {
	Tools  []converseTool `json:"tools"`
	Choice map[string]any `json:"toolChoice,omitempty"`
}

type converseTool struct {
	Spec struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema struct {
			JSON json.RawMessage `json:"json"`
		} `json:"inputSchema"`
	} `json:"toolSpec"`
}

type converseUsage struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	CacheReadTokens  int `json:"cacheReadInputTokens"`
	CacheWriteTokens int `json:"cacheWriteInputTokens"`
}

// usage counts cache reads and writes beside inputTokens, as Anthropic does.
func (u converseUsage) usage() Usage {
	return anthropicUsage(u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
}

type converseResponse struct {
	Output struct {
		Message converseMessage `json:"message"`
	} `json:"output"`
	StopReason string        `json:"stopReason"`
	Usage      converseUsage `json:"usage"`
}

// converses reports whether this client sends model over Converse.
func (c *Client) converses(model string) bool {
	if c.provider != ProviderBedrock {
		return false
	}
	f, ok := c.facts(model)
	return ok && f.Converse
}

/*
 * converseURL is where a Converse call for model goes.
 * desc: A bedrock-mantle endpoint names the region, and Converse is on
 *       bedrock-runtime in that region. Any other endpoint — bedrock-runtime
 *       itself, a proxy — is used as given, without its path.
 */
func (c *Client) converseURL(model string, stream bool) string {
	base := "https://bedrock-runtime." + DefaultBedrockRegion + ".amazonaws.com"
	if u, err := url.Parse(c.endpoint); err == nil && u.Host != "" {
		base = u.Scheme + "://" + u.Host
		if region, ok := strings.CutPrefix(u.Hostname(), "bedrock-mantle."); ok {
			base = "https://bedrock-runtime." + strings.TrimSuffix(region, ".api.aws") + ".amazonaws.com"
		}
	}
	path := base + "/model/" + url.PathEscape(model) + "/converse"
	if stream {
		path += "-stream"
	}
	return path
}

// postConverse sends one Converse request and returns the open reply, or the
// error kind for a refusal.
func (c *Client) postConverse(ctx context.Context, req *ChatRequest, stream bool) (*http.Response, error) {
	body, err := json.Marshal(buildConverseRequest(req))
	if err != nil {
		return nil, fmt.Errorf("marshal converse request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.converseURL(req.Model, stream), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setAuthHeaders(httpReq)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, transportFailure(err)
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, httpFailure(resp.StatusCode, resp.Header, truncate(string(data), 300))
	}
	return resp, nil
}

// completeConverse is one plain Converse call.
func (c *Client) completeConverse(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeoutFor(req))
	defer cancel()
	resp, err := c.postConverse(ctx, req, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var cr converseResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, &CallError{Kind: KindTransport, Err: fmt.Errorf("parse converse response: %w", err)}
	}
	msg := Message{Role: "assistant"}
	for _, b := range cr.Output.Message.Content {
		msg.Content += b.Text
		msg.Reasoning += b.Reasoning.text()
		if b.ToolUse != nil {
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: b.ToolUse.ToolUseID, Type: "function",
				Function: FunctionCall{Name: b.ToolUse.Name, Arguments: string(b.ToolUse.Input)}})
		}
	}
	liftThinkingInto(&msg)
	return &ChatResponse{
		Choices: []Choice{{Message: msg, FinishReason: converseFinish(cr.StopReason)}},
		Usage:   cr.Usage.usage(),
	}, nil
}

// streamConverse is one streamed Converse call, read as AWS event-stream
// messages and shown as they arrive.
func (c *Client) streamConverse(ctx context.Context, req *ChatRequest, onChunk func(chunk, kind string)) (*ChatResponse, error) {
	resp, err := c.postConverse(ctx, req, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	out := newStreamed(onChunk)
	r := bufio.NewReader(resp.Body)
	for {
		ev, err := readEvent(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			return out.reply(), &CallError{Kind: KindTransport, Err: err}
		}
		if t := ev.headers[":message-type"]; t != "" && t != "event" {
			return out.reply(), converseException(ev)
		}
		var p struct {
			Index int `json:"contentBlockIndex"`
			Start struct {
				ToolUse *converseToolUse `json:"toolUse"`
			} `json:"start"`
			Delta struct {
				Text    string `json:"text"`
				ToolUse *struct {
					Input string `json:"input"`
				} `json:"toolUse"`
				Reasoning *converseReasoning `json:"reasoningContent"`
			} `json:"delta"`
			StopReason string         `json:"stopReason"`
			Usage      *converseUsage `json:"usage"`
		}
		if json.Unmarshal(ev.payload, &p) != nil {
			continue
		}
		switch ev.headers[":event-type"] {
		case "contentBlockStart":
			if tu := p.Start.ToolUse; tu != nil {
				acc := out.tool(p.Index)
				acc.ID, acc.Function.Name = tu.ToolUseID, tu.Name
			}
		case "contentBlockDelta":
			out.thought(p.Delta.Reasoning.text())
			out.text(p.Delta.Text)
			if tu := p.Delta.ToolUse; tu != nil {
				out.tool(p.Index).Function.Arguments += tu.Input
			}
		case "messageStop":
			out.finish = converseFinish(p.StopReason)
		case "metadata":
			if p.Usage != nil {
				out.usage = p.Usage.usage()
			}
		}
	}
	out.bill(ctx)
	return out.reply(), nil
}

// converseException is an error Bedrock sent inside the stream, as the error
// kind the same refusal would have had as an HTTP status.
func converseException(ev streamEvent) error {
	kind := ev.headers[":exception-type"]
	status := http.StatusInternalServerError
	switch kind {
	case "throttlingException":
		status = http.StatusTooManyRequests
	case "validationException":
		status = http.StatusBadRequest
	case "serviceUnavailableException":
		status = http.StatusServiceUnavailable
	}
	return httpFailure(status, nil, truncate(kind+": "+string(ev.payload), 300))
}

// converseFinish is Bedrock's stop reason as the OpenAI finish reason.
func converseFinish(stop string) string {
	switch stop {
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "guardrail_intervened", "content_filtered":
		return "content_filter"
	}
	return "stop"
}

// converseOpening is the user turn put first when a conversation would start
// with the assistant. See startWithUser.
const converseOpening = "The steps run so far, and what each returned:"

/*
 * startWithUser puts a user turn first when the conversation starts with the
 * assistant.
 * desc: Converse refuses that conversation: "A conversation must start with a
 *       user message." (HTTP 400, found live on Nova 2 Lite, 2026-11-12).
 *       Chat Completions accepts it, which is why the rest of kaiju builds it:
 *       BuildMessagesWithResults puts the steps already run before the
 *       objective, written as the assistant's tool calls, so every stage after
 *       the first step starts with the assistant. The fix stays here, on the
 *       Converse wire only, so no other provider's request changes.
 * param: msgs - the conversation in Converse's shape.
 * return: msgs, with converseOpening first where it was needed.
 */
func startWithUser(msgs []converseMessage) []converseMessage {
	if len(msgs) == 0 || msgs[0].Role == "user" {
		return msgs
	}
	opening := converseMessage{Role: "user", Content: []converseBlock{{Text: converseOpening}}}
	return append([]converseMessage{opening}, msgs...)
}

/*
 * buildConverseRequest is req in Converse's shape.
 * desc: System messages go apart. Tool results are user turns, and turns from
 *       the same side are merged, because Converse refuses two in a row.
 *       Thinking is sent as Nova's reasoning config, from the effort the
 *       client already settled on — see bedrockEffort.
 */
func buildConverseRequest(req *ChatRequest) *converseRequest {
	out := &converseRequest{InferenceConfig: converseInference{
		MaxTokens: req.MaxTokens, Temperature: req.Temperature, TopP: req.TopP,
	}}
	for _, m := range req.Messages {
		if m.Role == "system" {
			out.System = append(out.System, converseBlock{Text: m.Content})
			continue
		}
		role, blocks := converseTurn(m)
		if len(blocks) == 0 {
			continue
		}
		if n := len(out.Messages); n > 0 && out.Messages[n-1].Role == role {
			out.Messages[n-1].Content = append(out.Messages[n-1].Content, blocks...)
			continue
		}
		out.Messages = append(out.Messages, converseMessage{Role: role, Content: blocks})
	}

	out.Messages = startWithUser(out.Messages)

	if len(req.Tools) > 0 && req.ToolChoice != "none" {
		tc := &converseTools{}
		for _, t := range req.Tools {
			var ct converseTool
			ct.Spec.Name, ct.Spec.Description = t.Function.Name, t.Function.Description
			ct.Spec.InputSchema.JSON = t.Function.Parameters
			tc.Tools = append(tc.Tools, ct)
		}
		if name := forcedToolName(req.ToolChoice); name != "" {
			tc.Choice = map[string]any{"tool": map[string]string{"name": name}}
		} else if req.ToolChoice == "required" {
			tc.Choice = map[string]any{"any": struct{}{}}
		}
		out.ToolConfig = tc
	}

	if e := req.ReasoningEffort; e != "" && e != "none" {
		out.Additional = map[string]any{"reasoningConfig": map[string]string{"type": "enabled", "maxReasoningEffort": e}}
	}
	return out
}

// converseTurn is one message as Converse's role and content blocks.
func converseTurn(m Message) (string, []converseBlock) {
	var blocks []converseBlock
	text := func(t string) {
		if t != "" {
			blocks = append(blocks, converseBlock{Text: t})
		}
	}
	switch {
	case m.Role == "tool":
		content := m.Content
		if content == "" {
			content = "(no output)" // Converse refuses an empty text block
		}
		return "user", []converseBlock{{ToolResult: &converseToolResult{
			ToolUseID: m.ToolCallID, Content: []converseBlock{{Text: content}}}}}
	case len(m.Parts) > 0:
		for _, p := range m.Parts {
			if p.Type == "text" {
				text(p.Text)
			} else if img := converseImageOf(p.ImageURL); img != nil {
				blocks = append(blocks, converseBlock{Image: img})
			}
		}
	default:
		text(m.Content)
	}
	for _, tc := range m.ToolCalls {
		input := json.RawMessage(tc.Function.Arguments)
		if !json.Valid(input) {
			input = json.RawMessage("{}")
		}
		blocks = append(blocks, converseBlock{ToolUse: &converseToolUse{ToolUseID: tc.ID, Name: tc.Function.Name, Input: input}})
	}
	return m.Role, blocks
}

// converseImageOf is a base64 data: URI as a Converse image. Converse takes
// image bytes only, so a link is dropped and said.
func converseImageOf(u *ImageURL) *converseImage {
	if u == nil {
		return nil
	}
	meta, data, ok := strings.Cut(strings.TrimPrefix(u.URL, "data:"), ";base64,")
	format, isImage := strings.CutPrefix(meta, "image/")
	if !ok || !isImage || !strings.HasPrefix(u.URL, "data:") {
		log.Printf("[llm] converse: image %q is not a base64 data URI and is not sent", truncate(u.URL, 60))
		return nil
	}
	if format == "jpg" {
		format = "jpeg"
	}
	img := &converseImage{Format: format}
	img.Source.Bytes = data
	return img
}
