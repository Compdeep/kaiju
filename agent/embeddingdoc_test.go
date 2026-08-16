package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The example in docs/embedding.md, compiled. A document people build from
// should not contain code that does not compile.

type docProcessList struct{}

func (docProcessList) Name() string              { return "process_list" }
func (docProcessList) Description() string       { return "List running processes." }
func (docProcessList) Impact(map[string]any) int { return toolapi.ImpactObserve }
func (docProcessList) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (p docProcessList) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	lines := []string{"sshd", "nginx"}
	if len(lines) == 0 {
		return toolapi.ToolEmpty("listing", "no processes matched"), nil
	}
	return toolapi.ToolOK("listing", strings.Join(lines, "\n"),
		map[string]any{"count": len(lines)}), nil
}

func (p docProcessList) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(p.ExecuteTyped(ctx, params))
}

func TestTheEmbeddingDocumentCompiles(t *testing.T) {
	dir := t.TempDir()
	ag, err := New(Config{
		ModelConfig: ModelConfig{
			LLMEndpoint: "http://127.0.0.1:1", LLMAPIKey: "k", LLMModel: "gpt-4o", MaxTokens: 4096,
		},
		PathConfig:     PathConfig{Workspace: dir, DataDir: dir, MetadataDir: dir},
		IdentityConfig: IdentityConfig{NodeID: "this-node"},
		DAGConfig:      DAGConfig{DAGEnabled: true, MaxNodes: 20, MaxLLMCalls: 20},
		RoutingConfig:  RoutingConfig{ClassifierEnabled: true},
		Handlers: Handlers{
			AllowTool: func(ctx context.Context, req ToolCallRequest) (bool, string) {
				if req.Tool == "delete_everything" && req.Target != "" {
					return false, "that tool may only run on this machine"
				}
				return true, ""
			},
		},
	})
	if err != nil {
		t.Fatalf("the document's minimal Config does not construct: %v", err)
	}
	t.Cleanup(ag.Stop)

	if err := ag.Registry().Register(docProcessList{}); err != nil {
		t.Fatalf("the document's tool does not register: %v", err)
	}

	// The trigger shape the document shows.
	_ = Trigger{
		Type: "chat_query",
		ID:   "req-1",
		Data: json.RawMessage(`{"query":"what is listening on this host?"}`),
	}
}
