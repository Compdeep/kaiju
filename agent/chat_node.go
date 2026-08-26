package agent

import (
	"context"
	"encoding/json"
	"log"

	"github.com/Compdeep/kaiju/agent/llm"
)

/*
 * The chat lane's answer, as a node on the graph.
 *
 * Preflight can decide a request is conversational and short-circuit before the
 * planner. The answer was then produced in the caller's error-recovery branch,
 * outside the graph — which made chat the one path that returns an answer with
 * no node behind it, and every mechanism that operates on nodes unavailable to
 * it: the operator's interjection had nowhere to land, nothing reflected on the
 * reply, the trace showed a run with no steps, and a steer typed while the model
 * was answering was read by nobody.
 *
 * Recording the call as a node changes none of the answering. It gives the same
 * work a place on the graph, so the machinery that already exists applies to it
 * rather than being rebuilt for one lane.
 *
 * Note this is the DAG's chat lane, NOT Converse. Converse never builds a graph
 * — it is one streamed call and stays that way, which is why this lives here and
 * not on the path both share.
 */

// chatNodeTag labels the node in a trace. Short, because it is the only node in
// the graph and its type already says what it is.
const chatNodeTag = "chat"

/*
 * runChatNode answers a conversational turn and records it as a graph node.
 * desc: Adds the node before the call, so an interjection arriving mid-answer
 *       has a node to attach to rather than racing an answer that has already
 *       been written. The reply is stored on the node and returned.
 * param: ctx - the run context.
 * param: trigger - the turn being answered.
 * param: graph - the run's graph; the node is added to it.
 * param: query - the user's message.
 * param: prompt - the assembled system prompt.
 * return: the reply, the node's id, and any error from the model call.
 */
func (a *Agent) runChatNode(ctx context.Context, trigger Trigger, graph *Graph, query, prompt string) (string, string, error) {
	node := &Node{Type: NodeChat, Tag: chatNodeTag}
	id := graph.AddNode(node)

	// Announced before the call so a client sees the turn start, and so an
	// interjection has something to attach to while the answer is still being
	// written — after it, the steer could only reframe prose already produced.
	graph.SetState(id, StateRunning)
	a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: id, Node: graph.SnapshotNode(id)})

	ctx = withTrace(ctx, TraceID{NodeID: id, NodeType: "chat", Tag: chatNodeTag})
	resp, err := a.completeHeavy(ctx, &llm.ChatRequest{
		Messages:    BuildMessagesWithHistory(prompt, query, trigger.History),
		Temperature: a.cfg.Temperature,
		MaxTokens:   a.cfg.MaxTokens,
	})
	if err != nil || len(resp.Choices) == 0 {
		if err == nil {
			err = errNoChatChoices
		}
		node.Error = err
		graph.SetState(id, StateFailed)
		a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: id, Node: graph.SnapshotNode(id)})
		return "", id, err
	}

	answer := resp.Choices[0].Message.Content
	graph.SetResult(id, answer)
	return answer, id, nil
}

// errNoChatChoices is the model returning success with nothing in it — a
// distinct failure from a transport error, and one worth naming rather than
// reporting as an empty answer.
var errNoChatChoices = chatError("the model returned no reply")

type chatError string

func (e chatError) Error() string { return string(e) }

/*
 * pendingInterjection returns a steering message if the operator sent one.
 * desc: A non-blocking read of the run's interjection channel. Used by the chat
 *       lane, which has no scheduling loop to poll it — without this a steer
 *       typed during a conversational turn is received by the scheduler and
 *       read by nobody.
 * param: ctx - the run context, carrying the channel.
 * return: the message and true, or "" and false when none is waiting.
 */
func pendingInterjection(ctx context.Context) (string, bool) {
	ch := interjectFrom(ctx)
	if ch == nil {
		return "", false
	}
	select {
	case msg := <-ch:
		return msg, true
	default:
		return "", false
	}
}

/*
 * addInterjectionNode records an operator's steer as a node beside the chat one.
 * desc: The same node type the scheduling loop creates, so a trace reads the
 *       same whichever lane the steer arrived on.
 * param: graph - the run's graph.
 * param: msg - what the operator sent.
 * return: the node's id.
 */
func addInterjectionNode(graph *Graph, msg string) string {
	n := &Node{Type: NodeInterjection, Tag: "operator", OperatorMessage: msg}
	id := graph.AddNode(n)
	graph.SetResult(id, msg)
	log.Printf("[dag] chat lane: operator interjection attached as %s", id)
	return id
}

// chatQuery reads the user's message out of a trigger's data.
func chatQuery(trigger Trigger) string {
	if trigger.Data == nil {
		return ""
	}
	var d map[string]string
	if json.Unmarshal(trigger.Data, &d) != nil {
		return ""
	}
	return d["query"]
}

/*
 * coordinateChatAnswer rewrites a chat reply to account for an operator steer.
 * desc: The aggregator lane, given the question, the answer already written and
 *       the steer that arrived while it was being written. It writes the reply
 *       the user reads, so the steer is answered rather than appended to an
 *       answer that predates it.
 *
 *       Runs ONLY when a steer arrived — an ordinary chat turn stays one call.
 *       That is the whole reason this is conditional: the aggregator is the
 *       expensive lane, and there is nothing to coordinate without a second
 *       message.
 * param: ctx - the run context.
 * param: trigger - the turn, for its history.
 * param: graph - the run's graph (unused today; the nodes are already recorded).
 * param: intent - the run's intent, for the lane.
 * param: query - the original message.
 * param: answer - what the chat node replied.
 * param: steer - the operator's interjection.
 * return: the coordinated reply, or an error.
 */
func (a *Agent) coordinateChatAnswer(ctx context.Context, trigger Trigger, graph *Graph,
	intent int, query, answer, steer string) (string, error) {

	ctx = withTrace(ctx, TraceID{NodeType: "chat", Tag: "coordinate"})
	sys := a.soulPrompt + `

The user sent a follow-up WHILE the reply below was being written, so the reply
does not account for it. Write the answer they should receive now.

Answer the follow-up. Keep from the earlier reply only what still applies — if
the follow-up changes what was asked, the earlier reply may be wholly beside the
point, and saying so plainly is better than reconciling two answers to different
questions. Do not describe the sequence of events; the user knows what they
typed.`
	sys += "\n\n## Output format\n" + a.FormatRule()

	user := "Original message:\n" + query +
		"\n\nReply written before the follow-up arrived:\n" + answer +
		"\n\nFollow-up:\n" + steer

	resp, err := a.completeHeavy(ctx, &llm.ChatRequest{
		Messages:    BuildMessagesWithHistory(sys, user, trigger.History),
		Temperature: a.cfg.Temperature,
		MaxTokens:   a.cfg.MaxTokens,
	})
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", errNoChatChoices
	}
	return resp.Choices[0].Message.Content, nil
}

// resolvedChatIntent is the intent a chat turn runs at. Kept as a named function
// so the chat lane's choice is visible rather than an inline zero.
func resolvedChatIntent(t Trigger) int { return int(t.Intent()) }
