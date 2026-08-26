package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// reached reports which recorded stages carry a value, by id.
//
// The question every test here asks. A value exists at one stage; the run then
// hands it around in four forms — a payload, prose, a prompt, a worklog line —
// and until now nothing could ask which stages actually received it.
func reached(records []DebugRecord, value string) []string {
	var ids []string
	for _, r := range records {
		hay := r.Text + " " + string(r.Out) + " " + r.System + " " + r.User + " " + r.Reply
		if p, err := json.Marshal(r.Params); err == nil {
			hay += " " + string(p)
		}
		if strings.Contains(hay, value) {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// A tool's fields are recorded, not only its prose.
//
// This is the shape of the failure that started this: process_list matched
// nothing and said so in its payload — count 0 — while its prose was a bare
// column header. Every stage downstream read the prose, decided the output had
// been truncated, and told the user the data was unusable. The count was there
// the whole time; nothing could show that it was.
func TestDebugRecord_AToolsFieldsAreRecorded(t *testing.T) {
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "find_internet_processes", ToolName: "process_list",
		Params: map[string]any{"filter": "ESTABLISHED"}})
	g.SetBody(id, toolMessageBody{msg: toolapi.ToolEmptyWith("processes",
		`no process matched "ESTABLISHED"`,
		json.RawMessage(`{"at_limit":false,"count":0,"filter":"ESTABLISHED","limit":30}`))})

	recs := g.DebugRecords()
	if len(recs) != 1 {
		t.Fatalf("recorded %d stages, want 1", len(recs))
	}
	r := recs[0]
	if r.ID != id || r.Tool != "process_list" || r.Label != "find_internet_processes" {
		t.Errorf("the stage is not identifiable: %+v", r)
	}
	if !strings.Contains(string(r.Out), `"count":0`) {
		t.Errorf("the fields were not recorded, so nothing can ask whether they were read: %s", r.Out)
	}
	if !strings.Contains(r.Text, "no process matched") {
		t.Errorf("the prose was not recorded: %q", r.Text)
	}
}

// The assertion the whole file exists for: a value produced at one stage, and
// which later stages received it.
func TestDebugRecord_AValueCanBeFollowedThroughTheRun(t *testing.T) {
	const url = "https://solscan.io/txs"

	g := NewGraph()
	search := g.AddNode(&Node{Type: NodeTool, Tag: "search", ToolName: "web_search"})
	g.SetBody(search, toolMessageBody{msg: toolapi.ToolOK("search", "found one",
		json.RawMessage(`{"results":[{"url":"`+url+`"}]}`))})

	// The next step is wired to that URL, so the record of it must carry the
	// value too — that is what proves the wiring, rather than proving the
	// planner wrote a plausible-looking string.
	g.BeginRound()
	fetch := g.AddNode(&Node{Type: NodeTool, Tag: "fetch", ToolName: "web_fetch",
		Params: map[string]any{"url": url}, DependsOn: []string{search}})
	g.SetBody(fetch, toolMessageBody{msg: toolapi.ToolOK("page", "the page",
		json.RawMessage(`{"bytes":81766}`))})

	got := reached(g.DebugRecords(), url)
	if len(got) != 2 {
		t.Fatalf("the URL reached %v — it was produced by %s and used by %s", got, search, fetch)
	}

	// And the wiring is visible: the second stage records what it depended on.
	var f DebugRecord
	for _, r := range g.DebugRecords() {
		if r.ID == fetch {
			f = r
		}
	}
	if len(f.In) != 1 || f.In[0] != search {
		t.Errorf("the fetch does not record where its input came from: in=%v", f.In)
	}
}

// A value that stops somewhere is what a test wants to report. One that only
// checks the final answer cannot say where it was lost; this can.
func TestDebugRecord_AValueThatDoesNotReachAStageIsVisible(t *testing.T) {
	g := NewGraph()
	a := g.AddNode(&Node{Type: NodeTool, Tag: "one", ToolName: "web_search"})
	g.SetBody(a, toolMessageBody{msg: toolapi.ToolOK("search", "",
		json.RawMessage(`{"token":"SENTINEL-42"}`))})

	g.BeginRound()
	b := g.AddNode(&Node{Type: NodeTool, Tag: "two", ToolName: "web_fetch"})
	g.SetBody(b, toolMessageBody{msg: toolapi.ToolOK("page", "unrelated text", nil)})

	got := reached(g.DebugRecords(), "SENTINEL-42")
	if len(got) != 1 || got[0] != a {
		t.Fatalf("expected the value to stop at %s, reached=%v", a, got)
	}
}

// A failed stage records why. The failure is the result, and a run that lost a
// step needs the reason more than it needs the successes.
func TestDebugRecord_AFailureIsRecordedWithItsReason(t *testing.T) {
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeCompute, Tag: "parse_txs", ToolName: "compute"})
	g.SetError(id, errors.New("gate: compute blocked (impact=100 > min(intent=rank(0)) = 0)"))

	recs := g.DebugRecords()
	if len(recs) != 1 {
		t.Fatalf("recorded %d stages, want 1", len(recs))
	}
	if !strings.Contains(recs[0].Err, "compute blocked") {
		t.Errorf("the reason was not recorded: %q", recs[0].Err)
	}
	if recs[0].Kind != "compute" {
		t.Errorf("kind = %q", recs[0].Kind)
	}
}

// Order is completion order, which is what the run actually did rather than
// what the plan said it would.
func TestDebugRecord_OrderIsWhatHappened(t *testing.T) {
	g := NewGraph()
	for _, tag := range []string{"first", "second", "third"} {
		id := g.AddNode(&Node{Type: NodeTool, Tag: tag, ToolName: "bash"})
		g.SetResult(id, tag+" done")
	}
	recs := g.DebugRecords()
	for i, want := range []string{"first", "second", "third"} {
		if recs[i].Label != want {
			t.Errorf("record %d is %q, want %q", i, recs[i].Label, want)
		}
		if recs[i].Seq != i+1 {
			t.Errorf("record %d has seq %d", i, recs[i].Seq)
		}
	}
}

// Records are written from scheduler workers. A tally that only sees one of
// them is worse than none.
func TestDebugRecord_SurvivesConcurrentStages(t *testing.T) {
	g := NewGraph()
	ids := make([]string, 30)
	for i := range ids {
		ids[i] = g.AddNode(&Node{Type: NodeTool, Tag: "x", ToolName: "bash"})
	}
	done := make(chan struct{})
	for _, id := range ids {
		go func(id string) { g.SetResult(id, "ok"); done <- struct{}{} }(id)
	}
	for range ids {
		<-done
	}
	if got := len(g.DebugRecords()); got != len(ids) {
		t.Errorf("recorded %d of %d stages", got, len(ids))
	}
}

// Nothing in the engine reads a record back. That is what makes this safe to
// add to a run: a mistake here can give a TEST a wrong answer and cannot give a
// RUN one. Asserted against the source, because the property is about which
// code exists, not about what one call returns.
func TestDebugRecord_IsWriteOnly(t *testing.T) {
	for _, file := range []string{"scheduler.go", "dispatcher.go", "reflection.go",
		"executive.go", "aggregator.go", "edge_reframe.go", "contextgate.go", "preflight.go"} {
		src := readSource(t, file)
		if strings.Contains(src, "DebugRecords()") {
			t.Errorf("%s reads the debug records back. They are an observation of the run; "+
				"a stage that reads one has made them part of it, and a bug in recording "+
				"becomes a bug in the answer.", file)
		}
	}
}

// Every stage records, including the four that are not nodes. A stage missing
// from the records is a stage no test can ask anything about — and those four
// are the executive, the aggregator, the reframe and preflight, which is to say
// the plan, the answer, the framing and the intent.
func TestDebugRecord_TheNonNodeStagesRecordToo(t *testing.T) {
	want := map[string]string{
		"executive.go":    "the plan",
		"aggregator.go":   "the answer a person reads",
		"edge_reframe.go": "the framing every reasoning stage reads first",
		"scheduler.go":    "preflight's decision, which gates every compute step",
	}
	for file, what := range want {
		if !strings.Contains(readSource(t, file), "recordStage(") {
			t.Errorf("%s records nothing, so %s cannot be followed through a run", file, what)
		}
	}
}
