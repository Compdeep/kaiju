package toolapi

import (
	"context"
	"encoding/json"
	"testing"
)

// How far a tool may be called from.
//
// An application that can run a step on another machine has three answers for
// each tool, not two: nobody, this node, or another machine asking this node.
// The middle one is the case a boolean cannot hold — a tool that is fine to run
// here can be a poor thing to let a stranger trigger.

type reachTool struct{ name string }

func (t reachTool) Name() string                { return t.name }
func (t reachTool) Description() string         { return "for the reach tests" }
func (t reachTool) Impact(map[string]any) int   { return ImpactObserve }
func (t reachTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t reachTool) Execute(context.Context, map[string]any) (string, error) {
	return "ran", nil
}

func TestReachDecidesWhoMayCall(t *testing.T) {
	reg := NewRegistry()
	for _, n := range []string{"off", "local", "everywhere"} {
		if err := reg.Register(reachTool{n}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.SetReach("off", ReachOff); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetReach("everywhere", ReachEverywhere); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name              string
		localOK, remoteOK bool
	}{
		{"off", false, false},
		{"local", true, false},
		{"everywhere", true, true},
	}
	for _, c := range cases {
		if _, ok := reg.Get(c.name); ok != c.localOK {
			t.Errorf("Get(%s) = %v, want %v", c.name, ok, c.localOK)
		}
		if _, ok := reg.GetForRemote(c.name); ok != c.remoteOK {
			t.Errorf("GetForRemote(%s) = %v, want %v — this decides what one machine "+
				"may make another do", c.name, ok, c.remoteOK)
		}
	}
}

// A newly registered tool is local. Granting another machine the right to run
// something here should be a thing someone typed, never a default.
func TestANewToolIsLocalNotEverywhere(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(reachTool{"fresh"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("fresh"); !ok {
		t.Error("a newly registered tool should be callable by this node")
	}
	if _, ok := reg.GetForRemote("fresh"); ok {
		t.Error("a newly registered tool is dispatchable by another machine — " +
			"reach must be granted, not defaulted")
	}
	if got := reg.ReachOf("fresh"); got != ReachLocal {
		t.Errorf("reach = %v, want local", got)
	}
}

// SetEnabled is the two-state caller — a dashboard toggle, an impact-tier
// policy. It cannot grant remote reach, because it has no way to say it meant
// to, and silently widening what a stranger may trigger is the one mistake
// worth making impossible.
func TestEnablingCannotGrantRemoteReach(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(reachTool{"t"})
	if err := reg.SetReach("t", ReachEverywhere); err != nil {
		t.Fatal(err)
	}

	if err := reg.SetEnabled("t", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("t"); ok {
		t.Error("disabled and still callable")
	}
	if _, ok := reg.GetForRemote("t"); ok {
		t.Error("disabled and still dispatchable by another machine")
	}

	if err := reg.SetEnabled("t", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("t"); !ok {
		t.Error("re-enabled and not callable")
	}
	if _, ok := reg.GetForRemote("t"); ok {
		t.Error("a two-state toggle restored remote reach — turning a tool off and " +
			"on again must not widen who may call it")
	}
}

// A tool that is off is still listed. That is the difference between off and
// absent: an operator can see it exists and turn it on.
func TestAToolThatIsOffIsStillListed(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(reachTool{"t"})
	_ = reg.SetReach("t", ReachOff)

	infos := reg.ListInfo()
	if len(infos) != 1 {
		t.Fatalf("listed %d tools, want 1", len(infos))
	}
	if infos[0].Enabled {
		t.Error("a tool that is off is listed as enabled")
	}
	if infos[0].Reach != "off" {
		t.Errorf("reach = %q, want off", infos[0].Reach)
	}
}

func TestReachRoundTripsThroughItsWord(t *testing.T) {
	for _, r := range []Reach{ReachOff, ReachLocal, ReachEverywhere} {
		got, ok := ParseReach(r.String())
		if !ok || got != r {
			t.Errorf("%v -> %q -> %v, %v", r, r.String(), got, ok)
		}
	}
	if _, ok := ParseReach("remote"); ok {
		t.Error("an unknown word parsed — the API would accept a typo as a setting")
	}
}

func TestSetReachRefusesAnUnknownTool(t *testing.T) {
	if err := NewRegistry().SetReach("nope", ReachLocal); err == nil {
		t.Error("setting the reach of a tool that is not registered was accepted")
	}
}
