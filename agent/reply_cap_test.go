package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// limitsOf answers every model with the same pair, which is what most of these
// tests need. limitsFor answers one id and treats the rest as unknown.
func limitsOf(contextTokens, maxOutput int) ModelLimits {
	return func(string) (int, int) { return contextTokens, maxOutput }
}

func limitsFor(id string, contextTokens, maxOutput int) ModelLimits {
	return func(m string) (int, int) {
		if m == id {
			return contextTokens, maxOutput
		}
		return 0, 0
	}
}

func reqOf(maxTokens, promptChars int) *llm.ChatRequest {
	return &llm.ChatRequest{
		Messages:  []llm.Message{{Role: "system", Content: strings.Repeat("x", promptChars)}},
		MaxTokens: maxTokens,
	}
}

// ── planMaxTokens ────────────────────────────────────────────────────────────

// planAgent is an agent configured for one question: how big may a plan be.
func planAgent(maxNodes, maxTokens int, limits ModelLimits) *Agent {
	return &Agent{
		llm: llm.NewClient("http://example.invalid", "", "some/model"),
		cfg: Config{
			ModelConfig: ModelConfig{MaxTokens: maxTokens, Limits: limits},
			DAGConfig:   DAGConfig{MaxNodes: maxNodes},
		},
	}
}

func TestPlanMaxTokens_SmallBudgetKeepsTheConfiguredCap(t *testing.T) {
	// 30 steps needs 2200, which already fits in 4096.
	a := planAgent(30, 4096, limitsOf(200000, 64000))
	if got := a.planMaxTokens(context.Background()); got != 4096 {
		t.Fatalf("want the configured 4096, got %d", got)
	}
}

func TestPlanMaxTokens_RaisesForAModelThatCanTakeIt(t *testing.T) {
	// 100 steps is what the prompt tells the planner it may write, and 4096 is
	// not enough to write them.
	a := planAgent(100, 4096, limitsOf(200000, 64000))
	want := 100*stepTokens + planOverhead
	if got := a.planMaxTokens(context.Background()); got != want {
		t.Fatalf("want %d, got %d", want, got)
	}
}

func TestPlanMaxTokens_UnknownModelKeepsTheConfiguredCap(t *testing.T) {
	// The safe default. Raising max_tokens past a provider's own maximum is
	// rejected by some providers, so an unknown model must not be raised.
	a := planAgent(100, 4096, limitsFor("openai/gpt-4.1", 1047576, 32768))
	if got := a.planMaxTokens(context.Background()); got != 4096 {
		t.Fatalf("an unknown model must keep 4096, got %d", got)
	}
}

func TestPlanMaxTokens_NoCatalogKeepsTheConfiguredCap(t *testing.T) {
	a := planAgent(100, 4096, nil)
	if got := a.planMaxTokens(context.Background()); got != 4096 {
		t.Fatalf("no catalog must keep 4096, got %d", got)
	}
}

func TestPlanMaxTokens_ClampsToASmallMaxOutput(t *testing.T) {
	// The budget wants 5000; the model tops out at 3000.
	a := planAgent(100, 4096, limitsOf(32000, 3000))
	if got := a.planMaxTokens(context.Background()); got != 3000 {
		t.Fatalf("want 3000, got %d", got)
	}
}

// ── resolvedModel ─────────────────────────────────────────────────────────────

func TestResolvedModel_FallsBackToTheClientsOwnModel(t *testing.T) {
	c := llm.NewClient("http://example.invalid", "", "configured/model")
	if got := resolvedModel("", c); got != "configured/model" {
		t.Fatalf("want the client's model, got %q", got)
	}
	if got := resolvedModel("lane/model", c); got != "lane/model" {
		t.Fatalf("a lane selection must win, got %q", got)
	}
	if got := resolvedModel("", nil); got != "" {
		t.Fatalf("no client means no model, got %q", got)
	}
}

// ── the whole path ────────────────────────────────────────────────────────────

// TestCompleteHeavy_CapsAgainstTheModel drives the real call seam: the cap has
// to be lowered by the time the request reaches the wire, without the caller
// naming a model. This is the case that was silently doing nothing while
// capReply read req.Model, which the client fills in only later.
func TestCompleteHeavy_CapsAgainstTheModel(t *testing.T) {
	var seen struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	limits := limitsFor("anthropic/claude-sonnet-5", 200000, 8192)
	a := &Agent{
		llm: llm.NewClient(srv.URL, "", "anthropic/claude-sonnet-5").Limits(limits),
		cfg: Config{ModelConfig: ModelConfig{Limits: limits}},
	}

	_, err := a.completeHeavy(context.Background(), reqOf(64000, 400))
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if seen.Model != "anthropic/claude-sonnet-5" {
		t.Fatalf("model did not reach the wire: %q", seen.Model)
	}
	if seen.MaxTokens != 8192 {
		t.Fatalf("want max_tokens 8192 on the wire, got %d", seen.MaxTokens)
	}
}

func TestCompleteHeavy_UnknownModelSendsTheOriginalCap(t *testing.T) {
	var seen struct {
		MaxTokens int `json:"max_tokens"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	a := &Agent{
		llm: llm.NewClient(srv.URL, "", "selfhosted/qwen3-32b"),
		cfg: Config{ModelConfig: ModelConfig{Limits: limitsFor("openai/gpt-4.1", 1047576, 32768)}},
	}
	if _, err := a.completeHeavy(context.Background(), reqOf(16384, 400)); err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if seen.MaxTokens != 16384 {
		t.Fatalf("an unknown model must reach the wire uncapped, got %d", seen.MaxTokens)
	}
}

// Config.Limits is only worth anything if it reaches the clients that send.
//
// The catalog lives on Config and the sizing lives on the client, so the client
// is built with it — every construction in this package says .Limits(cfg.Limits)
// in the expression that makes the client. If one stops, its calls are sized
// against nothing and nothing else says so.
func TestNewGivesEveryClientTheCatalog(t *testing.T) {
	asked := map[string]bool{}
	a, err := New(Config{
		ModelConfig: ModelConfig{
			LLMEndpoint:   "http://example.invalid",
			LLMModel:      "reasoning/model",
			ExecutorModel: "executor/model",
			MaxTokens:     4096,
			Limits: func(m string) (int, int) {
				asked[m] = true
				return 128000, 512
			},
		},
		PathConfig: PathConfig{DataDir: t.TempDir(), Workspace: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Sizing a request on each client asks the catalog, which is the observable
	// proof that the client holds it.
	for name, c := range map[string]*llm.Client{"reasoning": a.llm, "executor": a.executor} {
		if c == nil {
			continue
		}
		req := &llm.ChatRequest{
			Messages:  []llm.Message{{Role: "user", Content: "hello"}},
			MaxTokens: 4096,
		}
		if _, err := c.Complete(context.Background(), req); err == nil {
			t.Logf("%s client reached its endpoint, which this test does not need", name)
		}
		if req.MaxTokens != 512 {
			t.Errorf("the %s client sent max_tokens %d; it has no catalog, so every "+
				"call it makes is sized against nothing", name, req.MaxTokens)
		}
	}
	if len(asked) == 0 {
		t.Error("no client asked the catalog anything")
	}
}
