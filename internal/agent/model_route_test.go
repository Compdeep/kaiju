package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Compdeep/kaiju/internal/agent/llm"
)

// captureServer is a fake OpenAI-compatible endpoint that records the model id
// from each request body and returns a minimal valid completion.
func captureServer(t *testing.T, hits *int, lastModel *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		*hits++
		*lastModel = req.Model
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"total_tokens":1}}`)
	}))
}

// TestCompleteHeavyRoutesOverHTTP proves the full wire: a heavy-lane selection
// sends the chosen model to the chosen provider's endpoint, and an unselected
// call hits the default client instead.
func TestCompleteHeavyRoutesOverHTTP(t *testing.T) {
	var defHits, oaiHits int
	var defModel, oaiModel string
	defSrv := captureServer(t, &defHits, &defModel)
	defer defSrv.Close()
	oaiSrv := captureServer(t, &oaiHits, &oaiModel)
	defer oaiSrv.Close()

	a := &Agent{
		llm: llm.NewClient(defSrv.URL, "k", "default-heavy"),
		providerClients: map[string]*llm.Client{
			"openai": llm.NewClientWithProvider("openai", oaiSrv.URL, "k", ""),
		},
	}
	msg := []llm.Message{{Role: "user", Content: "hi"}}

	// Selected → routes to openai endpoint with the selected model.
	ctx := withLaneSelection(context.Background(), laneSelection{heavyProvider: "openai", heavyModel: "gpt-4o-mini"})
	if _, err := a.completeHeavy(ctx, &llm.ChatRequest{Messages: msg}); err != nil {
		t.Fatalf("completeHeavy routed: %v", err)
	}
	if oaiHits != 1 || oaiModel != "gpt-4o-mini" {
		t.Fatalf("expected openai endpoint hit with gpt-4o-mini, got hits=%d model=%q", oaiHits, oaiModel)
	}
	if defHits != 0 {
		t.Fatalf("default endpoint should not be hit when routed, got %d", defHits)
	}

	// Unselected → default client with its own baked-in model.
	if _, err := a.completeHeavy(context.Background(), &llm.ChatRequest{Messages: msg}); err != nil {
		t.Fatalf("completeHeavy default: %v", err)
	}
	if defHits != 1 || defModel != "default-heavy" {
		t.Fatalf("expected default endpoint hit with default-heavy, got hits=%d model=%q", defHits, defModel)
	}
}

// newRouteAgent builds a minimal Agent wired only for lane resolution — no
// network, no config. The clients are distinct pointers so we can assert which
// one a lane resolves to by identity.
func newRouteAgent() (*Agent, *llm.Client, *llm.Client, *llm.Client, *llm.Client) {
	defLLM := llm.NewClient("", "", "default-heavy")
	defExec := llm.NewClient("", "", "default-light")
	oai := llm.NewClientWithProvider("openai", "", "k", "")
	anth := llm.NewClientWithProvider("anthropic", "", "k", "")
	a := &Agent{
		llm:      defLLM,
		executor: defExec,
		providerClients: map[string]*llm.Client{
			"openai":    oai,
			"anthropic": anth,
		},
	}
	return a, defLLM, defExec, oai, anth
}

func TestLaneSelectionRoundTrip(t *testing.T) {
	sel := laneSelection{heavyProvider: "openai", heavyModel: "gpt-4o", lightProvider: "anthropic", lightModel: "claude-haiku"}
	ctx := withLaneSelection(context.Background(), sel)
	if got := laneSelFrom(ctx); got != sel {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, sel)
	}
	// Empty selection must not tag the ctx.
	ctx2 := withLaneSelection(context.Background(), laneSelection{})
	if got := laneSelFrom(ctx2); got != (laneSelection{}) {
		t.Fatalf("empty selection should be zero, got %+v", got)
	}
}

func TestHeavyLaneDefaultWhenUnselected(t *testing.T) {
	a, defLLM, _, _, _ := newRouteAgent()
	c, m := a.heavyLane(context.Background())
	if c != defLLM || m != "" {
		t.Fatalf("unselected heavy lane should be default client + empty model, got %v %q", c == defLLM, m)
	}
}

func TestHeavyLaneRoutesToSelectedProvider(t *testing.T) {
	a, _, _, oai, _ := newRouteAgent()
	ctx := withLaneSelection(context.Background(), laneSelection{heavyProvider: "openai", heavyModel: "gpt-4o"})
	c, m := a.heavyLane(ctx)
	if c != oai || m != "gpt-4o" {
		t.Fatalf("heavy lane should route to openai client + gpt-4o, got match=%v model=%q", c == oai, m)
	}
}

func TestLightLaneIndependentOfHeavy(t *testing.T) {
	a, _, defExec, _, anth := newRouteAgent()
	// Heavy selected, light not → light stays default.
	ctx := withLaneSelection(context.Background(), laneSelection{heavyProvider: "openai", heavyModel: "gpt-4o"})
	if c, m := a.lightLane(ctx); c != defExec || m != "" {
		t.Fatalf("light lane should be default when only heavy selected, got match=%v model=%q", c == defExec, m)
	}
	// Light selected to anthropic.
	ctx = withLaneSelection(context.Background(), laneSelection{lightProvider: "anthropic", lightModel: "claude-haiku"})
	if c, m := a.lightLane(ctx); c != anth || m != "claude-haiku" {
		t.Fatalf("light lane should route to anthropic, got match=%v model=%q", c == anth, m)
	}
}

func TestLaneFallsBackWhenProviderUnconfigured(t *testing.T) {
	a, defLLM, _, _, _ := newRouteAgent()
	// "google" isn't in providerClients → fall back to default, ignore model.
	ctx := withLaneSelection(context.Background(), laneSelection{heavyProvider: "google", heavyModel: "gemini"})
	if c, m := a.heavyLane(ctx); c != defLLM || m != "" {
		t.Fatalf("unconfigured provider should fall back to default, got match=%v model=%q", c == defLLM, m)
	}
}

func TestLaneRequiresBothProviderAndModel(t *testing.T) {
	a, defLLM, _, _, _ := newRouteAgent()
	// Provider without a model is incomplete → default (avoids empty req.Model
	// against a provider client that has no baked-in default).
	ctx := withLaneSelection(context.Background(), laneSelection{heavyProvider: "openai"})
	if c, _ := a.heavyLane(ctx); c != defLLM {
		t.Fatalf("provider without model should fall back to default")
	}
}

func TestLaneSelectionFromTrigger(t *testing.T) {
	tr := Trigger{Provider: "openai", Model: "gpt-4o", ExecutorProvider: "anthropic", ExecutorModel: "claude-haiku"}
	got := laneSelectionFromTrigger(tr)
	want := laneSelection{heavyProvider: "openai", heavyModel: "gpt-4o", lightProvider: "anthropic", lightModel: "claude-haiku"}
	if got != want {
		t.Fatalf("trigger→selection mismatch: got %+v want %+v", got, want)
	}
}
