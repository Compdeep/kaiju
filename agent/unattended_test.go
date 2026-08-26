package agent

import (
	"context"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Whether anyone is watching decides what a run may do. The default reads
// Trigger.ExecutionMode; an application that knows it from something else —
// its own kind of work, where the request arrived from — answers directly
// rather than every construction site having to remember a field.

func TestUnattendedDefaultsToExecutionMode(t *testing.T) {
	a := &Agent{}
	if !a.unattended(Trigger{ExecutionMode: "autonomous"}) {
		t.Error("an autonomous run was treated as watched")
	}
	if a.unattended(Trigger{Type: "chat_query"}) {
		t.Error("an ordinary run was treated as unattended")
	}
	if a.unattended(Trigger{}) {
		t.Error("an unmarked run was treated as unattended; the default must be that somebody is there")
	}
}

func TestUnattendedUsesTheApplicationsAnswer(t *testing.T) {
	// An application whose own kind of work is unattended, without the field.
	a := &Agent{isUnattended: func(t Trigger) bool {
		return t.ExecutionMode == "autonomous" || t.Type == "event"
	}}

	if !a.unattended(Trigger{Type: "event"}) {
		t.Error("the application's answer was not used")
	}
	if !a.unattended(Trigger{ExecutionMode: "autonomous"}) {
		t.Error("the application kept the ExecutionMode check and it was ignored")
	}
	if a.unattended(Trigger{Type: "chat_query"}) {
		t.Error("an ordinary run was treated as unattended")
	}
}

// TestUnattendedReachesTheToolFilter: the answer has to arrive where it
// matters. Supplying it and seeing the tool list unchanged would be the
// failure that looks like success.
func TestUnattendedReachesTheToolFilter(t *testing.T) {
	reg := toolapi.NewRegistry()
	if err := reg.Register(&plainTool{name: "list_records"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Register(&humanTool{plainTool{name: "raise_ticket"}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	a := &Agent{registry: reg, isUnattended: func(t Trigger) bool { return t.Type == "event" }}

	// No ExecutionMode at all — only the application's answer marks this
	// unattended, so the default would have offered the tool.
	got := a.relevantTools(context.Background(), nil, Trigger{Type: "event"}, "list some records")
	if has(got, "raise_ticket") {
		t.Errorf("the application said nobody is watching and the tool was offered anyway: %v", got)
	}
	if !has(got, "list_records") {
		t.Errorf("an ordinary tool was withheld: %v", got)
	}
}
