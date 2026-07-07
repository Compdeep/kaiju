// Package tokens is a tiny in-memory counter for LLM token usage, attributed by
// two independent context tags: a category (what the work was — chat /
// background / preflight / …) and a principal (who it was for — an opaque caller
// id; for makeen, the JWT `sub`). It works because every LLM response already
// carries a token count and every call goes through one method (llm.Client.
// Complete), so accumulating is a single Add at that chokepoint.
//
// The counter is domain-agnostic: it never learns what a principal or category
// *means*. In-memory only — totals are since process start and reset on restart.
package tokens

import (
	"context"
	"sync"
)

type catKey struct{}
type prinKey struct{}

// WithCategory tags ctx so LLM token usage on calls made under it is attributed
// to category. Set once at an agent entry point; it propagates to every LLM call
// in that run.
func WithCategory(ctx context.Context, category string) context.Context {
	return context.WithValue(ctx, catKey{}, category)
}

// WithPrincipal tags ctx with the caller this work is on behalf of (opaque — the
// JWT `sub` for makeen). Set in the API/gateway boundary; propagates to every
// LLM call made under the same context (i.e. the synchronous request path).
func WithPrincipal(ctx context.Context, principal string) context.Context {
	return context.WithValue(ctx, prinKey{}, principal)
}

func categoryFrom(ctx context.Context) string {
	if c, ok := ctx.Value(catKey{}).(string); ok && c != "" {
		return c
	}
	return "other"
}

func principalFrom(ctx context.Context) string {
	if p, ok := ctx.Value(prinKey{}).(string); ok {
		return p
	}
	return ""
}

// CategoryFrom exposes the context's category tag (for LLM debug logging).
func CategoryFrom(ctx context.Context) string { return categoryFrom(ctx) }

type key struct {
	principal string
	category  string
}

var (
	mu     sync.Mutex
	counts = map[key]int64{}
	total  int64
)

// Add records n tokens against the (principal, category) tagged on ctx.
// Concurrency-safe. No-op for n <= 0.
func Add(ctx context.Context, n int) {
	if n <= 0 {
		return
	}
	k := key{principal: principalFrom(ctx), category: categoryFrom(ctx)}
	mu.Lock()
	counts[k] += int64(n)
	total += int64(n)
	mu.Unlock()
}

// Usage is one (principal, category) tally, for JSON reporting.
type Usage struct {
	Principal string `json:"principal"`
	Category  string `json:"category"`
	Tokens    int64  `json:"tokens"`
}

// Snapshot returns a copy of the per-(principal, category) tallies and the grand
// total since process start.
func Snapshot() ([]Usage, int64) {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Usage, 0, len(counts))
	for k, v := range counts {
		out = append(out, Usage{Principal: k.principal, Category: k.category, Tokens: v})
	}
	return out, total
}
