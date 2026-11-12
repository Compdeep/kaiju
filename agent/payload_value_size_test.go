package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A fetched page's summary reaches the planner whole. Each text field of a step's
// result was cut to 400 characters, its first and last 200, for every stage: the
// Horizons documentation summary (3,600 characters) reached the planner as its
// opening and its end, the parameter list in between was lost, and the planner
// guessed parameters six times (run ea4397c6).
func TestAPageSummaryReachesThePlannerWhole(t *testing.T) {
	summary := "API overview. " + strings.Repeat("filler ", 250) + "EPHEM_TYPE='VECTORS', START_TIME, STOP_TIME, STEP_SIZE. " + strings.Repeat("more ", 360)
	if len(summary) < 3500 || len(summary) > 4000 {
		t.Fatalf("summary is %d characters, want a page summary's size", len(summary))
	}
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "fetch_doc", ToolName: "web_fetch"})
	g.SetBody(id, toolMessageBody{msg: toolapi.ToolOK("page", summary, map[string]any{"content": summary, "full_content_path": "fetched/doc"})})

	for _, r := range g.StepResults(-1) {
		if got := stepResultContent(r); !strings.Contains(got, "START_TIME, STOP_TIME, STEP_SIZE") {
			t.Fatalf("the middle of the summary was cut from what the planner reads:\n%.300s…", got)
		}
		return
	}
	t.Fatal("the step is missing from StepResults")
}
