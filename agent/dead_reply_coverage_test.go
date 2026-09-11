package agent

import (
	"os"
	"strings"
	"testing"
)

// Every lane a person waits on must handle a reply that came back empty.
//
// recoverDeadThought was built for the planner, from a run where one call spent
// 8,192 tokens thinking and returned an empty string. It was wired into the
// planner and nowhere else — so when the same thing happened on the CHAT lane,
// which is the one a person is actually sitting in front of, there was nothing
// to catch it: 112 seconds, all 4,096 tokens spent reasoning, and the reader
// was told "the request finished but produced no answer — nothing usable was
// gathered", which describes a failure to gather and asks them to rephrase.
//
// This reads the source rather than running a lane: constructing an Agent with
// a provider to prove a branch exists is heavier than the thing proved, and the
// regression is textual — a lane that grows a model call without the two guards
// beside it.
func TestEveryWaitedOnLaneHandlesADeadReply(t *testing.T) {
	for _, c := range []struct{ file, lane string }{
		{"executive.go", "the planner"},
		{"chat.go", "the chat lane"},
	} {
		src, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatalf("reading %s: %v", c.file, err)
		}
		body := string(src)

		if !strings.Contains(body, "recoverDeadThought(") {
			t.Errorf("%s never calls recoverDeadThought, so a reply whose budget went "+
				"on reasoning becomes no answer at all on %s", c.file, c.lane)
		}
		if !strings.Contains(body, "roundBudget(") {
			t.Errorf("%s puts no deadline on its model call, so %s waits as long as the "+
				"model takes — max_tokens bounds the reply, not the wait", c.file, c.lane)
		}
	}
}

// The recovery has to reach the reader on a streamed lane.
//
// The chat lane streams its answer as it arrives. A first attempt that produced
// nothing streamed nothing, so the recovered answer has to be sent — otherwise
// it is recovered into a variable the reader never sees.
func TestTheChatRecoveryIsStreamedToTheReader(t *testing.T) {
	src, err := os.ReadFile("chat.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "recoverDeadThought(")
	if i < 0 {
		t.Fatal("the chat lane does not recover at all")
	}
	after := body[i:]
	if j := strings.Index(after, "\n\t}\n"); j > 0 {
		after = after[:j]
	}
	if !strings.Contains(after, "stream(") {
		t.Error("the recovered answer is not streamed, so the reader sees nothing " +
			"even though an answer was produced")
	}
}
