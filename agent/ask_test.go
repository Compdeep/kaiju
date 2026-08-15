package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The door every model call goes through.
//
// What it has to do is small and the order matters: resolve the lane, stamp its
// model, size the reply against that model, send. Sizing measures the prompt and
// looks the limits up by model id, so it cannot run before the model is known.
//
// Each of these was applied at the call site before, so each was applied
// differently or not at all. These check the door does them, and that a lane
// reaches the model it names.

// laneModel returns the model id a lane would stamp, which is the observable
// half of resolving a lane.
func laneModel(t *testing.T, a *Agent, ctx context.Context, l Lane) string {
	t.Helper()
	_, model := a.lane(ctx, l)
	return model
}

func TestEachLaneReachesItsOwnModel(t *testing.T) {
	a := &Agent{
		llm:      llm.NewClient("", "", "reasoning-model"),
		executor: llm.NewClient("", "", "executor-model"),
	}

	// With nothing pinned and no per-request selection, each lane returns its
	// client's own default, which it signals by naming no model.
	for _, l := range []Lane{Heavy, Light, Route, Answer} {
		c, model := a.lane(context.Background(), l)
		if c == nil {
			t.Errorf("%s lane resolved no client", l)
		}
		if model != "" {
			t.Errorf("%s lane named %q with nothing pinned; empty means the client's own default", l, model)
		}
	}

	// Heavy and Answer are the same client until an answer model is pinned, and
	// Route and Light are the same until a route model is. That is the
	// fallback each is documented to have.
	if hc, _ := a.lane(context.Background(), Heavy); hc != a.llm {
		t.Error("the heavy lane is not the reasoning client")
	}
	if lc, _ := a.lane(context.Background(), Light); lc != a.executor {
		t.Error("the light lane is not the executor client")
	}
	if rc, _ := a.lane(context.Background(), Route); rc != a.executor {
		t.Error("with no route model pinned, the route lane should fall back to light")
	}
	if ac, _ := a.lane(context.Background(), Answer); ac != a.llm {
		t.Error("with no answer model pinned, the answer lane should fall back to heavy")
	}
}

func TestAPinnedRouteModelTakesTheRouteLaneOffLight(t *testing.T) {
	a := &Agent{
		llm:        llm.NewClient("", "", "reasoning-model"),
		executor:   llm.NewClient("", "", "executor-model"),
		routeModel: "small-model",
	}

	if got := laneModel(t, a, context.Background(), Route); got != "small-model" {
		t.Errorf("route lane model = %q, want the pinned one", got)
	}
	// And pinning it leaves the cheap background calls where they were, which is
	// the reason the route lane exists separately at all.
	if got := laneModel(t, a, context.Background(), Light); got != "" {
		t.Errorf("light lane model = %q; pinning a route model must not move it", got)
	}
}

func TestAskStampsTheLanesModelBeforeSizingTheReply(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{"": {Content: "an answer"}})
	a := agentOnStub(t, model)

	// A window smaller than the request's cap, reported only for the model the
	// answer lane resolves to. If ask sized before stamping, the lookup would
	// miss and the cap would stand.
	a.cfg.Limits = func(m string) (int, int) {
		if m == a.executor.Model() {
			return 4096, 512
		}
		return 0, 0
	}

	req := &llm.ChatRequest{
		Messages:  []llm.Message{{Role: "user", Content: "hello"}},
		MaxTokens: 8192,
	}
	if _, err := a.ask(context.Background(), Light, req); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if req.MaxTokens != 512 {
		t.Errorf("MaxTokens = %d, want 512 — the model's published maximum reply. "+
			"The cap is looked up by model id, so it only works if the lane's model "+
			"is stamped on the request first", req.MaxTokens)
	}
}

func TestAskLeavesARequestAloneWhenTheModelIsUnknown(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{"": {Content: "an answer"}})
	a := agentOnStub(t, model)
	a.cfg.Limits = func(string) (int, int) { return 0, 0 }

	req := &llm.ChatRequest{
		Messages:  []llm.Message{{Role: "user", Content: "hello"}},
		MaxTokens: 8192,
	}
	if _, err := a.ask(context.Background(), Light, req); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if req.MaxTokens != 8192 {
		t.Errorf("MaxTokens = %d; a model the catalog does not carry must be left "+
			"exactly as the caller asked", req.MaxTokens)
	}
}

// askParsed is the same door with one more question asked of the reply.
func TestAskParsedReportsACutReplyAndAskDoesNot(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{"": {Content: "half an ans", Cut: true}})
	a := agentOnStub(t, model)
	newReq := func() *llm.ChatRequest {
		return &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hello"}}, MaxTokens: 64}
	}

	if _, err := a.askParsed(context.Background(), Light, newReq()); !errors.Is(err, llm.ErrReplyTruncated) {
		t.Errorf("askParsed = %v; a caller that parses has to be told the reply was cut", err)
	}

	resp, err := a.ask(context.Background(), Light, newReq())
	if err != nil {
		t.Fatalf("ask = %v; a cut prose reply is short, not an error", err)
	}
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		t.Error("the partial answer was dropped; short beats nothing")
	}
}

// The three named doors are shims now, and must behave exactly as the door they
// wrap — the call sites this migration has not reached yet depend on it.
func TestTheNamedDoorsAreTheSameDoor(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{"": {Content: "an answer"}})
	a := agentOnStub(t, model)
	a.cfg.Limits = func(string) (int, int) { return 4096, 512 }

	for _, c := range []struct {
		name string
		call func(*llm.ChatRequest) (*llm.ChatResponse, error)
	}{
		{"completeHeavy", func(r *llm.ChatRequest) (*llm.ChatResponse, error) {
			return a.completeHeavy(context.Background(), r)
		}},
		{"completeLight", func(r *llm.ChatRequest) (*llm.ChatResponse, error) {
			return a.completeLight(context.Background(), r)
		}},
		{"completeRoute", func(r *llm.ChatRequest) (*llm.ChatResponse, error) {
			return a.completeRoute(context.Background(), r)
		}},
	} {
		req := &llm.ChatRequest{
			Messages:  []llm.Message{{Role: "user", Content: "hello"}},
			MaxTokens: 8192,
		}
		if _, err := c.call(req); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if req.MaxTokens != 512 {
			t.Errorf("%s left MaxTokens at %d; it no longer goes through the door", c.name, req.MaxTokens)
		}
	}
}
