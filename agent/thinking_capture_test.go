package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// Closed when the test ends, so a handler still hanging is released rather
	// than holding the server open — a connection the client walked away from
	// stays active until somebody lets go of it.
	over := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(over) }) // runs first: cleanups are undone in reverse
	return &stubModel{Server: srv, reply: map[string]stubReply{}, byNth: map[string][]stubReply{}}
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
