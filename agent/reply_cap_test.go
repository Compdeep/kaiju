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

// ── capReply: the safe defaults ───────────────────────────────────────────────

func TestCapReply_NoCatalogLeavesEveryRequestAlone(t *testing.T) {
	a := &Agent{}
	req := reqOf(4096, 100)
	a.capReply("openai/gpt-4.1", req)
	if req.MaxTokens != 4096 {
		t.Fatalf("want 4096 untouched, got %d", req.MaxTokens)
	}
}

func TestCapReply_ModelMissingFromTheCatalogLeavesRequestAlone(t *testing.T) {
	a := &Agent{cfg: Config{ModelConfig: ModelConfig{Limits: limitsFor("openai/gpt-4.1", 1047576, 32768)}}}
	req := reqOf(16384, 100)
	a.capReply("selfhosted/qwen3-32b", req)
	if req.MaxTokens != 16384 {
		t.Fatalf("an unknown model must keep its cap, got %d", req.MaxTokens)
	}
}

func TestCapReply_UnnamedModelLeavesRequestAlone(t *testing.T) {
	a := &Agent{cfg: Config{ModelConfig: ModelConfig{Limits: limitsOf(8000, 1000)}}}
	req := reqOf(4096, 100)
	a.capReply("", req)
	if req.MaxTokens != 4096 {
		t.Fatalf("no model name means no opinion, got %d", req.MaxTokens)
	}
}

func TestCapReply_HalfKnownLimitsUseTheHalfThatIsKnown(t *testing.T) {
	// A catalog entry with a window and no published maximum reply.
	a := &Agent{cfg: Config{ModelConfig: ModelConfig{Limits: limitsOf(8000, 0)}}}
	req := reqOf(6000, 4000) // 8000 - 1000 prompt - 2000 headroom = 5000 of room
	a.capReply("some/model", req)
	if req.MaxTokens != 5000 {
		t.Fatalf("want 5000 from the window alone, got %d", req.MaxTokens)
	}

	// And the reverse: a maximum reply with no window.
	b := &Agent{cfg: Config{ModelConfig: ModelConfig{Limits: limitsOf(0, 2048)}}}
	req2 := reqOf(6000, 4000)
	b.capReply("some/model", req2)
	if req2.MaxTokens != 2048 {
		t.Fatalf("want 2048 from the maximum reply alone, got %d", req2.MaxTokens)
	}
}

func TestCapReply_ZeroOrNegativeCapIsNotTouched(t *testing.T) {
	a := &Agent{cfg: Config{ModelConfig: ModelConfig{Limits: limitsOf(8000, 1000)}}}
	req := &llm.ChatRequest{MaxTokens: 0}
	a.capReply("some/model", req)
	if req.MaxTokens != 0 {
		t.Fatalf("a request that set no cap must keep none, got %d", req.MaxTokens)
	}
}

// ── capReply: the two ceilings ────────────────────────────────────────────────

func TestCapReply_LowersToTheModelsMaximumReply(t *testing.T) {
	a := &Agent{cfg: Config{ModelConfig: ModelConfig{Limits: limitsOf(200000, 8192)}}}
	req := reqOf(16384, 100)
	a.capReply("anthropic/claude-sonnet-5", req)
	if req.MaxTokens != 8192 {
		t.Fatalf("want 8192, got %d", req.MaxTokens)
	}
}

func TestCapReply_NeverRaisesWhatTheCallerAskedFor(t *testing.T) {
	a := &Agent{cfg: Config{ModelConfig: ModelConfig{Limits: limitsOf(200000, 64000)}}}
	req := reqOf(16, 100) // the router lane wants a tiny reply
	a.capReply("anthropic/claude-haiku-4.5", req)
	if req.MaxTokens != 16 {
		t.Fatalf("want 16 kept, got %d", req.MaxTokens)
	}
}

func TestCapReply_WindowBeatsMaxOutputWhenTighter(t *testing.T) {
	// 32k window, ~5k-token prompt: 32000-5000-2000 = 25000 of room, so the
	// model's 16k maximum reply is the binding limit.
	a := &Agent{cfg: Config{ModelConfig: ModelConfig{Limits: limitsOf(32000, 16000)}}}
	req := reqOf(20000, 20000)
	a.capReply("some/small-window", req)
	if req.MaxTokens != 16000 {
		t.Fatalf("want 16000, got %d", req.MaxTokens)
	}
}

func TestCapReply_MaxOutputBeatsWindowWhenTighter(t *testing.T) {
	// 32k window, ~30k-token prompt: 0 of room, so the floor applies rather
	// than the model's 16k maximum reply.
	a := &Agent{cfg: Config{ModelConfig: ModelConfig{Limits: limitsOf(32000, 16000)}}}
	req := reqOf(8192, 120000)
	a.capReply("some/small-window", req)
	if req.MaxTokens != replyFloor {
		t.Fatalf("want the floor %d, got %d", replyFloor, req.MaxTokens)
	}
}

// ── planMaxTokens ─────────────────────────────────────────────────────────────

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

	a := &Agent{
		llm: llm.NewClient(srv.URL, "", "anthropic/claude-sonnet-5"),
		cfg: Config{ModelConfig: ModelConfig{Limits: limitsFor("anthropic/claude-sonnet-5", 200000, 8192)}},
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
