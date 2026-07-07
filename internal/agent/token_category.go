package agent

import (
	"context"

	"github.com/Compdeep/kaiju/internal/tokens"
)

// tagTokens marks ctx so every LLM call in this run is attributed to the right
// usage category. Called once at each Run* entry point; sub-stages (classifier,
// aggregator, reflection, …) inherit the tag through the same ctx.
func tagTokens(ctx context.Context, triggerType string) context.Context {
	return tokens.WithCategory(ctx, tokenCategory(triggerType))
}

// tokenCategory maps a trigger type to a generic execution-lane bucket. Kept
// domain-agnostic — interactive callers vs everything else — so the counter
// carries no host vocabulary.
func tokenCategory(triggerType string) string {
	switch triggerType {
	case "chat_query", "api_query":
		return "chat"
	default:
		return "background"
	}
}
