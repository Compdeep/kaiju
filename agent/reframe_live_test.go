//go:build live

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A live look at what the reframe actually writes. Run with:
//
//	go test ./agent/ -tags live -run TestLiveReframe -v
//
// Behind a build tag because it calls a real model and costs money.

func liveAgent(t *testing.T) *Agent {
	t.Helper()
	raw, err := os.ReadFile("../kaiju.json")
	if err != nil {
		t.Skipf("no kaiju.json: %v", err)
	}
	var cfg struct {
		LLM struct {
			Endpoint string `json:"endpoint"`
			APIKey   string `json:"api_key"`
			Model    string `json:"model"`
		} `json:"llm"`
		Executor struct {
			Model string `json:"model"`
		} `json:"executor"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("kaiju.json: %v", err)
	}
	if cfg.LLM.APIKey == "" {
		t.Skip("no api key configured")
	}
	a, err := New(Config{
		ModelConfig: ModelConfig{
			LLMEndpoint:      cfg.LLM.Endpoint,
			LLMAPIKey:        cfg.LLM.APIKey,
			LLMModel:         cfg.LLM.Model,
			ExecutorEndpoint: cfg.LLM.Endpoint,
			ExecutorAPIKey:   cfg.LLM.APIKey,
			ExecutorModel:    cfg.Executor.Model,
			MaxTokens:        1024,
		},
		PathConfig: PathConfig{
			Workspace: t.TempDir(), MetadataDir: t.TempDir(), DataDir: t.TempDir(),
		},
		IdentityConfig: IdentityConfig{NodeID: "live"},
	})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	return a
}

func TestLiveReframe(t *testing.T) {
	a := liveAgent(t)

	readers := []struct{ name, line string }{
		{"reflector", "decide whether this run should do more work or stop and answer"},
		{"aggregator", "write the final answer a person will read"},
		{"verdict", "decide how serious this is and whether it warrants acting on"},
	}

	runs := []struct {
		name    string
		request string
		build   func(*Graph)
	}{
		{
			name:    "everything worked, and nothing was followed up",
			request: "which advisories affect the parser on this host?",
			build: func(g *Graph) {
				withStep(g, "find advisories", "web_search",
					toolapi.ToolOK("search", "", map[string]any{"results": []map[string]any{
						{"url": "https://example.test/advisory-2026-1", "title": "parser overflow"},
						{"url": "https://example.test/advisory-2026-2", "title": "parser panic"},
					}}))
			},
		},
		{
			name:    "a step returned nothing and one failed",
			request: "is this host affected by the parser bug?",
			build: func(g *Graph) {
				withStep(g, "check the config", "file_read",
					toolapi.ToolEmpty("text", "the file is empty: /etc/parser.conf"))
				withStep(g, "list processes", "process_list",
					toolapi.ToolOK("listing", "sshd\nnginx\nparserd", nil))
				id := g.AddNode(&Node{Type: NodeTool, Tag: "reach the host", ToolName: "http_get"})
				g.SetError(id, fmt.Errorf("dial tcp 10.0.0.9:443: i/o timeout"))
			},
		},
		{
			name:    "a tool that would not say what it found",
			request: "did anything persist on this machine?",
			build: func(g *Graph) {
				withStep(g, "check persistence", "check_persistence",
					toolapi.ToolMessage{Type: "text", Status: toolapi.StatusUnclassified,
						Content: "3 units in /etc/systemd/system, 1 cron entry"})
			},
		},
	}

	for _, r := range runs {
		g := NewGraph()
		trigger := Trigger{Type: "chat_query", ID: "live"}
		g.Context = NewContextGate(g, &trigger, a)
		r.build(g)
		fmt.Printf("\n════════════════════════════════════════════════════════\nRUN: %s\nREQUEST: %s\n", r.name, r.request)
		fmt.Printf("\nMATERIAL THE GATE ASSEMBLED:\n%s\n\n", a.reframeMaterial(context.Background(), g, r.request))
		for _, rd := range readers {
			fmt.Printf("──────── reader: %s ────────\n%s\n\n",
				rd.name, a.EdgeReFrame(context.Background(), g, r.request, rd.line))
		}
	}
}

func TestLiveReframeHarderCases(t *testing.T) {
	a := liveAgent(t)
	// Registered so the engine can see which output fields are handles: without
	// the schema nothing is ever "in hand and unused", which is the case the
	// first harness never actually exercised.
	if err := a.Registry().Register(livewebsearch{}); err != nil {
		t.Fatalf("register: %v", err)
	}

	readers := []struct{ name, line string }{
		{"reflector", "decide whether this run should do more work or stop and answer"},
		{"aggregator", "write the final answer a person will read"},
	}

	runs := []struct {
		name    string
		request string
		build   func(*Graph)
	}{
		{
			name:    "results in hand that nothing has opened",
			request: "what is the fix for the parser overflow?",
			build: func(g *Graph) {
				withStep(g, "find advisories", "web_search",
					toolapi.ToolOK("search", "", map[string]any{"results": []map[string]any{
						{"url": "https://example.test/advisory-2026-1", "title": "parser overflow — patched in 4.2"},
						{"url": "https://example.test/advisory-2026-2", "title": "parser panic"},
					}}))
			},
		},
		{
			name:    "nothing worked at all",
			request: "why is the service down?",
			build: func(g *Graph) {
				a := g.AddNode(&Node{Type: NodeTool, Tag: "read the log", ToolName: "file_read"})
				g.SetError(a, fmt.Errorf("open /var/log/parserd.log: permission denied"))
				b := g.AddNode(&Node{Type: NodeTool, Tag: "check the unit", ToolName: "service_status"})
				g.SetError(b, fmt.Errorf("systemctl: command not found"))
			},
		},
		{
			name:    "two steps that disagree",
			request: "is parserd running?",
			build: func(g *Graph) {
				withStep(g, "list processes", "process_list",
					toolapi.ToolOK("listing", "sshd\nnginx", nil))
				withStep(g, "check the port", "net_info",
					toolapi.ToolOK("listing", "tcp 0.0.0.0:9000 LISTEN parserd/1121", nil))
			},
		},
		{
			name:    "not security at all",
			request: "summarise what this repository does",
			build: func(g *Graph) {
				withStep(g, "read the readme", "file_read",
					toolapi.ToolOK("text", "# ledger\n\nA double-entry accounting library for Go.", nil))
				withStep(g, "list the tests", "file_list",
					toolapi.ToolEmpty("listing", "no files matched *_test.go"))
			},
		},
	}

	for _, r := range runs {
		g := NewGraph()
		trigger := Trigger{Type: "chat_query", ID: "live"}
		g.Context = NewContextGate(g, &trigger, a)
		r.build(g)
		fmt.Printf("\n════════════════════════════════════════════════════════\nRUN: %s\nREQUEST: %s\n", r.name, r.request)
		fmt.Printf("\nMATERIAL:\n%s\n\n", a.reframeMaterial(context.Background(), g, r.request))
		for _, rd := range readers {
			fmt.Printf("──────── reader: %s ────────\n%s\n\n",
				rd.name, a.EdgeReFrame(context.Background(), g, r.request, rd.line))
		}
	}
}

// livewebsearch declares the same reference annotation kaiju's own web_search
// does, so a url it returns is a value the engine knows can be followed.
type livewebsearch struct{}

func (livewebsearch) Name() string                { return "web_search" }
func (livewebsearch) Description() string         { return "search" }
func (livewebsearch) Impact(map[string]any) int   { return 0 }
func (livewebsearch) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (livewebsearch) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}
func (livewebsearch) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(`{"type":"object","properties":{"results":{"type":"array","items":{"type":"object","properties":{"url":{"type":"string","x-reference":"web_fetch.url"},"title":{"type":"string"}}}}}}`)
}
