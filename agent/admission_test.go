package agent

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Run admission.
//
// Three things have to hold, and the third is the one that is easy to get
// wrong:
//
//   - nil admits everything, so an application with no such rules is unaffected
//   - a refusal carries the application's own wording, not a generic message
//   - EVERY entry point asks. A check on one path and not another is worse than
//     no check, because it looks enforced and isn't.

func TestAdmitNilAdmitsEverything(t *testing.T) {
	a := &Agent{}
	ok, reason := a.admit(Trigger{Type: "chat_query"})
	if !ok {
		t.Errorf("a nil admission check refused a run (%q) — the capability is off, not closed", reason)
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty when admitted", reason)
	}

	// And on a nil agent, so no caller has to guard before asking.
	var nilAgent *Agent
	if ok, _ := nilAgent.admit(Trigger{}); !ok {
		t.Error("a nil agent should admit rather than refuse")
	}
}

func TestAdmitPassesTheTriggerAndKeepsTheReason(t *testing.T) {
	var seen Trigger
	a := &Agent{admitRun: func(t Trigger) (bool, string) {
		seen = t
		return false, "licence expired on 2026-07-01"
	}}

	ok, reason := a.admit(Trigger{Type: "alert", ID: "a-1"})
	if ok {
		t.Fatal("the run was admitted despite the check refusing it")
	}
	if reason != "licence expired on 2026-07-01" {
		t.Errorf("reason = %q, want the application's own wording", reason)
	}
	if seen.Type != "alert" || seen.ID != "a-1" {
		t.Errorf("the check was not given the trigger: %+v", seen)
	}
}

func TestAdmitSuppliesAReasonWhenTheApplicationDoesNot(t *testing.T) {
	a := &Agent{admitRun: func(Trigger) (bool, string) { return false, "" }}
	ok, reason := a.admit(Trigger{})
	if ok {
		t.Fatal("admitted despite refusal")
	}
	if reason == "" {
		t.Error("a refusal with no reason produced no reason — the caller has nothing to report")
	}
}

// TestAdmitReachesEveryEntryPoint is the important one. RunDAGSync must ask
// before doing anything, and must report the refusal as a result rather than an
// error — the caller asked for work the application had already decided not to
// do, which is not a failure.
//
// It once checked runDAG too. That function had no callers and was deleted, so
// this scanned a dead path and passed on it.
func TestAdmitReachesEveryEntryPoint(t *testing.T) {
	src := readSource(t, "scheduler.go")

	sync := funcBody(t, src, "RunDAGSync")
	if !strings.Contains(sync, "a.admit(trigger)") {
		t.Error("RunDAGSync does not consult run admission — work starts that the " +
			"application had already refused")
	}

	if !strings.Contains(sync, "NotAdmitted: true") {
		t.Error("RunDAGSync does not mark a refusal as such; the caller cannot tell a " +
			"refusal from an answer without reading the text")
	}
}

// readSource reads a file from this package for assertions about call sites.
func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

/*
 * funcBody returns the body of a named method on *Agent.
 * desc: Scoping an assertion to one function is stricter than searching the
 *       whole file: "RunDAGSync consults admission" is the claim, and a mention
 *       anywhere else in a two-thousand-line file would satisfy a plain search
 *       while the function itself had stopped asking.
 * param: src - the file contents.
 * param: name - the method name.
 * return: the body, from the signature to the closing brace in column zero.
 */
func funcBody(t *testing.T, src, name string) string {
	t.Helper()
	// Any receiver, or none: the functions asked about live on *Agent, on
	// *Scheduler and at package level, and a helper that only matched the first
	// would report "not found" for a function that is there.
	loc := regexp.MustCompile(`(?m)^func (\([^)]*\) )?` + regexp.QuoteMeta(name) + `\(`).FindStringIndex(src)
	if loc == nil {
		t.Fatalf("function %s not found", name)
	}
	start := loc[0]
	if end := strings.Index(src[start:], "\n}\n"); end >= 0 {
		return src[start : start+end]
	}
	return src[start:]
}
