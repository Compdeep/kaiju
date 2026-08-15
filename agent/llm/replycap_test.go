package llm

import (
	"strings"
	"testing"
)

// Sizing a reply against the model, which every send now does.
//
// The safe defaults matter more than the arithmetic. A client with no catalog,
// or a model the catalog does not carry, must leave a request exactly as its
// caller wrote it — anything else would change every call in an application
// that supplies no limits at all.

func clientFor(model string, fn ModelLimits) *Client {
	return NewClient("", "", model).Limits(fn)
}

func limitsOf(contextTokens, maxOutput int) ModelLimits {
	return func(string) (int, int) { return contextTokens, maxOutput }
}

func reqOf(maxTokens, promptChars int) *ChatRequest {
	return &ChatRequest{
		Messages:  []Message{{Role: "system", Content: strings.Repeat("x", promptChars)}},
		MaxTokens: maxTokens,
	}
}

// ── the safe defaults ────────────────────────────────────────────────────────

func TestCapReply_NoCatalogLeavesEveryRequestAlone(t *testing.T) {
	req := reqOf(4096, 100)
	clientFor("openai/gpt-4.1", nil).capReply(req)
	if req.MaxTokens != 4096 {
		t.Fatalf("MaxTokens = %d, want 4096 untouched", req.MaxTokens)
	}
}

func TestCapReply_ModelMissingFromTheCatalogLeavesRequestAlone(t *testing.T) {
	known := func(m string) (int, int) {
		if m == "openai/gpt-4.1" {
			return 8000, 1000
		}
		return 0, 0
	}
	req := reqOf(16384, 100)
	clientFor("selfhosted/qwen3-32b", known).capReply(req)
	if req.MaxTokens != 16384 {
		t.Fatalf("MaxTokens = %d; a model the catalog does not carry keeps its cap", req.MaxTokens)
	}
}

func TestCapReply_UnnamedModelLeavesRequestAlone(t *testing.T) {
	req := reqOf(4096, 100)
	clientFor("", limitsOf(8000, 1000)).capReply(req)
	if req.MaxTokens != 4096 {
		t.Fatalf("MaxTokens = %d; no model name means no opinion", req.MaxTokens)
	}
}

func TestCapReply_ZeroOrNegativeCapIsNotTouched(t *testing.T) {
	for _, cap := range []int{0, -1} {
		req := reqOf(cap, 100)
		clientFor("openai/gpt-4.1", limitsOf(8000, 512)).capReply(req)
		if req.MaxTokens != cap {
			t.Errorf("MaxTokens = %d, want %d — no cap means the provider's own default", req.MaxTokens, cap)
		}
	}
}

// ── which ceiling wins ───────────────────────────────────────────────────────

func TestCapReply_LowersToTheModelsMaximumReply(t *testing.T) {
	req := reqOf(4096, 100)
	clientFor("openai/gpt-4.1", limitsOf(128000, 512)).capReply(req)
	if req.MaxTokens != 512 {
		t.Fatalf("MaxTokens = %d, want 512 — the model's published maximum", req.MaxTokens)
	}
}

func TestCapReply_NeverRaisesWhatTheCallerAskedFor(t *testing.T) {
	req := reqOf(256, 100)
	clientFor("openai/gpt-4.1", limitsOf(128000, 16384)).capReply(req)
	if req.MaxTokens != 256 {
		t.Fatalf("MaxTokens = %d; a caller that wants a short reply keeps getting one", req.MaxTokens)
	}
}

func TestCapReply_WindowBeatsMaxOutputWhenTighter(t *testing.T) {
	// 8000 window − 1000 prompt − 2000 headroom = 5000 of room, under the 8192
	// the model would otherwise allow.
	req := reqOf(8192, 4000)
	clientFor("openai/gpt-4.1", limitsOf(8000, 8192)).capReply(req)
	if req.MaxTokens != 5000 {
		t.Fatalf("MaxTokens = %d, want 5000 — what the window leaves after the prompt", req.MaxTokens)
	}
}

func TestCapReply_MaxOutputBeatsWindowWhenTighter(t *testing.T) {
	req := reqOf(8192, 100)
	clientFor("openai/gpt-4.1", limitsOf(128000, 2048)).capReply(req)
	if req.MaxTokens != 2048 {
		t.Fatalf("MaxTokens = %d, want 2048 — the model's own maximum", req.MaxTokens)
	}
}

func TestCapReply_HalfKnownLimitsUseTheHalfThatIsKnown(t *testing.T) {
	// A window and no published maximum reply.
	req := reqOf(6000, 4000)
	clientFor("some/model", limitsOf(8000, 0)).capReply(req)
	if req.MaxTokens != 5000 {
		t.Fatalf("MaxTokens = %d, want 5000 from the window alone", req.MaxTokens)
	}

	// And the reverse.
	req = reqOf(6000, 100)
	clientFor("some/model", limitsOf(0, 2048)).capReply(req)
	if req.MaxTokens != 2048 {
		t.Fatalf("MaxTokens = %d, want 2048 from the maximum reply alone", req.MaxTokens)
	}
}

// A prompt that nearly fills the window would compute a cap of a few tokens,
// which fails in a way that looks like the model refusing to answer.
func TestCapReply_NeverSettlesBelowTheFloor(t *testing.T) {
	req := reqOf(4096, 30000) // ~7500 tokens of prompt against an 8000 window
	clientFor("openai/gpt-4.1", limitsOf(8000, 4096)).capReply(req)
	if req.MaxTokens != replyFloor {
		t.Fatalf("MaxTokens = %d, want the floor of %d", req.MaxTokens, replyFloor)
	}
}

// The request's own model wins over the client's default, because a routed call
// stamps the model it is actually reaching.
func TestCapReply_UsesTheRequestsModelOverTheClients(t *testing.T) {
	perModel := func(m string) (int, int) {
		if m == "routed/model" {
			return 128000, 512
		}
		return 128000, 8192
	}
	req := reqOf(4096, 100)
	req.Model = "routed/model"
	clientFor("client/default", perModel).capReply(req)
	if req.MaxTokens != 512 {
		t.Fatalf("MaxTokens = %d, want 512 — the limits of the model actually being called", req.MaxTokens)
	}
}

// A caller that has to tell the model its budget needs the number before the
// request goes out. Asking must not change the request, and sending must apply
// the same answer — otherwise the number stated and the number enforced differ,
// which is worse than saying nothing.
func TestReplyCapAnswersWithoutChangingTheRequest(t *testing.T) {
	c := clientFor("openai/gpt-4.1", limitsOf(128000, 512))
	req := reqOf(4096, 100)

	got := c.ReplyCap(req)
	if got != 512 {
		t.Errorf("ReplyCap = %d, want 512", got)
	}
	if req.MaxTokens != 4096 {
		t.Errorf("asking changed the request: MaxTokens = %d, want it untouched", req.MaxTokens)
	}

	c.capReply(req)
	if req.MaxTokens != got {
		t.Errorf("sending applied %d where asking said %d; a stated budget must be "+
			"the one the provider stops at", req.MaxTokens, got)
	}
}
