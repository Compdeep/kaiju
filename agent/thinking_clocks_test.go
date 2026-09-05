package agent

import (
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
)

func thinksOnly(id string) func(string) bool {
	return func(m string) bool { return m == id }
}

// The two clocks move together or neither moves.
//
// A call by a thinking model is given twice the request deadline, and the run
// that makes it twice the wall clock. Raising one alone leaves the other as the
// binding constraint: the run is cancelled by the shorter clock and the error
// says "context canceled", naming neither. That is what a live deployment saw —
// a 206s planner call inside a 600s run budget, and three cancellations in the
// error log that were nothing to do with the provider.
func TestAThinkingReasoningModelDoublesTheWallClock(t *testing.T) {
	a := &Agent{
		llm: llm.NewClient("http://127.0.0.1:1", "", "thinker"),
		cfg: Config{
			DAGConfig:   DAGConfig{DAGWallClock: 10 * time.Minute},
			ModelConfig: ModelConfig{Thinks: thinksOnly("thinker")},
		},
	}
	if got := a.wallClock(); got != 20*time.Minute {
		t.Errorf("wall clock = %s, want 20m — a thinking reasoning model gets twice the run", got)
	}
}

// A model that does not think gains nothing from a longer run, so it does not
// get one. The doubling is paid for by the thinking, not granted by the lane.
func TestANonThinkingReasoningModelKeepsTheConfiguredWallClock(t *testing.T) {
	a := &Agent{
		llm: llm.NewClient("http://127.0.0.1:1", "", "plain"),
		cfg: Config{
			DAGConfig:   DAGConfig{DAGWallClock: 10 * time.Minute},
			ModelConfig: ModelConfig{Thinks: thinksOnly("thinker")},
		},
	}
	if got := a.wallClock(); got != 10*time.Minute {
		t.Errorf("wall clock = %s, want the configured 10m", got)
	}
}

// No wall clock configured means no wall clock at all, and doubling nothing has
// to stay nothing rather than becoming a limit the deployment never asked for.
func TestNoWallClockStaysNoWallClock(t *testing.T) {
	a := &Agent{
		llm: llm.NewClient("http://127.0.0.1:1", "", "thinker"),
		cfg: Config{ModelConfig: ModelConfig{Thinks: thinksOnly("thinker")}},
	}
	if got := a.wallClock(); got != 0 {
		t.Errorf("wall clock = %s, want 0 — an unset clock is not a clock to double", got)
	}
}

// An application that supplies no catalog gets the behaviour it had before any
// of this existed.
func TestWithoutACatalogTheWallClockIsUntouched(t *testing.T) {
	a := &Agent{
		llm: llm.NewClient("http://127.0.0.1:1", "", "thinker"),
		cfg: Config{DAGConfig: DAGConfig{DAGWallClock: 10 * time.Minute}},
	}
	if got := a.wallClock(); got != 10*time.Minute {
		t.Errorf("wall clock = %s, want the configured 10m when Thinks is nil", got)
	}
}
