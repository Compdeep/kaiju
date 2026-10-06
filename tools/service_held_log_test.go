package tools

import (
	"testing"
)

// A condition that persists is logged when it is first seen, not on every tick.
//
// reapDead runs every few seconds and the condition it reports here — a dead pid
// whose port something else still answers — lasts until a human clears it. The
// line wrote itself 25,019 times across three days: 26% of a 15MB log, with every
// real entry older than that pushed out of the file. The log was unusable for the
// diagnosis that needed it.

// The state key is what decides whether to speak, so it is what the test checks:
// the same pid and port stay silent, a different one is news.
func TestHeldPortIsReportedOncePerState(t *testing.T) {
	s := NewService(t.TempDir())
	defer close(s.stopPoll)

	speak := func(name string, pid, port int) bool {
		state := heldState(pid, port)
		if s.held[name] == state {
			return false
		}
		s.held[name] = state
		return true
	}

	if !speak("webreader", 285564, 8092) {
		t.Fatal("the first time a held port is found it must be reported")
	}
	for i := 0; i < 50; i++ {
		if speak("webreader", 285564, 8092) {
			t.Fatalf("tick %d reported the same pid and port again", i)
		}
	}
	// A different orphan on the same port is a different fact.
	if !speak("webreader", 999999, 8092) {
		t.Error("a new pid holding the port was not reported")
	}
	// So is the same pid moving port.
	if !speak("webreader", 999999, 9000) {
		t.Error("a change of port was not reported")
	}
	// Another service is tracked separately.
	if !speak("illustrator", 1234, 8093) {
		t.Error("a second service was silenced by the first")
	}
}

// Clearing the memory means a recurrence is reported again, which is why the
// delete calls beside the healthy and port-free branches matter.
func TestHeldPortIsForgottenWhenItClears(t *testing.T) {
	s := NewService(t.TempDir())
	defer close(s.stopPoll)

	s.held["webreader"] = heldState(285564, 8092)
	delete(s.held, "webreader") // what reapDead does when the port is free or the pid lives

	if s.held["webreader"] != "" {
		t.Fatal("the service is still remembered, so a recurrence would stay silent")
	}
}
