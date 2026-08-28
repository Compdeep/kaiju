package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A model that answers from a script, so a whole run can be driven in a test.
//
// The engine's stages are the only things that call a model, and each names its
// own function when it asks — route, submit_preflight, plan, reflector_decision,
// submit_summary. That name is the whole dispatch: a stage asking for a plan is
// a request whose tools carry submit_plan, and nothing else in a run looks like
// that.
//
// So this is an OpenAI endpoint that reads the function name off the request and
// replies with whatever the test scripted for that stage. Nothing in the engine
// changes and no interface is added for testing's sake — the engine already
// takes an endpoint, and this is one.
//
// Why it exists: the parts of this package with the most behaviour in them —
// the scheduler, the executive — had no test that could reach them, because
// reaching them meant a real model. Three separate behaviours were lost during
// this migration while the whole suite stayed green: a data-flow check that
// stopped rejecting anything, a classifier that stopped running, and a kernel
// that lost both its modules. None was a compile error; none broke a test.
//
// With this, two of the three fail. The third does not, because it is on the
// queued path rather than the synchronous one — see the note in
// run_endtoend_test.go.

// stubModel is a scripted model endpoint.
type stubModel struct {
	*httptest.Server

	mu    sync.Mutex
	calls []stubCall             // every request, in order
	reply map[string]stubReply   // function name → what to answer
	byNth map[string][]stubReply // function name → answers for the 1st, 2nd, … call
}

// stubCall is one request the engine made.
type stubCall struct {
	Function string // the function the stage asked for
	System   string // the system prompt it sent
	User     string // the last user message
	// Messages is every message the request carried, in order.
	//
	// System and User are the two a test usually wants and are kept for the
	// tests that read them. Neither can answer "was this stage GIVEN the value
	// the step before it produced" — the answer is somewhere in the array, and
	// once results travel as tool messages it is not in either of them. A test
	// asserting what a stage was shown has to see the whole request.
	Messages []stubMessage
}

// stubMessage is one message of a request, as the engine sent it.
type stubMessage struct {
	Role       string
	Content    string
	ToolCallID string
	Name       string
	// ToolCalls are the function names an assistant message declared, if any.
	ToolCalls []string
}

// stubReply is what the model says: either a tool call's arguments, or prose.
type stubReply struct {
	Args    any    // marshalled into the tool call's arguments
	Content string // used instead when no Args are given
	// Cut makes the endpoint report finish_reason "length", which is how every
	// provider says the reply stopped at the token cap rather than because the
	// model had finished.
	Cut bool
}

// newStubModel starts an endpoint that answers each stage from the script.
// A stage with no script entry gets an empty tool call, which every stage reads
// as "nothing to say" rather than an error.
func newStubModel(t *testing.T, script map[string]stubReply) *stubModel {
	t.Helper()
	s := &stubModel{reply: script, byNth: map[string][]stubReply{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

// answerNth scripts the nth call to a function differently from the rest, which
// is how a run that reflects twice is driven: continue, then conclude.
func (s *stubModel) answerNth(fn string, replies ...stubReply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byNth[fn] = replies
}

func (s *stubModel) handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			Name       string `json:"name"`
			ToolCalls  []struct {
				Function struct{ Name string } `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
		Stream bool `json:"stream"`
		Tools  []struct {
			Function struct{ Name string } `json:"function"`
		} `json:"tools"`
		// A forced single-tool call goes out as a schema request instead — see
		// llm/structured.go. The stub answers by function name either way, so it
		// reads the name from wherever this call carried it.
		ResponseFormat struct {
			JSONSchema struct {
				Name string `json:"name"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)

	fn := ""
	// schema is true when the request went out as a response_format rather than
	// as a tool — see llm/structured.go. It changes what a real provider sends
	// BACK, so it has to change what this one sends back, or the stub answers a
	// wire nobody is on.
	schema := false
	if len(req.Tools) > 0 {
		fn = req.Tools[0].Function.Name
	} else if n := req.ResponseFormat.JSONSchema.Name; n != "" {
		fn, schema = n, true
	}
	call := stubCall{Function: fn}
	for _, m := range req.Messages {
		sm := stubMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name}
		for _, tc := range m.ToolCalls {
			sm.ToolCalls = append(sm.ToolCalls, tc.Function.Name)
		}
		call.Messages = append(call.Messages, sm)
	}
	if len(req.Messages) > 0 {
		call.System = req.Messages[0].Content
		call.User = req.Messages[len(req.Messages)-1].Content
	}

	s.mu.Lock()
	nth := 0
	for _, c := range s.calls {
		if c.Function == fn {
			nth++
		}
	}
	s.calls = append(s.calls, call)
	reply, scripted := s.reply[fn]
	if seq, ok := s.byNth[fn]; ok && nth < len(seq) {
		reply, scripted = seq[nth], true
	}
	s.mu.Unlock()

	// The router runs before every other stage and decides whether the run is a
	// conversation or a piece of work. A test that scripts nothing for it wants
	// the work, because that is the only path the rest of the stages are on.
	if fn == "route" && !scripted {
		reply, scripted = stubReply{Args: map[string]any{"mode": "agent"}}, true
	}

	// The aggregator streams its answer, so that path has to be answered as
	// server-sent events rather than one body.
	if req.Stream {
		content := reply.Content
		if !scripted || content == "" {
			content = "stub answer"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(
			`{"choices":[{"delta":{"content":%s}}]}`, mustJSON(content)))
		fmt.Fprint(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if fn == "" {
		// No tools offered: the stage wants prose. The aggregator is the one
		// that does, so a script keyed on "" is what answers it, and the
		// fallback is a non-empty line because a stage that receives nothing
		// treats it as a failure.
		content := reply.Content
		if !scripted || content == "" {
			content = "stub answer"
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s},"finish_reason":%s}]}`,
			mustJSON(content), mustJSON(finishReason(reply, "stop")))
		return
	}
	args := "{}"
	if reply.Args != nil {
		b, _ := json.Marshal(reply.Args)
		args = string(b)
	}
	// A schema request is answered the way a provider answers one: the object as
	// message content, and finish_reason "stop", because no tool was offered so
	// no tool was called. llm.asToolReply is what turns that back into the shape
	// the stage reads.
	//
	// This used to reply with a tool call whatever was asked, which is why the
	// suite stayed green through a live break: the executive gates on
	// finish_reason == "tool_calls", the real provider said "stop", and every
	// test here said "tool_calls". A stub that answers a wire nobody is on
	// tests nothing.
	if schema {
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s},"finish_reason":%s}]}`,
			mustJSON(args), mustJSON(finishReason(reply, "stop")))
		return
	}
	fmt.Fprintf(w, `{"choices":[{"message":{"tool_calls":[{"id":"c1","type":"function","function":{"name":%s,"arguments":%s}}]},"finish_reason":%s}]}`,
		mustJSON(fn), mustJSON(args), mustJSON(finishReason(reply, "tool_calls")))
}

// finishReason is "length" for a scripted-truncated reply and the stage's
// ordinary reason otherwise.
func finishReason(reply stubReply, ordinary string) string {
	if reply.Cut {
		return "length"
	}
	return ordinary
}

func mustJSON(v string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// callsTo counts what a stage was asked, which is how a test says "the
// aggregator did not run" without reaching inside the scheduler.
func (s *stubModel) callsTo(fn string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if c.Function == fn {
			n++
		}
	}
	return n
}

// sawSystemContaining reports whether any stage was sent a system prompt
// carrying the text — how a test checks that framing reached the model.
// requestsTo returns every request one stage received, in order.
func (s *stubModel) requestsTo(fn string) []stubCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []stubCall
	for _, c := range s.calls {
		if c.Function == fn {
			out = append(out, c)
		}
	}
	return out
}

// wasShown reports whether the nth request to a stage carried the text
// ANYWHERE in it — any message, any role.
//
// The question a test about lost values has to ask. sawUserContaining reads one
// message of one request; a value can be in the system prompt, in an earlier
// user message, or in a tool result, and "was this stage given it" is true in
// all of those cases and false only when it is in none.
func (s *stubModel) wasShown(fn string, nth int, text string) bool {
	reqs := s.requestsTo(fn)
	if nth < 0 || nth >= len(reqs) {
		return false
	}
	for _, m := range reqs[nth].Messages {
		if strings.Contains(m.Content, text) {
			return true
		}
	}
	return false
}

// shownTo renders one request for a failure message: role, and the first of
// each message, so a test that fails says what the stage actually got.
func (s *stubModel) shownTo(fn string, nth int) string {
	reqs := s.requestsTo(fn)
	if nth < 0 || nth >= len(reqs) {
		return fmt.Sprintf("(no request %d to %s; there were %d)", nth, fn, len(reqs))
	}
	var sb strings.Builder
	for _, m := range reqs[nth].Messages {
		label := m.Role
		if len(m.ToolCalls) > 0 {
			label += " tool_calls=" + strings.Join(m.ToolCalls, ",")
		}
		if m.ToolCallID != "" {
			label += " tool_call_id=" + m.ToolCallID
		}
		sb.WriteString(fmt.Sprintf("  %-28s %s\n", label, Text.TruncateLog(m.Content, 160)))
	}
	return sb.String()
}

func (s *stubModel) sawSystemContaining(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if strings.Contains(c.System, text) {
			return true
		}
	}
	return false
}

// sawUserContaining reports whether any stage was sent the text in its last
// user message.
func (s *stubModel) sawUserContaining(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if strings.Contains(c.User, text) {
			return true
		}
	}
	return false
}

// functionsCalled lists the stages that ran, in order, for a failure message
// that says what the run actually did.
func (s *stubModel) functionsCalled() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.calls))
	for _, c := range s.calls {
		out = append(out, c.Function)
	}
	return out
}

// agentOnStub builds an agent whose model is the stub and whose paths are
// temporary. Nothing else is stubbed: the registry, the graph, the scheduler
// and the dispatcher are the real ones.
func agentOnStub(t *testing.T, s *stubModel, tools ...toolapi.Tool) *Agent {
	t.Helper()
	return agentOnStubWith(t, s, DAGConfig{DAGEnabled: true, MaxNodes: 20, MaxLLMCalls: 20}, tools...)
}

// agentOnStubWith is agentOnStub with the run's limits chosen by the caller —
// for the tests that drive what happens when a limit is reached.
func agentOnStubWith(t *testing.T, s *stubModel, dag DAGConfig, tools ...toolapi.Tool) *Agent {
	t.Helper()
	d := t.TempDir()
	a, err := New(Config{
		ModelConfig: ModelConfig{
			LLMEndpoint: s.URL, LLMAPIKey: "k", LLMModel: "stub", MaxTokens: 2048,
		},
		PathConfig:     PathConfig{Workspace: d, DataDir: d, MetadataDir: d},
		IdentityConfig: IdentityConfig{NodeID: "this-node"},
		DAGConfig:      dag,
		RoutingConfig:  RoutingConfig{ClassifierEnabled: true},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, tool := range tools {
		if err := a.registry.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name(), err)
		}
	}
	t.Cleanup(a.Stop)
	return a
}
