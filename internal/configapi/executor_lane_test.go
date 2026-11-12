package configapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/internal/config"
)

// "Same as reasoning" clears the executor lane, and the running agent's
// executor then sends to the reasoning model, without a restart.
func TestClearingTheExecutorUsesTheReasoningModel(t *testing.T) {
	ag, err := agent.New(agent.Config{PathConfig: agent.PathConfig{
		Workspace: t.TempDir(), MetadataDir: t.TempDir(), DataDir: t.TempDir(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	c := config.Default()
	c.LLM.Provider, c.LLM.Model = "bedrock", "qwen.qwen3-32b+high"
	api := &API{cfg: c, agent: ag}
	patch := func(body string) {
		rec := httptest.NewRecorder()
		api.handleUpdateConfig(rec, httptest.NewRequest(http.MethodPatch, "/api/v1/config", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH %s = %d: %s", body, rec.Code, rec.Body.String())
		}
	}

	patch(`{"executor":{"provider":"openrouter","model":"qwen/qwen3.6-35b-a3b"}}`)
	if got := ag.ExecutorClient().Model(); got != "qwen/qwen3.6-35b-a3b" {
		t.Fatalf("executor sends to %q after choosing a model", got)
	}

	patch(`{"executor":{"provider":"","model":""}}`)
	if c.Executor.Provider != "" || c.Executor.Model != "" {
		t.Errorf("executor still %q / %q after same as reasoning", c.Executor.Provider, c.Executor.Model)
	}
	if got := ag.ExecutorClient().Model(); got != "qwen.qwen3-32b+high" {
		t.Errorf("executor sends to %q after same as reasoning, want the reasoning model", got)
	}
}

// Qwen3 32B on Bedrock carries the measured fact that it drops a strict
// schema when it thinks, so the engine sends its schema requests without.
func TestQwen3OnBedrockDropsTheSchemaWhenThinking(t *testing.T) {
	f, ok := Facts("qwen.qwen3-32b+high")
	if !ok || !f.Thinking.DropsSchema {
		t.Errorf("facts = %+v (%v), want DropsSchema", f.Thinking, ok)
	}
	if f, _ := Facts("qwen.qwen3-next-80b-a3b-instruct"); f.Thinking.DropsSchema {
		t.Error("Qwen3 Next 80B keeps the schema at every effort, measured")
	}
}
