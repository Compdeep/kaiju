package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The blueprint graft, which is the part of the compute change that could fail
// silently.
//
// compute's plan used to be the node's result verbatim, and the graft read it
// with json.Unmarshal(comp.Result). The plan is the envelope's payload now. If
// the graft had been left reading the envelope, the unmarshal would still
// succeed — and find the envelope's own type, "compute", where it looks for
// "blueprint". The gate would not match, the graft would never fire, and no
// error would be reported anywhere. The plan would be made and then dropped.
//
// This runs the whole chain: the tool's message, onto a node, back off it, and
// through the same struct the graft uses.

const graftPlan = `{"type":"blueprint","project_root":"collector/","execute":"python3 collector/main.py",` +
	`"follow_up":[{"tool":"compute","tag":"code_main","params":{"goal":"write the collector"}}],` +
	`"services":[{"name":"api","command":"python3 api.py","port":8080}],` +
	`"validation":[{"name":"health","check":"curl -sf localhost:8080/health","expect":"ok"}]}`

// graftView is the struct the scheduler unmarshals a compute plan into. Kept
// here field for field: if the scheduler's copy grows a field, this one should
// too, and a difference is a reason to look.
type graftView struct {
	Type        string          `json:"type"`
	ProjectRoot string          `json:"project_root,omitempty"`
	Setup       []string        `json:"setup,omitempty"`
	FollowUp    json.RawMessage `json:"follow_up,omitempty"`
	Execute     string          `json:"execute,omitempty"`
	Services    []struct {
		Name    string `json:"name"`
		Command string `json:"command"`
		Workdir string `json:"workdir,omitempty"`
		Port    int    `json:"port,omitempty"`
	} `json:"services,omitempty"`
	Validation []struct {
		Name   string `json:"name"`
		Check  string `json:"check"`
		Expect string `json:"expect"`
	} `json:"validation,omitempty"`
}

func TestTheBlueprintGraftStillReadsThePlan(t *testing.T) {
	// What the tool returns, and what the graph stores for it.
	msg := computeMessage("compute", graftPlan)
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeCompute, ToolName: "compute"})
	g.SetBody(id, NewToolBody(msg))
	result := g.Get(id).Result

	// What the graft does with it.
	var cr graftView
	if err := json.Unmarshal([]byte(computePayload(result)), &cr); err != nil {
		t.Fatalf("the graft cannot parse the plan: %v\n%s", err, result)
	}

	if cr.Type != "blueprint" {
		t.Fatalf("type = %q, want blueprint — the graft is gated on this and would "+
			"never fire, with no error anywhere", cr.Type)
	}
	if cr.ProjectRoot != "collector/" {
		t.Errorf("project_root = %q, want collector/", cr.ProjectRoot)
	}
	if len(cr.FollowUp) == 0 {
		t.Fatal("follow_up is empty — the graft is gated on this too")
	}
	if cr.Execute != "python3 collector/main.py" {
		t.Errorf("execute = %q", cr.Execute)
	}
	if len(cr.Services) != 1 || cr.Services[0].Port != 8080 {
		t.Errorf("services = %+v, want one on port 8080", cr.Services)
	}
	if len(cr.Validation) != 1 || cr.Validation[0].Check == "" {
		t.Errorf("validation = %+v, want one check", cr.Validation)
	}

	// And the follow-up items parse, which is the next thing the graft does.
	var followUps []struct {
		Tool   string         `json:"tool"`
		Tag    string         `json:"tag"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(cr.FollowUp, &followUps); err != nil {
		t.Fatalf("follow_up items do not parse: %v", err)
	}
	if len(followUps) != 1 || followUps[0].Tool != "compute" {
		t.Fatalf("follow_up = %+v, want one compute step", followUps)
	}
}

// Reading it the old way must now fail, or the test above proves nothing: if a
// raw read still worked, the change would not have been needed and the test
// would pass whether or not the graft was updated.
//
// It fails in the worst way available. The envelope has a "type" of its own —
// the tool kind, "compute" — so a raw read does not come back empty, it comes
// back with a different value. The graft's gate is cr.Type == "blueprint", so
// it reads "compute", does not match, and does nothing. No error, no log, no
// missing field: a plan is made and dropped.
func TestReadingThePlanTheOldWayNoLongerWorks(t *testing.T) {
	msg := computeMessage("compute", graftPlan)
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeCompute, ToolName: "compute"})
	g.SetBody(id, NewToolBody(msg))

	// The node's result is the plan itself — Evidence renders the payload when
	// there is no text — so a raw read of the RESULT still works. It is the
	// envelope's JSON that a raw read cannot see through, and that is what the
	// dispatcher stores and what a persisted result holds.
	var fromEnvelope graftView
	if err := json.Unmarshal([]byte(msg.JSON()), &fromEnvelope); err != nil {
		t.Fatalf("the envelope does not parse at all: %v", err)
	}
	if fromEnvelope.Type == "blueprint" {
		t.Fatal("a raw read of the envelope found the plan — then computePayload " +
			"is doing nothing and this test is not checking what it claims")
	}
	if fromEnvelope.Type != "compute" {
		t.Errorf("a raw read found type=%q; the envelope's own type is what it sees", fromEnvelope.Type)
	}
	if len(fromEnvelope.FollowUp) != 0 {
		t.Errorf("a raw read found follow_up, which is inside the payload: %s", fromEnvelope.FollowUp)
	}
}

// The other site the change touched: an exec node's stdout is spliced onto its
// compute parent so ${node.X.output} reaches what the code printed. The splice
// used to merge into the result directly; it merges into the payload now, and
// putting it beside the envelope's keys instead would leave the reference
// resolving to nothing.
//
// Kaiju only — Enbarr's copy has no exec node.
func TestExecStdoutLandsInsideThePlan(t *testing.T) {
	parent := computeMessage("compute", `{"type":"result","files_created":["main.py"]}`).JSON()

	merged, err := mergeJSONField(computePayload(parent), "output", "hello from the script")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	updated := withComputePayload(parent, merged)

	// The reference a downstream step writes.
	msg, ok := toolapi.ParseToolMessage(updated)
	if !ok {
		t.Fatalf("the spliced result is no longer an envelope:\n%s", updated)
	}
	v, found := NewToolBody(msg).Field("output")
	if !found || v != "hello from the script" {
		t.Errorf("${node.X.output} = %v (%v), want what the script printed", v, found)
	}
	// And the plan is still there beside it.
	if v, found := NewToolBody(msg).Field("files_created.0"); !found || v != "main.py" {
		t.Errorf("the splice lost the plan: files_created.0 = %v (%v)", v, found)
	}
}

// compute gets the run state, and the dispatcher gives it before it chooses a
// path.
//
// This is the half of the change that has no compile-time check. compute takes
// the typed branch now, and if the dispatcher built the run state inside the
// branch it replaced — as it used to — compute would be called with nothing and
// fail at runtime with nothing at build time to warn anyone.
func TestComputeReceivesTheRunStateFromTheContext(t *testing.T) {
	// Without it, a clear failure rather than a panic or a silent empty run.
	msg, err := (&ComputeTool{}).ExecuteTyped(context.Background(), map[string]any{"goal": "x", "mode": "shallow"})
	if err != nil {
		t.Fatalf("no run state should be reported, not errored: %v", err)
	}
	if msg.Status != toolapi.StatusError {
		t.Fatalf("status = %q, want error — nothing ran", msg.Status)
	}

	// With it, the same call reaches the state.
	ec := &ExecuteContext{Workspace: "/tmp/x"}
	got := ExecContextFrom(WithExecContext(context.Background(), ec))
	if got != ec {
		t.Fatal("the run state does not survive the context")
	}

	// And the dispatcher puts it there before the branch that decides how a
	// tool is called. Asserted against the source: a test that calls the tool
	// itself cannot notice the dispatcher's order changing.
	src, err := os.ReadFile("dispatcher.go")
	if err != nil {
		t.Fatalf("read dispatcher.go: %v", err)
	}
	text := string(src)
	put := strings.Index(text, "ctx = WithExecContext(ctx, ec)")
	branch := strings.Index(text, "if tx, ok := skill.(toolapi.TypedExecutor); ok {")
	if put < 0 || branch < 0 {
		t.Fatal("the dispatcher no longer puts the run state on the ctx before choosing a path")
	}
	if put > branch {
		t.Errorf("the run state is built after the branch that uses it (put=%d branch=%d) — "+
			"a typed tool would be called without it", put, branch)
	}
}
