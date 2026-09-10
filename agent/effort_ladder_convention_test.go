package agent

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The picker shows a person how long each effort allows, and the clock that
// cuts the call off is here.
//
// Two numbers for one fact, in two languages. That is exactly how fits_small_call
// came to be re-derived in four places and disagree with the daemon for a day —
// so this reads the picker's own table and fails when it drifts. A person told
// "fast — 1 min" who waits two is being lied to by a control, which is worse
// than a control that says nothing.
func TestTheEffortTableInTheUIMatchesTheClock(t *testing.T) {
	const table = "../web/src/services/reasoning.js"
	src, err := os.ReadFile(table)
	if err != nil {
		t.Skipf("no web sources here: %v", err) // the engine builds without them
	}

	block := regexp.MustCompile(`(?s)export const EFFORT_SECONDS = \{(.*?)\}`).FindSubmatch(src)
	if block == nil {
		t.Fatalf("%s no longer declares EFFORT_SECONDS; the picker's times come from somewhere this test cannot check", table)
	}

	shown := map[string]int{}
	for _, m := range regexp.MustCompile(`(?m)^\s*'?([a-z]*)'?\s*:\s*(\d+),`).FindAllStringSubmatch(string(block[1]), -1) {
		var secs int
		if err := json.Unmarshal([]byte(m[2]), &secs); err != nil {
			t.Fatalf("%q is not a number of seconds", m[2])
		}
		shown[m[1]] = secs
	}
	if len(shown) == 0 {
		t.Fatalf("read no entries out of EFFORT_SECONDS in %s", table)
	}

	a := &Agent{}
	for effort, secs := range shown {
		want := a.roundBudget(context.Background(), Heavy, Trigger{ReasoningEffort: effort})
		if got := time.Duration(secs) * time.Second; got != want {
			t.Errorf("the picker says %q allows %s; the clock allows %s",
				labelOf(effort), got, want)
		}
	}

	// Every effort the engine takes has to appear, or the picker offers a value
	// whose allowance it cannot state.
	for _, effort := range append(ReasoningEfforts(), EffortDefault) {
		if _, ok := shown[effort]; !ok {
			t.Errorf("%s has no entry for %q, which the engine accepts", table, labelOf(effort))
		}
	}
}

// labelOf names the ordinary setting in an error message. It is "" in the code
// and "normal" to a reader, and an error saying `""` names nothing.
func labelOf(effort string) string {
	if strings.TrimSpace(effort) == "" {
		return "normal"
	}
	return effort
}
