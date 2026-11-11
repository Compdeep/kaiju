package llm

import (
	"context"
	"strings"

	"github.com/Compdeep/kaiju/tokens"
)

// streamed assembles a streamed reply, whichever wire it arrives on: text and
// thinking are shown as they come, tool calls are put together from their
// pieces, and the whole is the same ChatResponse a plain call returns.
type streamed struct {
	onChunk      func(chunk, kind string)
	content      strings.Builder
	reasoning    strings.Builder
	tools        map[int]*ToolCall
	order        []int // tool-call indices in first-seen order
	usage        Usage
	finish       string
	id, provider string
}

func newStreamed(onChunk func(chunk, kind string)) *streamed {
	return &streamed{onChunk: onChunk, tools: map[int]*ToolCall{}, finish: "stop"}
}

// text adds answer text and shows it.
func (s *streamed) text(t string) { s.add(&s.content, t, "content") }

// thought adds thinking text and shows it.
func (s *streamed) thought(t string) { s.add(&s.reasoning, t, "reasoning") }

func (s *streamed) add(b *strings.Builder, t, kind string) {
	if t == "" {
		return
	}
	b.WriteString(t)
	if s.onChunk != nil {
		s.onChunk(t, kind)
	}
}

// tool is the call being assembled at index i, started on first sight.
func (s *streamed) tool(i int) *ToolCall {
	acc, ok := s.tools[i]
	if !ok {
		acc = &ToolCall{Type: "function"}
		s.tools[i] = acc
		s.order = append(s.order, i)
	}
	return acc
}

/*
 * reply is what has arrived, as a ChatResponse.
 * desc: Called on a cut stream too: what arrived was paid for and may already
 *       have been shown, so it is returned with the error rather than dropped.
 *       Thinking a model wrote inline as <think>…</think> is lifted out of the
 *       answer either way.
 */
func (s *streamed) reply() *ChatResponse {
	var calls []ToolCall
	for _, i := range s.order {
		calls = append(calls, *s.tools[i])
	}
	m := Message{Role: "assistant", Content: s.content.String(), Reasoning: s.reasoning.String(), ToolCalls: calls}
	liftThinkingInto(&m)
	return &ChatResponse{
		ID:       s.id,
		Provider: s.provider,
		Choices:  []Choice{{Message: m, FinishReason: s.finish}},
		Usage:    s.usage,
	}
}

// bill counts a finished stream's tokens through the same counter as a plain
// call.
func (s *streamed) bill(ctx context.Context) {
	if s.usage.PromptTokens > 0 || s.usage.CompletionTokens > 0 {
		tokens.AddSplit(ctx, s.usage.PromptTokens, s.usage.CompletionTokens)
	}
}
