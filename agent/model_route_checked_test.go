package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The checked lane helpers, which is where a stage that parses its reply gets
// told the reply was cut off.
//
// A whole-run test cannot show this: a scripted endpoint returns valid JSON
// whatever finish_reason it reports, and real truncation means invalid JSON.
// What can be shown is that the flag every provider sets is noticed here and
// ignored by the plain helpers, which is the difference the eleven call sites
// rest on.

func agentOnCutModel(t *testing.T) *Agent {
	t.Helper()
	model := newStubModel(t, map[string]stubReply{
		"": {Content: "half an ans", Cut: true},
	})
	a := agentOnStub(t, model)
	return a
}

func TestACheckedLaneCallReportsATruncatedReply(t *testing.T) {
	a := agentOnCutModel(t)
	req := &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hello"}}, MaxTokens: 64}

	_, err := a.completeLightChecked(context.Background(), req)
	if !errors.Is(err, llm.ErrReplyTruncated) {
		t.Errorf("completeLightChecked = %v; want the truncation named", err)
	}

	_, err = a.completeHeavyChecked(context.Background(),
		&llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hello"}}, MaxTokens: 64})
	if !errors.Is(err, llm.ErrReplyTruncated) {
		t.Errorf("completeHeavyChecked = %v; want the truncation named", err)
	}
}

// And the plain helpers do not, because the stages that write prose for a
// person are on the other side of them: a cut answer there is short, not
// unusable, and failing the run over it would lose an answer that was fine.
func TestAPlainLaneCallKeepsATruncatedReply(t *testing.T) {
	a := agentOnCutModel(t)
	req := &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hello"}}, MaxTokens: 64}

	resp, err := a.completeLight(context.Background(), req)
	if err != nil {
		t.Fatalf("completeLight = %v; a cut prose reply is not an error", err)
	}
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		t.Error("the partial answer was dropped; short beats nothing")
	}
	if !llm.Truncated(resp) {
		t.Error("the response no longer reports that it was cut, so a caller that " +
			"wants to know cannot ask")
	}
}
