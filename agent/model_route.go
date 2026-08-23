package agent

import (
	"context"
	"strings"

	"github.com/Compdeep/kaiju/agent/llm"
)

// visionImagesKey carries a turn's uploaded images (base64 data: URIs or URLs)
// on the run context, set once at the API boundary.
type visionImagesKey struct{}

// WithVisionImages returns ctx carrying images to attach to heavy-lane LLM calls
// when the resolved model is vision-capable. No-op for an empty list.
func WithVisionImages(ctx context.Context, images []string) context.Context {
	if len(images) == 0 {
		return ctx
	}
	return context.WithValue(ctx, visionImagesKey{}, images)
}

func visionImagesFrom(ctx context.Context) []string {
	if v, ok := ctx.Value(visionImagesKey{}).([]string); ok {
		return v
	}
	return nil
}

// IsVisionModel reports whether a model id accepts image input. Kept as a
// pattern check here (the authoritative catalog lives in the api package; a
// dependency the agent core must not take).
func IsVisionModel(id string) bool {
	s := strings.ToLower(id)
	switch {
	case strings.Contains(s, "vl-"), strings.HasSuffix(s, "-vl"): // qwen-vl, …
		return true
	case strings.Contains(s, "gpt-4o"), strings.Contains(s, "gpt-4.1"):
		return true
	case strings.Contains(s, "claude-3"), strings.Contains(s, "claude-4"),
		strings.Contains(s, "claude-sonnet"), strings.Contains(s, "claude-opus"), strings.Contains(s, "claude-haiku"):
		return true
	case strings.Contains(s, "gemini"), strings.Contains(s, "llama-4"), strings.Contains(s, "grok-4"):
		return true
	}
	return false
}

/*
 * Per-request model routing.
 *
 * kaiju is a stateless REST engine; the host (makeen) owns policy — which
 * organization may use which model, and the default. Every LLM call in the
 * engine flows through one of two lanes:
 *
 *   heavy  — executive, aggregator, planner, validator, RCA, react (a.llm)
 *   light  — classify, route, preflight, reflect, observe, compact (a.executor)
 *
 * A request may name a Provider+Model for either lane. The selection rides the
 * run context (set once at the API boundary alongside the token principal) and
 * is read here at the call seam. When a lane is unselected we return the
 * configured default client and an empty model, so behaviour is byte-identical
 * to pre-routing kaiju. The KEYS live only in cfg.Providers → providerClients;
 * a request carries a selection, never a key.
 */

// ProviderCreds is one provider's routable credentials (see Config.Providers).
type ProviderCreds struct {
	Type     string // wire protocol: "openai" (default) | "anthropic"
	Endpoint string
	APIKey   string
}

// laneSelection is the per-request model choice for the lanes.
type laneSelection struct {
	heavyProvider, heavyModel   string
	lightProvider, lightModel   string
	answerProvider, answerModel string
}

type laneSelKey struct{}

// withLaneSelection tags ctx with the per-request model choice. Called once at
// the API boundary; propagates through SubmitSync into every LLM call.
func withLaneSelection(ctx context.Context, sel laneSelection) context.Context {
	if sel == (laneSelection{}) {
		return ctx
	}
	return context.WithValue(ctx, laneSelKey{}, sel)
}

// laneSelectionFromTrigger reads the per-request model choice off a Trigger.
func laneSelectionFromTrigger(t Trigger) laneSelection {
	return laneSelection{
		heavyProvider:  t.Provider,
		heavyModel:     t.Model,
		lightProvider:  t.ExecutorProvider,
		lightModel:     t.ExecutorModel,
		answerProvider: t.AnswerProvider,
		answerModel:    t.AnswerModel,
	}
}

func laneSelFrom(ctx context.Context) laneSelection {
	if sel, ok := ctx.Value(laneSelKey{}).(laneSelection); ok {
		return sel
	}
	return laneSelection{}
}

// heavyLane resolves the client+model for a heavy-lane call. Returns the
// selected provider client (and its model) when a valid heavy selection is
// present and configured; otherwise the default reasoning client with an empty
// model (the client's own default applies).
func (a *Agent) heavyLane(ctx context.Context) (*llm.Client, string) {
	sel := laneSelFrom(ctx)
	if sel.heavyProvider != "" && sel.heavyModel != "" {
		if c := a.providerClients[sel.heavyProvider]; c != nil {
			return c, sel.heavyModel
		}
	}
	return a.llm, ""
}

// lightLane is heavyLane's counterpart for the executor (light) lane.
func (a *Agent) lightLane(ctx context.Context) (*llm.Client, string) {
	sel := laneSelFrom(ctx)
	if sel.lightProvider != "" && sel.lightModel != "" {
		if c := a.providerClients[sel.lightProvider]; c != nil {
			return c, sel.lightModel
		}
	}
	return a.executor, ""
}

// answerLane resolves the client+model that writes the FINAL answer (aggregator +
// chat). Precedence: per-request answer selection → the pinned answer model in
// config → the heavy/reasoning lane (so an unset answer lane behaves exactly as
// before). Kept SEPARATE from the heavy lane so a user's answer model — which may
// be a thinking model — never drives the planner's forced tool calls.
func (a *Agent) answerLane(ctx context.Context) (*llm.Client, string) {
	sel := laneSelFrom(ctx)
	if sel.answerProvider != "" && sel.answerModel != "" {
		if c := a.providerClients[sel.answerProvider]; c != nil {
			return c, sel.answerModel
		}
	}
	if a.answerModel != "" {
		return a.clientFor(a.answerProvider), a.answerModel
	}
	return a.heavyLane(ctx)
}

// clientFor returns the connection for the named provider, or the default
// reasoning client when the name is empty or not configured. Shared by the
// simple provider-routed callers (OneShot, Converse). heavyLane/lightLane do
// NOT use it — they resolve a model too and default differently.
func (a *Agent) clientFor(provider string) *llm.Client {
	if provider != "" {
		if c := a.providerClients[provider]; c != nil {
			return c
		}
	}
	return a.llm
}

// The three named doors, kept as one-line shims onto ask so the call sites this
// migration has not reached yet keep working. Each was the same four steps
// written out again; ask is those steps written once.

func (a *Agent) completeHeavy(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	return a.ask(ctx, Heavy, req)
}

func (a *Agent) completeLight(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	return a.ask(ctx, Light, req)
}

func (a *Agent) completeHeavyChecked(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	return a.askParsed(ctx, Heavy, req)
}

func (a *Agent) completeLightChecked(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	return a.askParsed(ctx, Light, req)
}

// completeRoute makes the routing-classifier call. If a route model is pinned in
// config it uses that (a small capable model, e.g. gpt-5-mini) so the run-the-agent
// decision is reliable; otherwise it falls back to the executor (light) lane so
// the rest of the cheap background calls are unaffected.
func (a *Agent) completeRoute(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	return a.ask(ctx, Route, req)
}

// routeLane resolves the routing classifier's client and model. A pinned route
// model wins; otherwise the light lane answers, so pinning one small model for
// this decision leaves the rest of the cheap background calls alone.
func (a *Agent) routeLane(ctx context.Context) (*llm.Client, string) {
	if a.routeModel != "" {
		return a.clientFor(a.routeProvider), a.routeModel
	}
	return a.lightLane(ctx)
}

// OneShot runs a single provider-routed LLM completion with NO agent machinery —
// no preflight, planner, DAG, tools, reflection, or aggregator. It is the raw
// passthrough for hosts that need a plain completion (e.g. makeen's compliance
// LLM-detection stage). The provider selects one of the configured provider
// clients (falling back to the reasoning client); token counting still fires via
// Complete. Returns the assistant content and the total tokens used.
func (a *Agent) OneShot(ctx context.Context, provider, model string, messages []llm.Message, temperature float64, maxTokens int) (string, int, error) {
	client := a.clientFor(provider)
	oneShot := &llm.ChatRequest{
		Model:       model,
		Messages:    messages,
		Temperature: temperature,
		MaxTokens:   maxTokens,
	}
	resp, err := client.Complete(ctx, oneShot)
	if err != nil {
		return "", 0, err
	}
	if len(resp.Choices) == 0 {
		return "", resp.Usage.TotalTokens, nil
	}
	return resp.Choices[0].Message.Content, resp.Usage.TotalTokens, nil
}
