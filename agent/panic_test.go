package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// A panic in application code must not end the process, and each capability has
// its own safe answer — the one thing a shared guard could not have given them.
//
// These run the real wrappers, so a missing recover fails the test by panicking
// rather than by returning the wrong value.
func TestApplicationCodeThatPanicsDoesNotEndTheRun(t *testing.T) {
	boom := func() { panic("application fault") }

	t.Run("a crashed admission rule admits, as a missing one does", func(t *testing.T) {
		a := &Agent{admitRun: func(Trigger) (bool, string) { boom(); return false, "" }}
		ok, reason := a.admit(Trigger{})
		if !ok || reason != "" {
			t.Errorf("admit = %v, %q; want the run admitted", ok, reason)
		}
	})

	t.Run("a crashed tool rule refuses, because it has not said yes", func(t *testing.T) {
		a := &Agent{allowToolFn: func(context.Context, ToolCallRequest) (bool, string) { boom(); return true, "" }}
		allow, reason := a.allowTool(context.Background(), ToolCallRequest{Tool: "open_case"})
		if allow {
			t.Error("a rule that crashed allowed a state-changing call")
		}
		if !strings.Contains(reason, "open_case") {
			t.Errorf("reason = %q; the model is told nothing", reason)
		}
	})

	t.Run("a crashed answer leaves the run to the aggregator", func(t *testing.T) {
		a := &Agent{answer: func(context.Context, AnswerRequest) (*AnswerResult, error) { boom(); return nil, nil }}
		res, ok, err := a.writeAnswer(context.Background(), AnswerRequest{})
		if ok || res != nil {
			t.Errorf("writeAnswer = %v, %v; want the run declined", res, ok)
		}
		if err != nil {
			t.Errorf("err = %v; a crash is not a failed run", err)
		}
	})

	t.Run("a crashed watching rule reads the trigger instead", func(t *testing.T) {
		a := &Agent{isUnattended: func(Trigger) bool { boom(); return false }}
		if !a.unattended(Trigger{ExecutionMode: "autonomous"}) {
			t.Error("an autonomous run was treated as watched")
		}
		if a.unattended(Trigger{Type: "chat_query"}) {
			t.Error("a chat query was treated as unwatched")
		}
	})

	t.Run("a crashed naming rule uses the built-in names", func(t *testing.T) {
		a := &Agent{tokenCategoryFn: func(Trigger) string { boom(); return "" }}
		if got := a.tokenCategory(Trigger{Type: "chat_query"}); got != "chat" {
			t.Errorf("tokenCategory = %q, want the built-in name", got)
		}
	})

	t.Run("a crashed target check rejects the target", func(t *testing.T) {
		a := &Agent{targetValid: func(string) error { boom(); return nil }}
		if err := a.validateTarget("host-1"); err == nil {
			t.Error("a check that crashed approved a target for a call")
		}
	})

	t.Run("a crashed machine lister falls back to the run's target", func(t *testing.T) {
		a := &Agent{targetLister: func(Trigger) []string { boom(); return nil }}
		got := a.runTargets(Trigger{Target: "host-2"})
		if len(got) != 1 || got[0] != "host-2" {
			t.Errorf("runTargets = %v, want the run's own target", got)
		}
	})

	t.Run("crashed wording uses the built-in wording", func(t *testing.T) {
		a := &Agent{describeTrigger: func(Trigger) string { boom(); return "" }}
		if got := a.formatTrigger(Trigger{Type: "event", ID: "a-1"}); got == "" {
			t.Error("every reasoning stage would read nothing")
		}
	})

	t.Run("a crashed executor fails the step rather than passing it", func(t *testing.T) {
		a := &Agent{remoteExec: panicExec{}}
		result, err := a.remoteExecute(context.Background(), RemoteRequest{Target: "machine-a"})
		if err == nil {
			t.Error("a crash part-way through a dial was reported as a step that ran")
		}
		if result != "" {
			t.Errorf("result = %q; nothing came back from the far end", result)
		}
		if !strings.Contains(err.Error(), "machine-a") {
			t.Errorf("err = %v; the machine is not named", err)
		}
	})

	t.Run("a crashed clearance check refuses, because it has not approved", func(t *testing.T) {
		a := &Agent{clearanceCheck: panicClearance{}}
		err := a.checkClearance(context.Background(), "process_kill", nil, "u1")
		if err == nil {
			t.Error("a check that crashed approved a call that is about to change something")
		}
		if !strings.Contains(err.Error(), "process_kill") {
			t.Errorf("err = %v; the tool is not named", err)
		}
	})

	t.Run("a crashed store costs a row and not the run", func(t *testing.T) {
		a := &Agent{eventStore: panicStore{}}
		a.storeRun(Run{ID: "run-1"})
		a.storeAction(Action{ActionType: "process_kill"})
		// Reaching here is the assertion: neither call may panic out, because
		// both run after the answer already exists.
	})

	t.Run("a crashed environment describes nothing", func(t *testing.T) {
		a := &Agent{environment: func() string { boom(); return "" }}
		if got := a.describeEnvironment(); got != "" {
			t.Errorf("describeEnvironment = %q, want the same answer a missing one gives", got)
		}
	})
}

// The three capabilities that are interfaces rather than function types. A
// function can be written inline; these need a receiver to panic from.

type panicExec struct{}

func (panicExec) Execute(context.Context, RemoteRequest) (string, error) {
	panic("application fault")
}

type panicClearance struct{}

func (panicClearance) Check(context.Context, string, map[string]any, string) error {
	panic("application fault")
}

type panicStore struct{}

func (panicStore) InsertRun(Run) error       { panic("application fault") }
func (panicStore) InsertAction(Action) error { panic("application fault") }

// TestEveryCallIntoApplicationCodeIsGuarded is the one that matters. Every test
// above passes on a wrapper written today; this catches the tenth capability
// somebody adds later, because the failure mode is forgetting entirely — which
// is how two of these came to be written without a guard in the first place.
func TestEveryCallIntoApplicationCodeIsGuarded(t *testing.T) {
	// Each entry is a wrapper this package calls application code through, and
	// the Handlers field it guards. Store has two because it is written at
	// two points in a run; every other capability has one.
	wrappers := []struct{ capability, file, fn string }{
		{"Admit", "admission.go", "admit"},
		{"AllowTool", "allowtool.go", "allowTool"},
		{"Answer", "answer.go", "writeAnswer"},
		{"Unattended", "unattended.go", "unattended"},
		{"TokenCategory", "token_category.go", "tokenCategory"},
		{"ValidateTarget", "remote.go", "validateTarget"},
		{"RunTargets", "remote.go", "runTargets"},
		{"Remote", "remote.go", "remoteExecute"},
		{"DescribeTrigger", "loop_react.go", "formatTrigger"},
		{"Refine", "refine.go", "refinePreflight"},
		{"Clearance", "clearance.go", "checkClearance"},
		{"Store", "runrecord.go", "storeRun"},
		{"Store", "runrecord.go", "storeAction"},
		{"Environment", "environment.go", "describeEnvironment"},
		{"Audit", "runrecord.go", "audit"},
	}

	for _, c := range wrappers {
		body := funcBody(t, readSource(t, c.file), c.fn)
		if !strings.Contains(body, "recover()") {
			t.Errorf("%s calls application code with no guard: a panic there ends the process", c.fn)
		}
	}

	// And the list is complete, which the loop above cannot tell.
	//
	// This is the half that was missing. The list said nine and Handlers had
	// thirteen fields, so four capabilities called application code unguarded
	// while a test named "every call" passed. Checking the wrappers against the
	// struct rather than against a number means a fourteenth capability cannot
	// be added without one.
	guarded := map[string]bool{}
	for _, c := range wrappers {
		guarded[c.capability] = true
	}
	caps := reflect.TypeOf(Handlers{})
	for i := 0; i < caps.NumField(); i++ {
		if name := caps.Field(i).Name; !guarded[name] {
			t.Errorf("Handlers.%s is application code with no wrapper in this list — "+
				"either it is called somewhere unguarded, or the list has fallen behind "+
				"the struct", name)
		}
	}
	for name := range guarded {
		if _, ok := caps.FieldByName(name); !ok {
			t.Errorf("this list guards %q, which is not a field of Handlers", name)
		}
	}
}
