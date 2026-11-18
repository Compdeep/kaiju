package configapi

import (
	"testing"

	"github.com/Compdeep/kaiju/agent"
)

type levelSource []agent.Intent

func (s levelSource) ListIntents() ([]agent.Intent, error)        { return s, nil }
func (s levelSource) ListToolIntents() (map[string]string, error) { return nil, nil }

// Only a level the registry names may be saved as the clearance: a 1 from the
// old 0/1/2 scale is refused, and the error lists the real levels.
func TestKnownLevel(t *testing.T) {
	reg := agent.NewIntentRegistry()
	if err := reg.Load(levelSource{{Name: "observe", Rank: 0}, {Name: "operate", Rank: 100}, {Name: "override", Rank: 200}}); err != nil {
		t.Fatal(err)
	}
	for _, rank := range []int{0, 100, 200} {
		if _, ok := knownLevel(reg, rank); !ok {
			t.Errorf("rank %d refused", rank)
		}
	}
	for _, rank := range []int{1, 2, -1, 150, 300} {
		if _, ok := knownLevel(reg, rank); ok {
			t.Errorf("rank %d accepted", rank)
		}
	}
	if levels, _ := knownLevel(reg, 1); levels != "observe (0), operate (100), override (200)" {
		t.Errorf("levels: %q", levels)
	}
	if _, ok := knownLevel(agent.NewIntentRegistry(), 1); !ok {
		t.Error("an empty registry refused a level it cannot check")
	}
}
