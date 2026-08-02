package llm

import (
	"context"
	"sync"
)

// CallObserver is notified after every LLM call this package makes, whether it
// succeeded or failed. It exists so an embedding application can record calls —
// prompt logging, tracing, cost attribution, replay capture — without this
// package knowing what is done with them.
//
// "Call" means one request/response pair, not one conversation: a single
// investigation makes many, and each fires the observer once.
//
// resp is the raw response, so the observer decides for itself what is
// interesting — reply text, tool calls, token counts, finish reason. It is nil
// when err is non-nil, and callers must treat it as possibly nil regardless.
// req is the request as sent, and never carries the API key: that lives on the
// Client and travels as an Authorization header.
//
// The observer runs inline on the calling goroutine, so a slow one slows every
// LLM call. Keep it to a channel send or a cheap write.
type CallObserver func(ctx context.Context, req *ChatRequest, resp *ChatResponse, err error)

var (
	observerMu sync.RWMutex
	observer   CallObserver
)

// SetCallObserver installs the process-wide observer. Pass nil to disable.
//
// Deliberately package-scoped rather than a field on Client. Clients are
// constructed in several places and some are created at runtime (a dashboard
// settings change builds a fresh one), so a per-client hook would have to be
// attached at every site and would silently miss any site that forgot — which
// is the exact failure this is meant to prevent. One call covers every client,
// present and future.
//
// Call it once during startup, before any client is used. It is guarded for
// safety, but swapping it while calls are in flight means some calls go to the
// old observer and some to the new, which no caller should rely on.
func SetCallObserver(fn CallObserver) {
	observerMu.Lock()
	observer = fn
	observerMu.Unlock()
}

// emitCall notifies the observer, if one is installed. Near-zero cost when
// none is (one RWMutex read and a nil check), so it is safe on the hot path.
func emitCall(ctx context.Context, req *ChatRequest, resp *ChatResponse, err error) {
	observerMu.RLock()
	fn := observer
	observerMu.RUnlock()
	if fn == nil || req == nil {
		return
	}
	fn(ctx, req, resp, err)
}
