package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A step that runs on another machine leaves a line here.
//
// The decision was made on this machine — this is where the principal's tool
// list was applied, where the target was checked, where the application's
// clearance hook answered — so this is where the record of it belongs. The far
// end keeps its own log, of its own decisions, and nothing on this side can
// read it. Without these lines a machine that ordered a process killed
// elsewhere has no record that it did.

// auditedAgent returns an agent whose gate writes to a file, and the path.
func auditedAgent(t *testing.T, tool toolapi.Tool) (*Agent, string) {
	t.Helper()
	dir := t.TempDir()
	gate, err := gates.NewGate(gates.GateConfig{
		MaxTurns:  10,
		RateLimit: 1000,
		Clearance: stubClearance{level: 200},
		AuditDir:  dir,
	})
	if err != nil {
		t.Fatalf("new gate: %v", err)
	}
	registry := toolapi.NewRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}
	reg, _, _ := newTestStack(t)
	a := &Agent{registry: registry, gate: gate, intentRegistry: reg}
	a.cfg.NodeID = "self"
	return a, filepath.Join(dir, "audit.jsonl")
}

// auditLines reads back what the gate wrote.
func auditLines(t *testing.T, path string) []gates.AuditEntry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("open audit log: %v", err)
	}
	defer f.Close()

	var out []gates.AuditEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e gates.AuditEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("audit line is not an entry: %v", err)
		}
		out = append(out, e)
	}
	return out
}

// refusingClearance stands in for an authorisation endpoint that says no.
type refusingClearance struct{}

func (refusingClearance) Check(context.Context, string, map[string]any, string) error {
	return errors.New("not authorised for this machine")
}

// okExec stands in for a machine that answered.
type okExec struct{}

func (okExec) Execute(context.Context, RemoteRequest) (string, error) {
	return "killed pid 4021", nil
}

func TestARemoteStepIsAudited(t *testing.T) {
	a, path := auditedAgent(t, &refusalTool{})
	a.remoteExec = okExec{}

	if got := fireOne(t, a, &Node{Type: NodeTool, ToolName: "counter", Target: "machine-b"}); got.Err != nil {
		t.Fatalf("the step failed: %v", got.Err)
	}

	lines := auditLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("audit has %d lines, want 1 — a call that ran on another machine left no record here", len(lines))
	}
	e := lines[0]
	if e.Target != "machine-b" {
		t.Errorf("Target = %q, want machine-b; a line that cannot say which machine cannot answer what the log is for", e.Target)
	}
	if e.Tool != "counter" {
		t.Errorf("Tool = %q, want counter", e.Tool)
	}
	if e.Result == "" {
		t.Error("Result is empty; what the far end answered is the only outcome this side has")
	}
	if e.Error != "" {
		t.Errorf("Error = %q on a call that succeeded", e.Error)
	}
}

// A machine that could not be reached is a fact worth keeping. It is also the
// case most likely to be asked about afterwards.
func TestAnUnreachableRemoteStepIsAudited(t *testing.T) {
	a, path := auditedAgent(t, &refusalTool{})
	a.remoteExec = failingExec{}

	if got := fireOne(t, a, &Node{Type: NodeTool, ToolName: "counter", Target: "machine-b"}); got.Err == nil {
		t.Fatal("an unreachable machine reported success")
	}

	lines := auditLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("audit has %d lines, want 1", len(lines))
	}
	if lines[0].Error == "" {
		t.Error("the failure was recorded as a success")
	}
	if lines[0].Target != "machine-b" {
		t.Errorf("Target = %q, want machine-b", lines[0].Target)
	}
}

// Each of the three checks this side makes is refused in its own way, and each
// refusal is a decision this machine took. The local path records its refusals;
// so does this one.
func TestARemoteRefusalIsAudited(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*Agent)
		scope *ResolvedScope
	}{
		{
			name:  "outside the principal's tool list",
			setup: func(a *Agent) { a.remoteExec = okExec{} },
			scope: &ResolvedScope{Username: "u", AllowedTools: map[string]bool{"something_else": true}},
		},
		{
			name: "a target the application rejects",
			setup: func(a *Agent) {
				a.remoteExec = okExec{}
				a.targetValid = func(string) error { return errors.New("no such machine") }
			},
		},
		{
			name: "refused by the application's authorisation endpoint",
			setup: func(a *Agent) {
				a.remoteExec = okExec{}
				a.clearanceCheck = refusingClearance{}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, path := auditedAgent(t, &refusalTool{})
			c.setup(a)

			graph := NewGraph()
			id := graph.AddNode(&Node{Type: NodeTool, ToolName: "counter", Target: "machine-b"})
			ch := make(chan nodeCompletion, 1)
			a.fireNode(context.Background(), graph.Get(id), graph, nil, ch, "",
				newToolThrottle(), gates.Intent(0), c.scope)
			<-ch

			lines := auditLines(t, path)
			if len(lines) != 1 {
				t.Fatalf("audit has %d lines, want 1 — a refusal on this machine left no record", len(lines))
			}
			if lines[0].Error == "" {
				t.Error("the refusal was recorded without a reason")
			}
			if lines[0].Target != "machine-b" {
				t.Errorf("Target = %q, want machine-b", lines[0].Target)
			}
		})
	}
}

// An application keeping its own record gets every line the file gets.
//
// The engine writes a file whose name and format it chose, and an application
// with a dashboard cannot read it. Without this the record a person looks at is
// the one that stays empty.
func TestTheApplicationSeesEveryAuditLine(t *testing.T) {
	var got []gates.AuditEntry
	a, _ := auditedAgent(t, &refusalTool{})
	a.auditFn = func(e gates.AuditEntry) { got = append(got, e) }
	a.remoteExec = okExec{}

	if c := fireOne(t, a, &Node{Type: NodeTool, ToolName: "counter", Target: "machine-b"}); c.Err != nil {
		t.Fatalf("the step failed: %v", c.Err)
	}

	if len(got) != 1 {
		t.Fatalf("the application received %d lines, want 1", len(got))
	}
	if got[0].Tool != "counter" || got[0].Target != "machine-b" {
		t.Errorf("entry = %+v; it arrives as the engine wrote it", got[0])
	}
	if got[0].Time == "" {
		t.Error("Time is empty; the application gets the line after it is completed, not before")
	}
}

// Both destinations get the same lines. Neither can be given one the other was
// not, because every decision goes through the one wrapper.
func TestTheFileAndTheApplicationAgree(t *testing.T) {
	seen := 0
	a, path := auditedAgent(t, &refusalTool{})
	a.auditFn = func(gates.AuditEntry) { seen++ }
	a.remoteExec = failingExec{}

	fireOne(t, a, &Node{Type: NodeTool, ToolName: "counter", Target: "machine-b"})

	if lines := auditLines(t, path); len(lines) != seen {
		t.Errorf("the file has %d lines and the application saw %d", len(lines), seen)
	}
	if seen != 1 {
		t.Errorf("the application saw %d lines, want 1", seen)
	}
}

// A record that crashes loses its line and nothing else. The alternative is a
// tool call failing because the writing of its audit line failed.
func TestAnAuditWriteThatPanicsDoesNotStopTheRun(t *testing.T) {
	a, _ := auditedAgent(t, &refusalTool{})
	a.auditFn = func(gates.AuditEntry) { panic("the database is gone") }
	a.remoteExec = okExec{}

	if c := fireOne(t, a, &Node{Type: NodeTool, ToolName: "counter", Target: "machine-b"}); c.Err != nil {
		t.Fatalf("a panicking audit write failed the call: %v", c.Err)
	}
}

// Who asked is the first question put to an audit line, and an audit page
// filters by it. A scope carries the answer, so every line records it.
func TestAnAuditLineNamesThePrincipal(t *testing.T) {
	var got []gates.AuditEntry
	a, _ := auditedAgent(t, &refusalTool{})
	a.auditFn = func(e gates.AuditEntry) { got = append(got, e) }

	graph := NewGraph()
	id := graph.AddNode(&Node{Type: NodeTool, ToolName: "counter"})
	ch := make(chan nodeCompletion, 1)
	a.fireNode(context.Background(), graph.Get(id), graph,
		NewBudget(20, 5, 20, 5, time.Minute), ch, "", newToolThrottle(), gates.Intent(0),
		&ResolvedScope{Username: "ada", AllowedTools: map[string]bool{"*": true}})
	<-ch

	if len(got) != 1 {
		t.Fatalf("audit has %d lines, want 1", len(got))
	}
	if got[0].Username != "ada" {
		t.Errorf("Username = %q, want ada — a line that cannot say who asked cannot be filtered by it", got[0].Username)
	}
}

// A run with no principal is the ordinary unattended case, and says so by
// leaving the field empty rather than by inventing a name for it.
func TestAnUnattendedRunNamesNoPrincipal(t *testing.T) {
	var got []gates.AuditEntry
	a, _ := auditedAgent(t, &refusalTool{})
	a.auditFn = func(e gates.AuditEntry) { got = append(got, e) }

	fireOne(t, a, &Node{Type: NodeTool, ToolName: "counter"})

	if len(got) != 1 {
		t.Fatalf("audit has %d lines, want 1", len(got))
	}
	if got[0].Username != "" {
		t.Errorf("Username = %q on a run with no scope, want empty", got[0].Username)
	}
}
