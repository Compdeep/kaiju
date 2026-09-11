package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
)

// A stage that forces a tool call, as the compute lane's two calls do.
func forcedCall(fn string) *llm.ChatRequest {
	return &llm.ChatRequest{
		Messages:   []llm.Message{{Role: "user", Content: "write it"}},
		Tools:      []llm.ToolDef{{Type: "function", Function: llm.FunctionDef{Name: fn}}},
		ToolChoice: "required",
		MaxTokens:  2048,
	}
}

// The reply budget went on reasoning, so the call is made again with thinking
// off — and the second attempt is shown the thinking the first paid for.
//
// This is what cost one live run four minutes: 239 seconds, 14,276 output
// tokens, and "llm response: empty content and no tool calls". The step failed
// and the run replanned into the same call.
func TestHeavyRound_RetriesAReplyThatSpentItsBudgetThinking(t *testing.T) {
	const thought = "the template has five fields and two of them repeat"

	model := newStubModel(t, nil)
	model.answerNth("write_code",
		stubReply{Cut: true, Reasoning: thought},                // nothing but thinking
		stubReply{Args: map[string]any{"filename": "pitch.py"}}, // the retry answers
	)
	a := agentOnStub(t, model)

	resp, err := a.heavyRound(context.Background(), nil, forcedCall("write_code"))
	if err != nil {
		t.Fatalf("the call failed rather than recovering: %v", err)
	}
	if len(resp.Choices) == 0 || len(resp.Choices[0].Message.ToolCalls) == 0 {
		t.Fatalf("no tool call came back, so the recovery did not produce one: %+v", resp)
	}

	asked := model.requestsTo("write_code")
	if len(asked) != 2 {
		t.Fatalf("%d calls were made, want the first and its retry", len(asked))
	}
	var carried bool
	for _, m := range asked[1].Messages {
		if strings.Contains(m.Content, thought) {
			carried = true
		}
	}
	if !carried {
		t.Error("the retry was not shown the thinking the first attempt paid for")
	}
}

// A reply cut off at the token cap with something in it is still reported.
//
// Different fault, opposite remedy: this one wrote too much rather than
// nothing, and a half-written program written to disk and run costs three
// debugging rounds on a script that was never whole.
func TestHeavyRound_StillReportsATruncatedReply(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{
		"write_code": {Cut: true, Args: map[string]any{"filename": "half.py"}},
	})
	a := agentOnStub(t, model)

	_, err := a.heavyRound(context.Background(), nil, forcedCall("write_code"))
	if err == nil {
		t.Fatal("a reply that ran into the cap was reported as a whole one")
	}
	if !errors.Is(err, llm.ErrReplyTruncated) {
		t.Errorf("error = %v, want the truncation to be named", err)
	}
}

// An ordinary reply passes through untouched, and once.
func TestHeavyRound_LeavesAGoodReplyAlone(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{
		"write_code": {Args: map[string]any{"filename": "whole.py"}},
	})
	a := agentOnStub(t, model)

	resp, err := a.heavyRound(context.Background(), nil, forcedCall("write_code"))
	if err != nil {
		t.Fatalf("a good reply came back as an error: %v", err)
	}
	if len(resp.Choices[0].Message.ToolCalls) == 0 {
		t.Error("the tool call was lost on the way through")
	}
	if n := model.callsTo("write_code"); n != 1 {
		t.Errorf("%d calls made, want 1 — nothing was wrong with the first", n)
	}
}

// The clock ends the call, and what it had thought by then goes to the retry.
//
// The compute lane had no clock at all: a call that never finished was bounded
// only by the client's request deadline, minutes away, and the run sat there.
func TestHeavyRound_RecoversFromItsOwnDeadline(t *testing.T) {
	const thought = "reading both files before writing anything"

	rig := hangingModel(t, thought)
	restore := effortBudget[EffortFast]
	effortBudget[EffortFast] = 300 * time.Millisecond
	defer func() { effortBudget[EffortFast] = restore }()

	a := agentOnStub(t, &stubModel{Server: rig.Server})
	ctx := withTrigger(context.Background(), &Trigger{ReasoningEffort: EffortFast})

	resp, err := a.heavyRound(ctx, nil, forcedCall("write_code"))
	if err != nil {
		t.Fatalf("the deadline was reported rather than recovered: %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("no reply came back at all")
	}
	asked := rig.asked(2)
	if asked == "" {
		t.Fatal("no second call was made, so the deadline ended the stage")
	}
	if !strings.Contains(asked, thought) {
		t.Errorf("the retry was not shown what the cut-off call had thought:\n%s", asked)
	}
}
