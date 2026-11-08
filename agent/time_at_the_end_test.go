package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

/*
 * The time goes at the end of a request, not in its system prompt.
 *
 * A provider reuses its work on a request only as far as the first character
 * that differs from the last one it saw. The clock is the one line in a system
 * prompt that differs on every call, and environmentSection put it there, so
 * everything after it was read and charged again on every call: a reflector
 * shared 23% of its request with the one before it, a planner 30%.
 */

// doorAgent is an agent whose lanes resolve, enough to send a request through
// prepare.
func doorAgent() *Agent {
	return &Agent{llm: llm.NewClient("", "", "m"), executor: llm.NewClient("", "", "m")}
}

// A system prompt carrying the environment section is the same bytes on every
// call, and the time is on the last line of the user's message.
func TestTheClockIsAtTheEndOfTheRequest(t *testing.T) {
	a := doorAgent()
	req := &llm.ChatRequest{Messages: []llm.Message{
		{Role: "system", Content: "the rules" + a.environmentSection()},
		{Role: "user", Content: "the question"},
	}}
	a.prepare(context.Background(), Heavy, req)

	sys, user := req.Messages[0].Content, req.Messages[1].Content
	if strings.Contains(sys, "Current time: 2") {
		t.Errorf("the clock is in the system prompt, so nothing after it can be reused:\n%s", sys)
	}
	if !strings.Contains(sys, "parameter") {
		t.Errorf("the rule that the date governs parameters left the system prompt:\n%s", sys)
	}
	if !strings.HasPrefix(user, "the question") {
		t.Errorf("the user's message was changed rather than added to: %q", user)
	}
	if !strings.HasSuffix(strings.TrimSpace(user), "UTC.") || !strings.Contains(user, "Current time: "+time.Now().UTC().Format("2006-01-02")) {
		t.Errorf("the time is not the last line of the request: %q", user)
	}
}

// Two system prompts built a second apart are identical.
func TestTheSystemPromptDoesNotChangeWithTheClock(t *testing.T) {
	a := doorAgent()
	build := func() string {
		req := &llm.ChatRequest{Messages: []llm.Message{
			{Role: "system", Content: "the rules" + a.environmentSection()},
			{Role: "user", Content: "q"},
		}}
		a.prepare(context.Background(), Heavy, req)
		return req.Messages[0].Content
	}
	first := build()
	time.Sleep(1100 * time.Millisecond)
	if second := build(); second != first {
		t.Errorf("the system prompt changed between calls:\n%q\n%q", first, second)
	}
}

// When the request does not end with the user's message — the ReAct loop in
// the middle of a turn ends with a tool result — the time still reaches the
// model, at the end of the system prompt as before.
func TestWithoutAClosingUserMessageTheClockStaysInTheSystemPrompt(t *testing.T) {
	a := doorAgent()
	req := &llm.ChatRequest{Messages: []llm.Message{
		{Role: "system", Content: "the rules" + a.environmentSection()},
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.FunctionCall{Name: "bash"}}}},
		{Role: "tool", ToolCallID: "c1", Content: "the result"},
	}}
	a.prepare(context.Background(), Heavy, req)
	if !strings.Contains(req.Messages[0].Content, "Current time: 2") {
		t.Errorf("the time reached nothing: %+v", req.Messages)
	}
	if req.Messages[3].Content != "the result" {
		t.Errorf("a tool result was changed: %q", req.Messages[3].Content)
	}
}

// A stage that was never told the time is not told it now.
func TestARequestWithoutTheSectionGetsNoClock(t *testing.T) {
	a := doorAgent()
	req := &llm.ChatRequest{Messages: []llm.Message{
		{Role: "system", Content: "the rules"},
		{Role: "user", Content: "q"},
	}}
	a.prepare(context.Background(), Heavy, req)
	if req.Messages[0].Content != "the rules" || req.Messages[1].Content != "q" {
		t.Errorf("a request that asked for no clock got one: %+v", req.Messages)
	}
}

// The planner reads the time from environmentSection, so its gate request
// asks for none. With an empty worklog the gate's fallback put its clock at
// the head of the tool index, the largest block the planner sends.
func TestThePlannersToolIndexCarriesNoClock(t *testing.T) {
	a := &Agent{}
	reg := toolapi.NewRegistry()
	reg.Register(&countingTool{name: "bash"})
	a.registry = reg
	g := NewGraph()
	g.Context = NewContextGate(g, &Trigger{}, a)

	resp, err := g.Context.Get(context.Background(), plannerContextRequest(a, []string{"bash"}))
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if idx := resp.Sources[SourceToolIndex]; idx == "" || strings.Contains(idx, "Current time:") {
		t.Errorf("the tool index is empty or led by the clock:\n%.120s", idx)
	}
}

// A request sent through the door twice — a retry — states the time once.
func TestARetriedRequestStatesTheTimeOnce(t *testing.T) {
	a := doorAgent()
	req := &llm.ChatRequest{Messages: []llm.Message{
		{Role: "system", Content: "the rules" + a.environmentSection()},
		{Role: "user", Content: "q"},
	}}
	a.prepare(context.Background(), Heavy, req)
	a.prepare(context.Background(), Heavy, req)
	if n := strings.Count(req.Messages[1].Content, "Current time:"); n != 1 {
		t.Errorf("the time is stated %d times: %q", n, req.Messages[1].Content)
	}
}

// Every call of a run carries the run's id as its cache key.
func TestACallCarriesItsRunAsTheCacheKey(t *testing.T) {
	a := doorAgent()
	req := &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "q"}}}
	a.prepare(withRunID(context.Background(), "run-7"), Heavy, req)
	if req.CacheKey != "run-7" {
		t.Errorf("cache key = %q, want the run's id", req.CacheKey)
	}
}
