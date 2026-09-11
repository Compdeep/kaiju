package agent

import (
	"context"

	"github.com/Compdeep/kaiju/agent/llm"
)

// Writing prose for a person to read.
//
// Four stages do it and each did it alone: the chat lane, the chat node on the
// graph, the rewrite that answers an operator's interruption, and the
// aggregator. Each composed its own request, mapped its own streamed chunks to
// events, read its own reply — and picked its own cap, which is where the drift
// showed:
//
//	Converse      a cap from the table
//	runChatNode   cfg.MaxTokens
//	coordinate    cfg.MaxTokens
//	aggregator    cfg.MaxTokens doubled, floored at 8192
//
// Three answers to one question, none of them argued for against the others,
// and the lane a deployment actually uses was on the smallest.
//
// What is NOT shared is the prompt. Each of these assembles its own and should:
// a chat turn leads with history, the aggregator leads with evidence, the
// rewrite leads with the answer it is replacing. That is where they genuinely
// differ, so it is an input rather than a step.

/*
 * proseTurn is one stage's request for prose, as the stage can describe it.
 *
 * A struct rather than four types: what varies between these is four VALUES,
 * not four behaviours, and nobody outside this package writes a fifth. An
 * interface is for cases that cannot be enumerated — toolapi.Tool is one,
 * because an application implements it. These four are in this package and not
 * growing.
 */
type proseTurn struct {
	// Lane names which model answers. Answer for the three conversational
	// stages; the aggregator's is chosen by agg_mode and passed in.
	Lane Lane

	// Messages is the assembled prompt. Each stage builds its own.
	Messages []llm.Message

	// Reply is which cap from the table in budgets.go bounds it.
	Reply budgetSpec

	// Temperature for this stage. Stated rather than defaulted, because a
	// conversational turn and a synthesis do not want the same one and a zero
	// value cannot be told from a deliberate zero.
	Temperature float64

	// Model is stamped on the request where the turn named one. Empty leaves
	// the lane's own resolution alone.
	Model string

	// Graph and SessionID say where the streamed chunks go. A graph-less lane
	// carries a session id instead; broadcastDAGEvent takes either.
	Graph     *Graph
	SessionID string
}

/*
 * writeProse sends one request for prose and streams it as it arrives.
 * desc: The part these four stages share: the request shape, the cap, the
 *       streaming, and what an empty reply means. A step added here is added
 *       for all of them, which is what four copies could not do.
 * param: ctx - the run context, carrying the lane selection and the trace.
 * param: t - the turn.
 * return: the reply, or an error — including for a reply with no choices, which
 *         by the time it reaches here has already been asked again once.
 */
func (a *Agent) writeProse(ctx context.Context, t proseTurn) (*llm.ChatResponse, error) {
	resp, err := a.askStreamResp(ctx, t.Lane, &llm.ChatRequest{
		Model:       t.Model,
		Messages:    t.Messages,
		Temperature: t.Temperature,
		MaxTokens:   a.replyBudget(ctx, t.Lane, t.Reply),
	}, a.streamTo(t.Graph, t.SessionID))
	if err != nil {
		return resp, err
	}
	if len(resp.Choices) == 0 {
		return resp, errNoChatChoices
	}
	return resp, nil
}

/*
 * streamTo is where one stage's chunks go as they arrive.
 * desc: Reasoning and content reach a reader on different channels, so a client
 *       can show the thinking apart from the answer. This mapping was written
 *       out at all four call sites, identically.
 * param: graph - the run's graph, or nil for a lane that has none.
 * param: session - the session to name when there is no graph.
 * return: the callback to hand the door.
 */
func (a *Agent) streamTo(graph *Graph, session string) func(chunk, kind string) {
	return func(chunk, kind string) {
		if graph == nil && session == "" {
			return
		}
		evType := "outcome"
		if kind == "reasoning" {
			evType = "reasoning"
		}
		a.broadcastDAGEvent(graph, DAGEvent{Type: evType, Text: chunk, SessionID: session})
	}
}

// proseOf is the text a prose turn produced, and "" when there is none. Every
// one of these stages read Choices[0].Message.Content for itself.
func proseOf(resp *llm.ChatResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return resp.Choices[0].Message.Content
}
