package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// light lane resolves to. The client is what sizes, and it is given its
	// catalog when it is built — so a test changes the client, not the config.
	a.executor.Limits(func(m string) (int, int) {
		if m == a.executor.Model() {
			return 4096, 512
		}
		return 0, 0
	})

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
	a.executor.Limits(func(string) (int, int) { return 0, 0 })

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
	small := func(string) (int, int) { return 4096, 512 }
	a.llm.Limits(small)
	a.executor.Limits(small)

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

// ── the stated budget ────────────────────────────────────────────────────────

// The model is never shown max_tokens — the provider counts and stops. So a
// model writes to its own sense of length and is cut wherever that lands. The
// only channel is the prompt, and the only moment the number is final is after
// the lane resolves and the cap settles.
func TestTheModelIsToldTheBudgetItWillBeCutAt(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{"": {Content: "an answer"}})
	a := agentOnStub(t, model)
	a.executor.Limits(func(string) (int, int) { return 128000, 2048 })

	req := &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "you are a helpful thing"},
			{Role: "user", Content: "hello"},
		},
		MaxTokens: 8192,
	}
	if _, err := a.ask(context.Background(), Light, req); err != nil {
		t.Fatalf("ask: %v", err)
	}

	if req.MaxTokens != 2048 {
		t.Fatalf("MaxTokens = %d, want the model's 2048", req.MaxTokens)
	}
	if !strings.Contains(req.Messages[0].Content, "2048 tokens") {
		t.Errorf("the system message does not state the budget:\n%s", req.Messages[0].Content)
	}
	if !strings.Contains(req.Messages[0].Content, "you are a helpful thing") {
		t.Error("stating the budget replaced the prompt instead of extending it")
	}
	if req.Messages[1].Content != "hello" {
		t.Error("the user message was edited; only the system message carries this")
	}
}

// The number stated has to be the number enforced. A prompt saying 2048 while
// the provider stops at 512 is worse than saying nothing.
func TestTheStatedBudgetIsTheOneSent(t *testing.T) {
	var sent struct {
		MaxTokens int           `json:"max_tokens"`
		Messages  []llm.Message `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	a := &Agent{llm: llm.NewClient(srv.URL, "", "some/model").Limits(limitsOf(128000, 700))}
	_, err := a.ask(context.Background(), Heavy, &llm.ChatRequest{
		Messages:  []llm.Message{{Role: "system", Content: "do the thing"}},
		MaxTokens: 8192,
	})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if sent.MaxTokens != 700 {
		t.Fatalf("max_tokens on the wire = %d, want 700", sent.MaxTokens)
	}
	if len(sent.Messages) == 0 || !strings.Contains(sent.Messages[0].Content, "700 tokens") {
		t.Errorf("the prompt on the wire does not state 700:\n%+v", sent.Messages)
	}
}

// The planner builds its retry from the same message slice as its first
// attempt, so the line has to be recognised and not repeated.
func TestTheBudgetIsStatedOnceHoweverOftenTheRequestIsSent(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{"": {Content: "an answer"}})
	a := agentOnStub(t, model)
	a.executor.Limits(func(string) (int, int) { return 128000, 2048 })

	req := &llm.ChatRequest{
		Messages:  []llm.Message{{Role: "system", Content: "you are a helpful thing"}},
		MaxTokens: 8192,
	}
	for range 3 {
		if _, err := a.ask(context.Background(), Light, req); err != nil {
			t.Fatalf("ask: %v", err)
		}
	}
	if n := strings.Count(req.Messages[0].Content, budgetMarker); n != 1 {
		t.Errorf("the budget is stated %d times; a retry reuses the messages it was "+
			"built from, so this has to be recognised rather than appended again", n)
	}
}

// A caller that wants a token or two — a forced route() call takes 16 — gets no
// sentence, because the sentence would be larger than the budget it describes.
func TestASmallBudgetIsNotWorthStating(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{"": {Content: "chat"}})
	a := agentOnStub(t, model)
	a.executor.Limits(func(string) (int, int) { return 128000, 128000 })

	req := &llm.ChatRequest{
		Messages:  []llm.Message{{Role: "system", Content: "classify this"}},
		MaxTokens: 16,
	}
	if _, err := a.ask(context.Background(), Route, req); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if strings.Contains(req.Messages[0].Content, budgetMarker) {
		t.Errorf("a 16-token cap was stated in the prompt:\n%s", req.Messages[0].Content)
	}
	if req.MaxTokens != 16 {
		t.Errorf("MaxTokens = %d; a small cap is still the caller's choice", req.MaxTokens)
	}
}

// A request with no system message is a caller talking to the model directly,
// and this package does not edit that.
func TestARequestWithNoSystemMessageIsLeftAlone(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{"": {Content: "an answer"}})
	a := agentOnStub(t, model)
	a.executor.Limits(func(string) (int, int) { return 128000, 2048 })

	req := &llm.ChatRequest{
		Messages:  []llm.Message{{Role: "user", Content: "hello"}},
		MaxTokens: 8192,
	}
	if _, err := a.ask(context.Background(), Light, req); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if req.Messages[0].Content != "hello" {
		t.Errorf("the user message was edited:\n%s", req.Messages[0].Content)
	}
	if req.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %d; the cap still applies", req.MaxTokens)
	}
}
