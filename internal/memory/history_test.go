package memory

import (
	"strings"
	"testing"
)

// A long conversation must hand the planner its RECENT turns. LoadHistory used
// GetMessages — ORDER BY created_at LIMIT, the OLDEST N — so a thread only had
// to pass fifty messages for the opening to be kept and everything since,
// including the turn being replied to, to be dropped. The comment on
// GetRecentMessages says as much.
func TestLoadHistoryKeepsTheTail(t *testing.T) {
	src := readSourceFile(t, "manager.go")
	fn := funcBody(t, src, "func (m *Manager) LoadHistory(")
	if strings.Contains(fn, "m.db.GetMessages(") {
		t.Error("LoadHistory windows to the OLDEST N again — a long chat loses the turn the user is replying to")
	}
	if !strings.Contains(fn, "m.db.GetRecentMessages(") {
		t.Error("LoadHistory no longer takes the tail of the conversation")
	}
}

// A message that was shortened must not read like a message that ended there.
func TestHeadTailNamesTheCut(t *testing.T) {
	long := strings.Repeat("a", 500) + "MIDDLE" + strings.Repeat("z", 500)
	got := headTail(long, 200)

	if !strings.Contains(got, "cut") || !strings.Contains(got, "1006") {
		t.Errorf("the gap is not named with the whole size: %q", got)
	}
	if strings.Contains(got, "MIDDLE") {
		t.Error("nothing was actually cut")
	}
	// Both ends survive: a reply opens with what it found and closes with what
	// it concluded.
	if !strings.HasPrefix(got, "aaa") || !strings.HasSuffix(got, "zzz") {
		t.Errorf("an end was lost: %.20q … %.20q", got, got[len(got)-20:])
	}
}

// Nothing is added to a message that fits.
func TestHeadTailLeavesShortMessagesAlone(t *testing.T) {
	const s = "a short reply"
	if got := headTail(s, 2000); got != s {
		t.Errorf("a short message was changed: %q", got)
	}
}

// The caps are named, not literals. 700 and 1300 were shorter than most answers
// this engine writes, so nearly every turn arrived with its middle replaced.
func TestHistoryCapsAreNamedAndRoomy(t *testing.T) {
	if historyAssistantChars < 1500 || historyUserChars < 1500 {
		t.Errorf("history caps are back to shredding ordinary turns: assistant=%d user=%d",
			historyAssistantChars, historyUserChars)
	}
}
