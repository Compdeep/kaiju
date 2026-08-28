package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A tool named "bash" that runs what it is given, so the exec child the
// scheduler grafts is a real command producing real stdout. The tools package
// cannot be imported here — it imports this one — and a stub that only echoed a
// canned string would not prove the file reached the shell.
// dir mirrors how the real bash tool is constructed — with the workspace as its
// working directory (cmd/kaiju/main.go) — so a relative run command like
// "bash project/compute.sh" resolves the same way here as in a real run.
type shellTool struct {
	dir   string
	calls int
}

func (s *shellTool) Name() string                { return "bash" }
func (s *shellTool) Description() string         { return "runs a command, for the end-to-end tests" }
func (s *shellTool) Impact(map[string]any) int   { return toolapi.ImpactAffect }
func (s *shellTool) RequiresTarget() bool        { return false }
func (s *shellTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *shellTool) Execute(ctx context.Context, p map[string]any) (string, error) {
	s.calls++
	cmd, _ := p["command"].(string)
	run := exec.CommandContext(ctx, "sh", "-c", cmd)
	run.Dir = s.dir
	out, err := run.CombinedOutput()
	if err != nil {
		return toolapi.ToolFail("command", err.Error(), map[string]any{"stdout": string(out)}).JSON(), nil
	}
	return toolapi.ToolOK("command", "ran", map[string]any{"stdout": string(out)}).JSON(), nil
}

// A whole run reaching compute, driven by a scripted model. Everything but the
// model is real: the plan, the graph, the scheduler's graft of the exec child,
// the dispatcher's reference resolution, the file on disk.

func agentWithCompute(t *testing.T, model *stubModel, extra ...toolapi.Tool) *Agent {
	t.Helper()
	d := t.TempDir()
	a, err := New(Config{
		// RateLimit is set because compute changes something, and a tool that
		// changes something is counted against the hourly limit. The shared
		// helper leaves it at zero, which reads as none allowed.
		ModelConfig: ModelConfig{
			LLMEndpoint: model.URL, LLMAPIKey: "k", LLMModel: "stub",
			MaxTokens: 2048, RateLimit: 1000,
		},
		PathConfig:     PathConfig{Workspace: d, DataDir: d, MetadataDir: d},
		IdentityConfig: IdentityConfig{NodeID: "this-node"},
		DAGConfig:      DAGConfig{DAGEnabled: true, MaxNodes: 20, MaxPerSkill: 10, MaxLLMCalls: 20},
		RoutingConfig:  RoutingConfig{ClassifierEnabled: true},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(a.Stop)
	// compute changes something, so it is gated. The run has to be cleared for
	// that, and the trigger has to ask for it — see operateTrigger.
	a.SetClearance(200)
	if err := a.registry.Register(NewComputeTool(a)); err != nil {
		t.Fatalf("register compute: %v", err)
	}
	for _, tool := range extra {
		if err := a.registry.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name(), err)
		}
	}
	return a
}

// operateTrigger asks for a rank that permits a tool which changes something.
// A chat query otherwise leaves the rank to be inferred, and an unconfigured
// registry infers the lowest one, which no compute can pass.
func operateTrigger(query string) Trigger {
	rank := 100
	return Trigger{
		Type:      "chat_query",
		Data:      json.RawMessage(`{"query":"` + query + `"}`),
		MaxIntent: &rank,
	}
}

// traceNodes reads the run's node snapshot — the same JSON the browser renders.
func traceNodes(t *testing.T, res *SyncResult) []map[string]any {
	t.Helper()
	if res == nil {
		t.Fatal("no result")
	}
	var nodes []map[string]any
	if err := json.Unmarshal(res.Trace, &nodes); err != nil {
		t.Fatalf("trace is not the expected JSON: %v (%.200s)", err, res.Trace)
	}
	return nodes
}

// nodeWithTag finds a node by its tag. A retried node carries a marker after
// its tag — "listing [oneshot_retry]" — so the tag is matched up to that.
func nodeWithTag(t *testing.T, nodes []map[string]any, tag string) map[string]any {
	t.Helper()
	for _, n := range nodes {
		got, _ := n["tag"].(string)
		if name, _, _ := strings.Cut(got, " ["); name == tag {
			return n
		}
	}
	t.Fatalf("no node tagged %q in the trace: %v", tag, nodes)
	return nil
}

// findFile returns the one path under root whose base name matches, or fails.
func findFile(t *testing.T, root, base string) string {
	t.Helper()
	var found []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(p) == base {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 1 {
		t.Fatalf("looking for %q under %s: found %v, want exactly one", base, root, found)
	}
	return found[0]
}

func computePlanned(tag string, params map[string]any) stubReply {
	p := map[string]any{"goal": "structure what the pages said", "mode": "shallow"}
	for k, v := range params {
		p[k] = v
	}
	return plan(step("compute", tag, p))
}

// A file nothing can run is written, and the step that wrote it succeeds.
//
// It used to fail instead: a shallow compute that named no task_files was
// required to produce something runnable, so a checklist, a document or a page
// took the whole step down and left nothing on disk. Three runs died this way
// on one afternoon, on languages "json" and "plaintext".
func TestComputeWritesAFileNothingCanRun(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "operate"}},
		"plan":             computePlanned("write_checklist", nil),
		"submit_code": {Args: map[string]any{
			"language": "json",
			"filename": "privesc_checklist.json",
			"code":     `{"checks":["suid","sudo -l"]}`,
		}},
		"reflector_decision": {Args: map[string]any{"decision": "conclude", "outcome": "written"}},
	})
	a := agentWithCompute(t, model)

	res, err := a.RunDAGSync(context.Background(), operateTrigger("list the checks"))
	if err != nil {
		t.Fatalf("the run failed: %v (stages called: %v)", err, model.functionsCalled())
	}

	node := nodeWithTag(t, traceNodes(t, res), "write_checklist")
	if node["state"] != "resolved" {
		t.Fatalf("compute state = %v, want resolved. err = %v", node["state"], node["err"])
	}

	body, readErr := os.ReadFile(findFile(t, a.cfg.Workspace, "privesc_checklist.json"))
	if readErr != nil {
		t.Fatalf("reading what compute wrote: %v", readErr)
	}
	if !strings.Contains(string(body), "sudo -l") {
		t.Errorf("the file holds %q, want the Coder's content", body)
	}
}

// Nothing ran, so there is no output — and a step that asks for one is told
// which field it asked for, rather than handed something else.
//
// This is what the removed guard was protecting: a reference to .output on a
// node that ran nothing. It is handled a layer down now, by name, at the step
// that got it wrong — and the file is written either way, so a re-plan has
// something to read.
func TestAStepAskingForOutputThatWasNeverProducedIsToldSo(t *testing.T) {
	reader := &countingTool{name: "process_list"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "operate"}},
		"plan": plan(
			step("compute", "write_checklist", map[string]any{
				"goal": "structure what the pages said", "mode": "shallow",
			}),
			map[string]any{
				"tool": "process_list", "tag": "deliver", "depends_on": []int{0},
				"params": map[string]any{"filter": "${step.0.output}"},
			},
		),
		"submit_code": {Args: map[string]any{
			"language": "json",
			"filename": "privesc_checklist.json",
			"code":     `{"checks":["suid"]}`,
		}},
		"reflector_decision": {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentWithCompute(t, model, reader)

	res, err := a.RunDAGSync(context.Background(), operateTrigger("list the checks"))
	if err != nil {
		t.Fatalf("the run failed: %v (stages called: %v)", err, model.functionsCalled())
	}
	nodes := traceNodes(t, res)

	if got := nodeWithTag(t, nodes, "write_checklist")["state"]; got != "resolved" {
		t.Errorf("the compute step state = %v, want resolved — the file it wrote is what a re-plan reads", got)
	}
	if reader.calls != 0 {
		t.Errorf("the reading step ran %d times with a value it was never given", reader.calls)
	}
	deliver := nodeWithTag(t, nodes, "deliver")
	if errText, _ := deliver["err"].(string); !strings.Contains(errText, `field "output" absent`) {
		t.Errorf("the reading step failed with %q, want it to name the field it asked for", errText)
	}
}

// The path that was already working keeps working: a script gets a run command
// from its language, the scheduler runs it, and its printed output reaches the
// step that asked for it.
func TestAScriptStillRunsAndItsOutputReachesTheNextStep(t *testing.T) {
	reader := &countingTool{name: "process_list"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "operate"}},
		"plan": plan(
			step("compute", "count_them", map[string]any{
				"goal": "count what the pages listed", "mode": "shallow",
			}),
			map[string]any{
				"tool": "process_list", "tag": "deliver", "depends_on": []int{0},
				"params": map[string]any{"filter": "${step.0.output}"},
			},
		),
		"submit_code": {Args: map[string]any{
			"language": "bash",
			"filename": "compute.sh",
			"code":     "echo seventeen-checks",
		}},
		"reflector_decision": {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentWithCompute(t, model, reader)
	// Registered after the agent exists, because it needs the workspace path.
	if err := a.registry.Register(&shellTool{dir: a.cfg.Workspace}); err != nil {
		t.Fatalf("register bash: %v", err)
	}

	if _, err := a.RunDAGSync(context.Background(), operateTrigger("how many checks?")); err != nil {
		t.Fatalf("the run failed: %v (stages called: %v)", err, model.functionsCalled())
	}
	if reader.calls != 1 {
		t.Fatalf("the reading step ran %d times, want once", reader.calls)
	}
	if got, _ := reader.got["filter"].(string); !strings.Contains(got, "seventeen-checks") {
		t.Errorf("the reading step got %q, want what the script printed", got)
	}
}

// Edit mode is the other way through the same function, and it still replaces
// text in a file that is already there.
//
// The write path is what changed; this asserts the branch above it did not.
func TestEditModeStillReplacesTextInAnExistingFile(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "operate"}},
		"plan": computePlanned("amend_notes", map[string]any{
			"task_files": []string{"project/notes.txt"},
		}),
		"submit_code": {Args: map[string]any{
			"language": "text",
			"filename": "project/notes.txt",
			"edits": []map[string]any{
				{"old_content": "one check", "new_content": "seventeen checks"},
			},
		}},
		"reflector_decision": {Args: map[string]any{"decision": "conclude", "outcome": "amended"}},
	})
	a := agentWithCompute(t, model)

	existing := filepath.Join(a.cfg.Workspace, "project", "notes.txt")
	if err := os.MkdirAll(filepath.Dir(existing), 0o755); err != nil {
		t.Fatalf("preparing the file to edit: %v", err)
	}
	if err := os.WriteFile(existing, []byte("we found one check today\n"), 0o644); err != nil {
		t.Fatalf("preparing the file to edit: %v", err)
	}

	res, err := a.RunDAGSync(context.Background(), operateTrigger("amend the notes"))
	if err != nil {
		t.Fatalf("the run failed: %v (stages called: %v)", err, model.functionsCalled())
	}
	if got := nodeWithTag(t, traceNodes(t, res), "amend_notes")["state"]; got != "resolved" {
		t.Fatalf("compute state = %v, want resolved", got)
	}

	body, readErr := os.ReadFile(existing)
	if readErr != nil {
		t.Fatalf("reading the edited file: %v", readErr)
	}
	if want := "we found seventeen checks today\n"; string(body) != want {
		t.Errorf("the file holds %q, want %q", body, want)
	}
}
