package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// Every lane a person waits on must handle a reply that came back empty.
//
// This was a guard that read three filenames out of the source and checked each
// for the words "recoverDeadThought" and "roundBudget". It passed while
// chat_node.go — the lane that answers a conversational turn on any deployment
// not in chat mode, and so the lane a person is most often sitting in front of —
// shipped with neither, and a live turn spent its whole 4,096-token budget
// reasoning and told the reader the request had produced no answer.
//
// A hand-maintained list of lanes is the fault, not the fix. There is one seam
// now, so the property is tested where it lives: send is asked for a reply, the
// model produces nothing, and a second attempt is made. Every lane gets that by
// calling send, and a lane added tomorrow gets it without anybody editing a list.

// deadReplyRig answers the first call with nothing at all and the second with an
// answer, recording what each was asked.
func deadReplyRig(t *testing.T) *stubModel {
	t.Helper()
	m := newStubModel(t, map[string]stubReply{"": {Content: "the answer, at last"}})
	m.answerNth("", stubReply{Content: "", Cut: true})
	return m
}

func TestSend_ARepliesThatProducedNothingIsAskedAgain(t *testing.T) {
	for _, lane := range []Lane{Heavy, Light, Answer, Route} {
		t.Run(lane.String(), func(t *testing.T) {
			a := agentOnStub(t, deadReplyRig(t))
			resp, err := a.send(context.Background(), modelCall{
				Lane: lane, Stage: replyDecisionBudget,
				Req: &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hello"}}},
			})
			if err != nil {
				t.Fatalf("the empty reply was reported rather than re-asked: %v", err)
			}
			if got := streamedText(resp); !strings.Contains(got, "at last") {
				t.Errorf("content = %q, want the second attempt's answer", got)
			}
		})
	}
}

// The second attempt is given the thinking the first one paid for.
//
// It used to start from the same blank page: a cancelled call returns no reply,
// the reasoning was read off the reply, and two minutes of it were thrown away.
func TestSend_TheSecondAttemptCarriesTheFirstsReasoning(t *testing.T) {
	const thought = "they mean the March invoice, not the contract"
	m := newStubModel(t, map[string]stubReply{"": {Content: "the March invoice"}})
	m.answerNth("", stubReply{Content: "", Cut: true, Reasoning: thought})
	a := agentOnStub(t, m)

	if _, err := a.send(context.Background(), modelCall{
		Lane: Answer, Stage: replyDecisionBudget,
		Req:     &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "which one?"}}},
		OnChunk: captureOnly,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	calls := m.requestsTo("")
	if len(calls) < 2 {
		t.Fatalf("%d calls made; no second attempt", len(calls))
	}
	// wasShown counts from zero, so index 1 is the second call.
	if !m.wasShown("", 1, thought) {
		t.Errorf("the second attempt did not carry the first's reasoning:\n%s", m.shownTo("", 1))
	}
	if calls[1].Reasoning != "off" {
		t.Errorf("the second attempt asked for reasoning %q, want it switched off", calls[1].Reasoning)
	}
}

// A model the catalog says cannot be asked to stop is given room instead.
//
// Telling glm-5.3 not to think is the same call again: reasoning_optional is
// false, so it spends the budget the same way and returns nothing twice. Nothing
// on the recovery path read that field, which is why the one live failure this
// work started from could not have been recovered even where the guards existed.
func TestSend_ALockedModelIsGivenRoomRatherThanToldToStop(t *testing.T) {
	m := newStubModel(t, map[string]stubReply{"": {Content: "done"}})
	m.answerNth("", stubReply{Content: "", Cut: true})
	a := agentOnStub(t, m)
	a.cfg.MaxTokens = 65536 // headroom for the raise
	a.cfg.ReasoningLocked = func(string) bool { return true }

	if _, err := a.send(context.Background(), modelCall{
		Lane: Answer, Stage: replyDecisionBudget,
		Req: &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hello"}}},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	calls := m.requestsTo("")
	if len(calls) < 2 {
		t.Fatalf("%d calls made; no second attempt", len(calls))
	}
	if calls[1].Reasoning == "off" {
		t.Error("a model that cannot stop reasoning was asked to stop anyway")
	}
	if calls[1].MaxTokens <= calls[0].MaxTokens {
		t.Errorf("the second attempt was given %d tokens against the first's %d — "+
			"a locked model needs room to finish, not an instruction it will ignore",
			calls[1].MaxTokens, calls[0].MaxTokens)
	}
	if calls[1].MaxTokens != lockedRetryCap {
		t.Errorf("the raised cap is %d, want %d", calls[1].MaxTokens, lockedRetryCap)
	}
}

// And when there is no room to give, that is said rather than retried.
//
// The operator's ceiling is a cost control, and a recovery that may quietly
// exceed it is not a control. What comes back names the one thing they can act
// on.
func TestSend_ALockedModelWithNoHeadroomIsReported(t *testing.T) {
	m := newStubModel(t, map[string]stubReply{"": {Content: "", Cut: true}})
	a := agentOnStub(t, m) // MaxTokens 2048
	a.cfg.ReasoningLocked = func(string) bool { return true }

	_, err := a.send(context.Background(), modelCall{
		Lane: Answer, Stage: replyDecisionBudget,
		Req: &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hello"}}},
	})
	if err == nil {
		t.Fatal("a call that could not be recovered reported success")
	}
	if !strings.Contains(err.Error(), "max_tokens") {
		t.Errorf("the error does not name what an operator can change: %v", err)
	}
	if n := m.callsTo(""); n != 1 {
		t.Errorf("%d calls made; with no room to raise, the second is not worth making", n)
	}
}
