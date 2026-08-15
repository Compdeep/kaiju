package agent

import (
	"context"
	"reflect"
	"testing"

	"github.com/Compdeep/kaiju/agent/gates"
)

// A handler supplied through Config has to reach the field that calls it.
//
// applyHandlers is fourteen lines of `if cfg.X != nil { a.y = cfg.X }`, and
// those lines are the only thing joining what an application supplies to what
// the engine reads. Add a field and forget its line and the code compiles,
// every other test passes, and the application's callback is never called.
//
// That is not hypothetical. Five separate defects on this branch were the same
// shape — a capability supplied and never reaching the code that uses it, one
// of them for forty commits — and Config.Environment shipped not working.
//
// So this walks Handlers by reflection rather than naming its fields: a
// field added without a wiring line fails here, and a field added without an
// entry in the table below fails too. Neither can be forgotten quietly.

// wiredTo says, for each handler, how to supply it and how to tell whether
// it arrived. Every field of Handlers must appear, which
// TestEveryCapabilityIsChecked enforces.
//
// The check reads the agent's private field directly. That is the point: the
// public path is Config, and what is being tested is that the private field
// behind it was assigned.
var wiredTo = map[string]struct {
	supply  func(*Handlers)
	arrived func(*Agent) bool
}{
	"Unattended": {
		func(c *Handlers) { c.Unattended = func(Trigger) bool { return true } },
		func(a *Agent) bool { return a.isUnattended != nil },
	},
	"TokenCategory": {
		func(c *Handlers) { c.TokenCategory = func(Trigger) string { return "x" } },
		func(a *Agent) bool { return a.tokenCategoryFn != nil },
	},
	"Admit": {
		func(c *Handlers) { c.Admit = func(Trigger) (bool, string) { return true, "" } },
		func(a *Agent) bool { return a.admitRun != nil },
	},
	"Refine": {
		func(c *Handlers) {
			c.Refine = func(context.Context, *PreflightResult, *Trigger) (*PreflightResult, string, error) {
				return nil, "", nil
			}
		},
		func(a *Agent) bool { return a.refine != nil },
	},
	"Answer": {
		func(c *Handlers) {
			c.Answer = func(context.Context, AnswerRequest) (*AnswerResult, error) { return nil, nil }
		},
		func(a *Agent) bool { return a.answer != nil },
	},
	"AllowTool": {
		func(c *Handlers) {
			c.AllowTool = func(context.Context, ToolCallRequest) (bool, string) { return true, "" }
		},
		func(a *Agent) bool { return a.allowToolFn != nil },
	},
	"Audit": {
		func(c *Handlers) { c.Audit = func(gates.AuditEntry) {} },
		func(a *Agent) bool { return a.auditFn != nil },
	},
	"Clearance": {
		func(c *Handlers) { c.Clearance = wiringClearance{} },
		func(a *Agent) bool { return a.clearanceCheck != nil },
	},
	"Store": {
		func(c *Handlers) { c.Store = wiringStore{} },
		func(a *Agent) bool { return a.eventStore != nil },
	},
	"Remote": {
		func(c *Handlers) { c.Remote = wiringRemote{} },
		func(a *Agent) bool { return a.remoteExec != nil },
	},
	"ValidateTarget": {
		func(c *Handlers) { c.ValidateTarget = func(string) error { return nil } },
		func(a *Agent) bool { return a.targetValid != nil },
	},
	"RunTargets": {
		func(c *Handlers) { c.RunTargets = func(Trigger) []string { return nil } },
		func(a *Agent) bool { return a.targetLister != nil },
	},
	"Environment": {
		func(c *Handlers) { c.Environment = func() string { return "here" } },
		func(a *Agent) bool { return a.environment != nil },
	},
	"DescribeTrigger": {
		func(c *Handlers) { c.DescribeTrigger = func(Trigger) string { return "because" } },
		func(a *Agent) bool { return a.describeTrigger != nil },
	},
}

type wiringClearance struct{}

func (wiringClearance) Check(context.Context, string, map[string]any, string) error { return nil }

type wiringStore struct{}

func (wiringStore) InsertRun(Run) error       { return nil }
func (wiringStore) InsertAction(Action) error { return nil }

type wiringRemote struct{}

func (wiringRemote) Execute(context.Context, RemoteRequest) (string, error) { return "", nil }

// Each handler, supplied on its own so a missing line cannot be hidden by a
// neighbour's.
func TestEveryHandlerReachesItsField(t *testing.T) {
	for name, c := range wiredTo {
		t.Run(name, func(t *testing.T) {
			var caps Handlers
			c.supply(&caps)

			a := &Agent{}
			a.applyHandlers(Config{Handlers: caps})

			if !c.arrived(a) {
				t.Errorf("Config.Handlers.%s was supplied and the field that reads it is "+
					"still empty — applyHandlers has no line for it, so an application "+
					"setting it gets silence", name)
			}
		})
	}
}

// Supplying nothing must leave everything empty, or the check above would pass
// on an agent that fills its fields regardless of what it was given.
func TestNoHandlersLeavesEveryFieldEmpty(t *testing.T) {
	a := &Agent{}
	a.applyHandlers(Config{})

	for name, c := range wiredTo {
		if c.arrived(a) {
			t.Errorf("%s arrived without being supplied, so the check for it proves nothing", name)
		}
	}
}

// The silent case: a field added to Handlers and left out of the table.
func TestEveryHandlerIsChecked(t *testing.T) {
	fields := reflect.TypeOf(Handlers{})
	for i := range fields.NumField() {
		name := fields.Field(i).Name
		if _, ok := wiredTo[name]; !ok {
			t.Errorf("Handlers.%s has no entry in wiredTo, so nothing checks that "+
				"supplying it reaches the engine", name)
		}
	}
	for name := range wiredTo {
		if _, ok := fields.FieldByName(name); !ok {
			t.Errorf("wiredTo names %s, which is not a field of Handlers", name)
		}
	}
}
