package agent

import (
	"context"

	"github.com/Compdeep/kaiju/tokens"
)

// TokenCategoryFunc names the usage bucket a run's LLM spend is counted
// against.
//
// The built-in answer is deliberately domain-agnostic — interactive callers
// versus everything else — so the counter carries no host vocabulary. An
// application that wants its spend broken down its own way (by the kind of work
// rather than the lane) says so here, and gets back a bill it can read.
//
// Nil uses the built-in answer.
type TokenCategoryFunc func(t Trigger) string

// tagTokens marks ctx so every LLM call in this run is attributed to the right
// usage category. Called once at each Run* entry point; sub-stages (classifier,
// aggregator, reflection, …) inherit the tag through the same ctx.
func (a *Agent) tagTokens(ctx context.Context, t Trigger) context.Context {
	return tokens.WithCategory(ctx, a.tokenCategory(t))
}

/*
 * tokenCategory names the bucket this run's spend belongs to.
 * desc: The application's answer when it supplied one, otherwise the built-in
 *       lane split.
 * param: t - the trigger.
 * return: the bucket name.
 */
func (a *Agent) tokenCategory(t Trigger) string {
	if a != nil && a.tokenCategoryFn != nil {
		if c := a.tokenCategoryFn(t); c != "" {
			return c
		}
	}
	switch t.Type {
	case "chat_query", "api_query":
		return "chat"
	default:
		return "background"
	}
}
