package agent

import (
	"context"
	"reflect"
	"testing"
)

// A capability supplied through Config has to reach the field that uses it.
//
// applyCapabilities is fourteen lines of `if cfg.X != nil { a.y = cfg.X }`, and
// those lines are the only thing joining what an application supplies to what
// the engine reads. Add a field and forget its line and the code compiles,
// every other test passes, and the application's callback is never called.
//
// That is not hypothetical. Five separate defects on this branch were the same
// shape — a capability supplied and never reaching the code that uses it, one
// of them for forty commits — and Config.Environment shipped not working.
//
// So this walks Capabilities by reflection rather than naming its fields: a
// field added without a wiring line fails here, and a field added without an
// entry in the table below fails too. Neither can be forgotten quietly.

// wiredTo says, for each capability, how to supply it and how to tell whether
// it arrived. Every field of Capabilities must appear, which
// TestEveryCapabilityIsChecked enforces.
//
// The check reads the agent's private field directly. That is the point: the
// public path is Config, and what is being tested is that the private field
// behind it was assigned.
var wiredTo = map[string]struct {
	supply  func(*Capabilities)
	arrived func(*Agent) bool
}{
	"Unattended": {
		func(c *Capabilities) { c.Unattended = func(Trigger) bool { return true } },
		func(a *Agent) bool { return a.isUnattended != nil },
	},
	"TokenCategory": {
		func(c *Capabilities) { c.TokenCategory = func(Trigger) string { return "x" } },
		func(a *Agent) bool { return a.tokenCategoryFn != nil },
	},
	"Admit": {
		func(c *Capabilities) { c.Admit = func(Trigger) (bool, string) { return true, "" } },
		func(a *Agent) bool { return a.admitRun != nil },
	},
	"Refine": {
		func(c *Capabilities) {
			c.Refine = func(context.Context, *PreflightResult, *Trigger) (*PreflightResult, string, error) {
				return nil, "", nil
			}
		},
		func(a *Agent) bool { return a.refine != nil },
	},
	"Answer": {
		func(c *Capabilities) {
			c.Answer = func(context.Context, AnswerRequest) (*AnswerResult, error) { return nil, nil }
		},
		func(a *Agent) bool { return a.answer != nil },
	},
	"AllowTool": {
		func(c *Capabilities) {
			c.AllowTool = func(context.Context, ToolCallRequest) (bool, string) { return true, "" }
		},
		func(a *Agent) bool { return a.allowToolFn != nil },
	},
	"Clearance": {
		func(c *Capabilities) { c.Clearance = wiringClearance{} },
		func(a *Agent) bool { return a.clearanceCheck != nil },
	},
	"Store": {
		func(c *Capabilities) { c.Store = wiringStore{} },
		func(a *Agent) bool { return a.eventStore != nil },
	},
	"Remote": {
		func(c *Capabilities) { c.Remote = wiringRemote{} },
		func(a *Agent) bool { return a.remoteExec != nil },
	},
	"ValidateTarget": {
		func(c *Capabilities) { c.ValidateTarget = func(string) error { return nil } },
		func(a *Agent) bool { return a.targetValid != nil },
	},
	"RunTargets": {
		func(c *Capabilities) { c.RunTargets = func(Trigger) []string { return nil } },
		func(a *Agent) bool { return a.targetLister != nil },
	},
	"Environment": {
		func(c *Capabilities) { c.Environment = func() string { return "here" } },
		func(a *Agent) bool { return a.environment != nil },
	},
	"DescribeTrigger": {
		func(c *Capabilities) { c.DescribeTrigger = func(Trigger) string { return "because" } },
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

// Each capability, supplied on its own so a missing line cannot be hidden by a
// neighbour's.
func TestEveryCapabilityReachesItsField(t *testing.T) {
	for name, c := range wiredTo {
		t.Run(name, func(t *testing.T) {
			var caps Capabilities
			c.supply(&caps)

			a := &Agent{}
			a.applyCapabilities(Config{Capabilities: caps})

			if !c.arrived(a) {
				t.Errorf("Config.Capabilities.%s was supplied and the field that reads it is "+
					"still empty — applyCapabilities has no line for it, so an application "+
					"setting it gets silence", name)
			}
		})
	}
}

// Supplying nothing must leave everything empty, or the check above would pass
// on an agent that fills its fields regardless of what it was given.
func TestNoCapabilitiesLeavesEveryFieldEmpty(t *testing.T) {
	a := &Agent{}
	a.applyCapabilities(Config{})

	for name, c := range wiredTo {
		if c.arrived(a) {
			t.Errorf("%s arrived without being supplied, so the check for it proves nothing", name)
		}
	}
}

// The silent case: a field added to Capabilities and left out of the table.
func TestEveryCapabilityIsChecked(t *testing.T) {
	fields := reflect.TypeOf(Capabilities{})
	for i := range fields.NumField() {
		name := fields.Field(i).Name
		if _, ok := wiredTo[name]; !ok {
			t.Errorf("Capabilities.%s has no entry in wiredTo, so nothing checks that "+
				"supplying it reaches the engine", name)
		}
	}
	for name := range wiredTo {
		if _, ok := fields.FieldByName(name); !ok {
			t.Errorf("wiredTo names %s, which is not a field of Capabilities", name)
		}
	}
}
