package agent

import (
	"testing"

	"github.com/Compdeep/kaiju/agent/gates"
)

// A pinned intent cannot take a run past the ceiling its scope allows.
//
// Trigger.MaxIntent is documented as a cap that "can only lower intent, never
// escalate". For a chat query the engine returned the pin as given, and its
// own ceiling check in the scheduler runs only when the intent was INFERRED —
// so a caller that pinned above the scope's ceiling was trusted outright.
// kaiju's own API clamps before building the trigger; the engine must not
// depend on every caller remembering to.
func TestTriggerIntent_PinIsLoweredToTheScopeCeiling(t *testing.T) {
	pin := 200
	tr := Trigger{
		Type:      "chat_query",
		MaxIntent: &pin,
		Scope:     &ResolvedScope{MaxIntent: 0},
	}
	if got := tr.Intent(); got != gates.Intent(0) {
		t.Errorf("Intent() = %v with a pin of 200 under a ceiling of 0, want 0", got)
	}
}

// A pin below the ceiling is the caller choosing to do less, and is kept.
func TestTriggerIntent_PinBelowTheCeilingIsKept(t *testing.T) {
	pin := 100
	tr := Trigger{Type: "chat_query", MaxIntent: &pin, Scope: &ResolvedScope{MaxIntent: 200}}
	if got := tr.Intent(); got != gates.Intent(100) {
		t.Errorf("Intent() = %v, want the pinned 100", got)
	}
}

// No scope means a local caller with full access — a CLI user on their own
// machine — and the pin stands as given. The ceiling is a property of a
// resolved scope, not something invented when there is none.
func TestTriggerIntent_NoScopeLeavesThePinAlone(t *testing.T) {
	pin := 200
	tr := Trigger{Type: "chat_query", MaxIntent: &pin}
	if got := tr.Intent(); got != gates.Intent(200) {
		t.Errorf("Intent() = %v with no scope, want the pinned 200", got)
	}
}

// With no pin the chat is left to the planner, and the scheduler's own
// ceiling check covers that case already. Nothing here may turn auto into a
// number.
func TestTriggerIntent_NoPinStaysAuto(t *testing.T) {
	tr := Trigger{Type: "chat_query", Scope: &ResolvedScope{MaxIntent: 0}}
	if got := tr.Intent(); got != gates.IntentAuto {
		t.Errorf("Intent() = %v with no pin, want auto", got)
	}
}

// The same holds for a trigger that is not a chat: its structural rank is
// still bounded by the scope it was handed.
func TestTriggerIntent_NonChatPinIsAlsoBounded(t *testing.T) {
	pin := 200
	tr := Trigger{Type: "event", MaxIntent: &pin, Scope: &ResolvedScope{MaxIntent: 100}}
	if got := tr.Intent(); got != gates.Intent(100) {
		t.Errorf("Intent() = %v for an event pinned at 200 under a ceiling of 100, want 100", got)
	}
}
