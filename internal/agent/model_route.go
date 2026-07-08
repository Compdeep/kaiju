package agent

import (
	"context"

	"github.com/Compdeep/kaiju/internal/agent/llm"
)

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

// laneSelection is the per-request model choice for both lanes.
type laneSelection struct {
	heavyProvider, heavyModel string
	lightProvider, lightModel string
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
		heavyProvider: t.Provider,
		heavyModel:    t.Model,
		lightProvider: t.ExecutorProvider,
		lightModel:    t.ExecutorModel,
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

// completeHeavy runs a heavy-lane completion through the routed client. Drop-in
// for a.llm.Complete. A non-empty routed model overrides req.Model so the
// selected provider gets its own model id (kaiju's internal default would not
// exist there); the default lane leaves req.Model untouched.
func (a *Agent) completeHeavy(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	c, model := a.heavyLane(ctx)
	if model != "" {
		req.Model = model
	}
	return c.Complete(ctx, req)
}

// completeLight is completeHeavy's counterpart for the executor lane. Drop-in
// for a.executor.Complete.
func (a *Agent) completeLight(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	c, model := a.lightLane(ctx)
	if model != "" {
		req.Model = model
	}
	return c.Complete(ctx, req)
}
