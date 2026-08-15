package agent

import (
	"context"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
)

// Who made a model call, and what came back.
//
// Every stage wrote its own trace: the run, the node, the model, the start
// time, the latency, the token counts, the prompts, the reply — then the same
// two branches, one setting Err and one setting Output. Eleven times, and the
// only parts that differed were which node it was and what the stage wanted
// recorded about its own input.
//
// The door already holds the rest. So a stage says who it is, and the door
// writes the trace on every send.

// TraceID is what only the stage knows about a call: which node it is, what
// kind of stage, and whatever it wants recorded about its own input.
type TraceID struct {
	NodeID   string
	NodeType string
	Tag      string
	Input    map[string]string

	// Gate carries the ContextGate request behind this call, when there was
	// one. The stages that ask the gate are the ones whose prompts are worth
	// reading beside what the gate returned.
	GateSources  []string
	GateBudget   int
	GateSummary  string
	GateReturned map[string]string
}

type traceIDKey struct{}

// withTrace names the stage a call belongs to. Without it a call is still made
// and simply not traced — which is what a call outside any stage should do.
func withTrace(ctx context.Context, id TraceID) context.Context {
	return context.WithValue(ctx, traceIDKey{}, id)
}

func traceIDFrom(ctx context.Context) (TraceID, bool) {
	if ctx == nil {
		return TraceID{}, false
	}
	id, ok := ctx.Value(traceIDKey{}).(TraceID)
	return id, ok
}

/*
 * traceFault records that a reply the door already traced was unusable.
 * desc: The door writes its trace when the call returns, so it cannot know what
 *       the stage then made of the reply — a forced tool call that carried no
 *       arguments, or arguments that would not parse. Those are the failures a
 *       person reads the trace to find, and on the route stage they are recorded
 *       nowhere else.
 *
 *       It writes a second, short entry naming the same node, which lands
 *       directly under the call it is about. Amending the first is not open to
 *       us: the log is a file, appended to and never rewritten.
 * param: ctx - carries the run and the stage, as at the call.
 * param: why - what was wrong with the reply.
 */
func traceFault(ctx context.Context, why string) {
	id, named := traceIDFrom(ctx)
	if !named {
		return
	}
	WriteLLMTrace(LLMTrace{
		RunID:    runIDFrom(ctx),
		NodeID:   id.NodeID,
		NodeType: id.NodeType,
		Tag:      id.Tag,
		Err:      why,
	})
}

/*
 * writeTrace records one model call, if the caller said which stage it is.
 * desc: Called by the door on every send, so no stage can forget it and no
 *       stage repeats the seven fields the door already has. The reply is taken
 *       as the model gave it — content, or the first tool call's arguments,
 *       which is what a forced-tool stage reads.
 * param: ctx - carries the run and, when a stage set one, its identity.
 * param: req - the request as sent, after the lane and the cap were applied.
 * param: resp - the reply, or nil.
 * param: err - the call's error, or nil.
 * param: started - when the call began.
 */
func (a *Agent) writeTrace(ctx context.Context, req *llm.ChatRequest,
	resp *llm.ChatResponse, err error, started time.Time) {

	id, named := traceIDFrom(ctx)
	if !named {
		return
	}

	tr := LLMTrace{
		RunID:        runIDFrom(ctx),
		NodeID:       id.NodeID,
		NodeType:     id.NodeType,
		Tag:          id.Tag,
		Model:        req.Model,
		Started:      started,
		Input:        id.Input,
		GateSources:  id.GateSources,
		GateBudget:   id.GateBudget,
		GateSummary:  id.GateSummary,
		GateReturned: id.GateReturned,
		LatencyMS:    time.Since(started).Milliseconds(),
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			tr.System = m.Content
		case "user":
			tr.User = m.Content
		}
	}
	if err != nil {
		tr.Err = err.Error()
	}
	if resp != nil && len(resp.Choices) > 0 {
		c := resp.Choices[0]
		tr.Output = c.Message.Content
		if len(c.Message.ToolCalls) > 0 {
			tr.Output = c.Message.ToolCalls[0].Function.Arguments
		}
		tr.TokensIn = resp.Usage.PromptTokens
		tr.TokensOut = resp.Usage.CompletionTokens
	}
	WriteLLMTrace(tr)
}
