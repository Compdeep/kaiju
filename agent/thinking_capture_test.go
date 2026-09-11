package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
)

// A model that thinks aloud and then never finishes.
//
// This is the shape of the run that started all of this: the planner reasons for
// its whole allowance and the call is stopped by the clock. The point of the
// server is the hang — a cancelled call returns an error and no reply, so
// anything read off the reply is read off nothing.
func thinkingThenHanging(t *testing.T, thoughts ...string) *stubModel {
	t.Helper()
	return &stubModel{Server: hangingModel(t, thoughts...).Server}
}

// hangingModel is that server with what it was sent kept, and answering from
// the second call on — so a test can read what the retry was asked for.
type hangingRig struct {
	*httptest.Server
	mu    sync.Mutex
	sent  []string
	calls int
}

// asked is the body of the nth request (from 1), or "" if it was never made.
func (h *hangingRig) asked(nth int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if nth < 1 || nth > len(h.sent) {
		return ""
	}
	return h.sent[nth-1]
}

func hangingModel(t *testing.T, thoughts ...string) *hangingRig {
	t.Helper()
	rig := &hangingRig{}
	// Closed when the test ends, so a handler still hanging is released rather
	// than holding the server open — a connection the client walked away from
	// stays active until somebody lets go of it.
	over := make(chan struct{})
	rig.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rig.mu.Lock()
		rig.sent = append(rig.sent, string(body))
		rig.calls++
		first := rig.calls == 1
		rig.mu.Unlock()

		// Only the first call hangs. A retry that hung too would prove nothing
		// and end the test with a stopped clock rather than an answer.
		if !first {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"content":"the March invoice"},"finish_reason":"stop"}]}`)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flush := func() {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		flush()
		for _, thought := range thoughts {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"reasoning\":%s}}]}\n\n", mustJSON(thought))
			flush()
		}
		select { // still thinking when the caller gives up
		case <-r.Context().Done():
		case <-over:
		}
	}))
	t.Cleanup(rig.Close)
	t.Cleanup(func() { close(over) }) // runs first: cleanups are undone in reverse
	return rig
}

// The thinking is kept as it arrives, not read off the reply.
//
// A call stopped by its deadline returns an error and nothing else, so the
// reasoning it had already produced went in the bin — the planner was given two
// minutes, spent them, and the retry that followed started from the same blank
// page as the first attempt.
func TestThinkingSurvivesACallThatIsCutOff(t *testing.T) {
	model := thinkingThenHanging(t, "the file is large, ", "so read it in windows")
	a := agentOnStub(t, model)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	resp, thought, err := a.completeHeavyStreaming(ctx, &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "plan it"}},
	})
	if err == nil {
		t.Fatalf("the call was supposed to be cut off, and returned %+v", resp)
	}
	if thought != "the file is large, so read it in windows" {
		t.Errorf("what it thought before the cut was lost: %q", thought)
	}
}

// Nothing thought is not the same as something thought and dropped.
func TestACutCallThatThoughtNothingCarriesNothing(t *testing.T) {
	model := thinkingThenHanging(t)
	a := agentOnStub(t, model)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, thought, _ := a.completeHeavyStreaming(ctx, &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "plan it"}},
	})
	if thought != "" {
		t.Errorf("invented %q out of a call that thought nothing", thought)
	}
	if cutThought(thought) != nil {
		t.Error("an empty thought became a reply for the retry to work from")
	}
}

// The retry is shown the thinking the first attempt paid for.
//
// Handing it back is the whole reason for keeping it. Recovered from a cut reply
// this already worked, because the reasoning was on the reply; recovered from a
// cancelled call it had to come from the capture instead, and the two paths now
// hand the recovery the same thing.
func TestTheRetryIsGivenWhatTheFirstAttemptThought(t *testing.T) {
	const thought = "process_list first, then read only the flagged pid"
	retry := withoutThinkingRetry(
		&llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "plan it"}}},
		cutThought(thought),
	)
	if retry == nil {
		t.Fatal("no retry was built")
	}
	last := retry.Messages[len(retry.Messages)-1].Content
	if !strings.Contains(last, thought) {
		t.Errorf("the retry starts from a blank page — it was not shown the thinking:\n%s", last)
	}
	if !strings.Contains(last, "cut off") {
		t.Errorf("the thinking is handed back as settled rather than as unfinished:\n%s", last)
	}
}

// A reader of the run sees why the plan is the plan.
//
// The reasoning was being collected already, to hand to a retry when a call was
// cut off, and dropped on every call that finished. So the trace showed the
// plan and not one word of what it weighed on the way there — which of two
// tools it chose between, what it took the question to mean.
func TestThePlanningRowCarriesWhatItThought(t *testing.T) {
	const thought = "the question is about processes, so list them before reading anything"

	planned := plan(step("process_list", "procs", nil))
	planned.Reasoning = thought
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{
			"mode": "agent", "intent": "observe", "skills": []string{},
		}},
		"plan": planned,
	})
	a := agentOnStub(t, model, &countingTool{name: "process_list"})

	events, unsub := a.SubscribeDAG()
	defer unsub()
	seen := make(chan string, 1)
	go func() {
		for ev := range events {
			if ev.Node != nil && ev.Node.Type == "executive" && ev.Node.State == "resolved" {
				select {
				case seen <- ev.Node.Reasoning:
				default:
				}
			}
		}
	}()

	if _, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: []byte(`{"query":"what is running?"}`),
	}); err != nil {
		t.Fatalf("the run failed: %v", err)
	}

	select {
	case got := <-seen:
		if got != thought {
			t.Errorf("the planning row does not carry what it thought:\ngot  %q\nwant %q", got, thought)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no resolved planning row was broadcast")
	}
}

// Long thinking is shown from both ends.
//
// A retry keeps the tail, because it needs the conclusion the model was about to
// reach. A person reads from the top, and the opening line is the preview — so
// cutting the head to fit would remove the one line the row shows.
func TestLongThinkingKeepsBothEnds(t *testing.T) {
	long := "OPENING. " + strings.Repeat("middle. ", 4000) + " CLOSING."
	shown := shownThinking(long)

	if len(shown) > maxShownThinking+200 { // the elision line is the allowance
		t.Errorf("shortened to %d characters, which is not shorter than it needs to be", len(shown))
	}
	if !strings.HasPrefix(shown, "OPENING.") {
		t.Error("the opening was cut, and the opening is what the collapsed row shows")
	}
	if !strings.HasSuffix(shown, "CLOSING.") {
		t.Error("the conclusion was cut")
	}
	if !strings.Contains(shown, "not shown") {
		t.Error("the middle went missing with nothing saying so")
	}
	if kept := shownThinking("short enough"); kept != "short enough" {
		t.Errorf("thinking that fits was disturbed: %q", kept)
	}
}

// The chat lane's retry is given the thinking too.
//
// The lane a person actually sits in front of had the same hole the planner
// did: its deadline cancels the call, a cancelled call returns no reply, and
// the reasoning went with it — so the retry began from nothing while the reader
// had already watched two minutes of thinking stream past.
//
// Driven through the real lane rather than the seam, because the two guards
// have to be wired to each other and not merely present: the deadline branch is
// what carries the buffer, and it is reached only by the lane's own clock.
func TestTheChatRetryStartsFromWhatWasAlreadyThought(t *testing.T) {
	const thought = "they are asking about the invoice from March, not the contract"

	// A model that thinks aloud and then stops answering, so the lane's own
	// deadline is what ends the call. The allowance is shortened for the test:
	// "fast" is the one effort permitted below the floor, and the floor is two
	// minutes.
	rig := hangingModel(t, thought)
	restore := effortBudget[EffortFast]
	effortBudget[EffortFast] = 300 * time.Millisecond
	defer func() { effortBudget[EffortFast] = restore }()

	a := agentOnStub(t, &stubModel{Server: rig.Server})
	res, err := a.Converse(context.Background(), ChatTurn{
		Query: "which one did we settle on?",
		Base:  Trigger{ReasoningEffort: EffortFast},
	})
	if err != nil {
		t.Fatalf("the turn failed outright rather than recovering: %v", err)
	}
	if res.Content == "" {
		t.Error("the deadline produced no answer, so the recovery did not reach the reader")
	}

	asked := rig.asked(2)
	if asked == "" {
		t.Fatal("no second call was made, so the deadline was reported rather than recovered")
	}
	if !strings.Contains(asked, thought) {
		t.Errorf("the chat retry was not shown the thinking the first attempt paid for:\n%s", asked)
	}
}

// A call that finished has its thinking on the reply, and that is the copy to
// keep.
//
// This is the fault that made every planning row empty while the model was
// plainly thinking. A model delivers reasoning two ways — its own field, or
// written into the content between <think> and </think> — and only the first
// passes through a stream callback: the second is lifted out of the finished
// content by the client, after the last chunk. Reading the capture instead of
// the reply threw that away on every call that was not cut off, which is
// almost all of them.
func TestThinkingIsTakenFromTheReplyWhenThereIsOne(t *testing.T) {
	const lifted = "the pitch file first, then the defence one"
	resp := &llm.ChatResponse{Choices: []llm.Choice{{
		Message: llm.Message{Role: "assistant", Content: "the plan", Reasoning: lifted},
	}}}

	var cap thinkingCapture // nothing streamed as reasoning, which is the case under test
	if got := thinkingOf(resp, &cap); got != lifted {
		t.Errorf("thinking = %q, want the reply's own %q", got, lifted)
	}
}

// With no reply there is only the capture, which is what a cancelled call
// leaves behind.
func TestThinkingFallsBackToTheCaptureWithNoReply(t *testing.T) {
	var cap thinkingCapture
	cap.onChunk("halfway through a thought", "reasoning")
	if got := thinkingOf(nil, &cap); got != "halfway through a thought" {
		t.Errorf("thinking = %q, want what was captured before the call was cut", got)
	}
}

// Thinking written into the content is thinking, and a cut-off call never
// closes the tag.
//
// The capture kept only reasoning chunks, so a model that writes its thinking
// inline streamed past it entirely — every chunk was content, and a deadline
// then handed the retry nothing.
func TestTheCaptureKeepsThinkingWrittenInline(t *testing.T) {
	var cap thinkingCapture
	cap.onChunk("<think>the two files are", "content")
	cap.onChunk(" the same document", "content")

	if got := cap.text(); got != "the two files are the same document" {
		t.Errorf("captured %q, want the thinking out of the content", got)
	}

	// And a finished block leaves the answer behind rather than carrying it.
	var done thinkingCapture
	done.onChunk("<think>weighing it up</think>here is the answer", "content")
	if got := done.text(); got != "weighing it up" {
		t.Errorf("captured %q, want only the thinking — the answer is not thought", got)
	}
}
