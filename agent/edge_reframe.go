package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/prompt"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Telling a stage what happened before it, in the words of the request.
//
// A stage that reads a run's results reads a list: this step resolved, that one
// failed, here is what came back. What it does not get is the situation — what
// was asked, what the run actually holds, and what it went looking for and did
// not find. A model handed a list and no situation fills the space itself, which
// is where an answer about a page nobody opened comes from.
//
// This replaces two edges that each did half of it. One listed the steps that
// came back with nothing; the other listed values the run held and had not
// followed up. Both fired on the same condition so neither could fire alone,
// each made its own model call before the reading stage made its own, and both
// prepended a block to the same prompt — three calls to describe one situation
// twice.
//
// The second of them wrote in one application's vocabulary. It told every run
// it had "real URLs from a search that you have NOT read yet", whatever its
// tools had actually returned, because URLs were what the engine's own tools
// returned when it was written. An application whose tools return process ids
// was told it had URLs. Nothing here names a kind of value: the facts say a
// step produced something or did not, and the model describes it in the terms
// the request used.
//
// It runs on every reframe, not only when something went wrong. The gate the
// two edges shared meant neither could fire on a run where every step
// succeeded — and a run that gathered ten results and read none of them is
// exactly that run.

// runFacts is what happened, with nothing decided about it.
//
// Assembled by code and handed to the model as text. No judgement here: a step
// that returned nothing is recorded as having returned nothing, not as a
// problem, because whether it is a problem depends on the request.
type runFacts struct {
	// Produced is one line per resolved tool step: what it was and how it
	// ended.
	Produced []string
	// Failed is one line per step that did not complete.
	Failed []string
	// Unfollowed is one line per value the run holds that no later step used.
	Unfollowed []string
}

// empty reports whether the run has produced nothing at all to describe.
func (f runFacts) empty() bool {
	return len(f.Produced) == 0 && len(f.Failed) == 0 && len(f.Unfollowed) == 0
}

/*
 * factsOf reads a run and returns what happened.
 * desc: Four outcomes per step, taken off the ToolMessage envelope, because the
 *       three that are not failures are invisible everywhere else. A stage
 *       reading the graph's own summary sees "resolved" for a search that
 *       returned nothing, a read of an empty file, and a tool that declined to
 *       say whether it found anything.
 *
 *       A step whose body is not an envelope — a tool that returns a plain
 *       string — is recorded as having produced a result, which is all that can
 *       honestly be said about it.
 * param: graph - the run so far.
 * return: the facts, empty when nothing has resolved.
 */
func (a *Agent) factsOf(graph *Graph) runFacts {
	var f runFacts
	if graph == nil {
		return f
	}

	for _, n := range graph.ResolvedByType(NodeTool) {
		label := n.Tag
		if label == "" {
			label = n.ToolName
		}
		outcome := "produced a result"
		if tb, ok := n.Body.(toolMessageBody); ok {
			env := tb.Envelope()
			switch env.Status {
			case toolapi.StatusEmpty:
				outcome = "returned nothing"
				if env.Detail != "" {
					outcome += " — " + env.Detail
				}
			case toolapi.StatusError:
				outcome = "could not run"
				if env.Detail != "" {
					outcome += " — " + env.Detail
				}
			case toolapi.StatusUnclassified:
				outcome = "returned something but did not say whether it found anything"
			}
		}
		f.Produced = append(f.Produced, fmt.Sprintf("- %s (%s): %s", label, n.ToolName, outcome))
	}

	for _, n := range graph.FailedNodes() {
		label := n.Tag
		if label == "" {
			label = n.ToolName
		}
		reason := ""
		if n.Error != nil {
			reason = " — " + n.Error.Error()
		}
		f.Failed = append(f.Failed, fmt.Sprintf("- %s (%s): failed%s", label, n.ToolName, reason))
	}

	for _, r := range a.unresolvedReferences(graph) {
		from := r.Tag
		if from == "" {
			from = "an earlier step"
		}
		f.Unfollowed = append(f.Unfollowed,
			fmt.Sprintf("- %s (from %s)", Text.TruncateLog(r.Value, 200), from))
	}

	return f
}

// text renders the facts for the model.
func (f runFacts) text(request string) string {
	var sb strings.Builder
	sb.WriteString("REQUEST:\n")
	sb.WriteString(strings.TrimSpace(request))
	sb.WriteString("\n\nWHAT EACH STEP PRODUCED:\n")
	if len(f.Produced) == 0 {
		sb.WriteString("(no step has produced anything yet)\n")
	} else {
		sb.WriteString(strings.Join(f.Produced, "\n") + "\n")
	}
	if len(f.Failed) > 0 {
		sb.WriteString("\nSTEPS THAT DID NOT COMPLETE:\n")
		sb.WriteString(strings.Join(f.Failed, "\n") + "\n")
	}
	sb.WriteString("\nALREADY IN HAND, NOT YET FOLLOWED UP:\n")
	if len(f.Unfollowed) == 0 {
		sb.WriteString("(nothing)\n")
	} else {
		sb.WriteString(strings.Join(f.Unfollowed, "\n") + "\n")
	}
	return sb.String()
}

/*
 * EdgeReFrame describes what a run has done so far, for the stage about to act
 * on it.
 * desc: One model call. Code assembles the facts; the model writes them as a
 *       short situation in the terms the request used, which is the part no
 *       list can do — a list of steps says nothing about whether the request
 *       has been answered.
 *
 *       Three stages read one of these and each does something different with
 *       it: one chooses whether to keep working, one writes the answer, one
 *       judges how serious the result is. reader is the sentence that says
 *       which, and it is the only thing that differs between them. One prompt
 *       with a slot rather than three prompts, because three prompts written
 *       days apart is how the two edges this replaces came to disagree.
 *
 *       Fails open, in both directions: with no model it returns the facts as
 *       assembled, and if the call fails it returns them too. A stage always
 *       gets something, and never gets nothing because a reframe could not be
 *       written.
 * param: ctx - the run's context.
 * param: graph - the run so far.
 * param: request - what was asked, as the calling stage renders it.
 * param: reader - what the reading stage is about to do, as a verb phrase
 *        completing "a stage that is about to …".
 * return: the block to prepend, or "" when the run has produced nothing to
 *         describe.
 */
func (a *Agent) EdgeReFrame(ctx context.Context, graph *Graph, request, reader string) string {
	facts := a.factsOf(graph)
	if facts.empty() {
		return "" // nothing has run; there is no situation to describe
	}
	user := facts.text(request)

	client, model := a.lightLane(ctx)
	if client == nil {
		return reframeHeading + "\n\n" + user
	}

	sys := fmt.Sprintf(prompt.Reframe, reader)
	resp, err := a.askParsed(withTrace(ctx, TraceID{
		NodeType: "reframe",
		Tag:      "reframe",
		Input:    map[string]string{"reader": reader},
	}), Light, &llm.ChatRequest{
		Model:       model,
		Messages:    []llm.Message{{Role: "system", Content: sys}, {Role: "user", Content: user}},
		Temperature: 0.2,
		MaxTokens:   400,
	})
	if err != nil || resp == nil || len(resp.Choices) == 0 ||
		strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
		log.Printf("[reframe] no reframe written, passing the facts through: %v", err)
		return reframeHeading + "\n\n" + user
	}
	return reframeHeading + "\n\n" + strings.TrimSpace(resp.Choices[0].Message.Content)
}

// reframeHeading opens the block. The reading stage's own prompt names it, so
// the two have to agree.
const reframeHeading = "## What happened so far"

/*
 * WithReframe prepends the block to a stage's input and tells its prompt what
 * the block is.
 * desc: The pair has to move together — a block with no instruction is text a
 *       model may or may not credit, and an instruction with no block describes
 *       something that is not there. One call so a stage cannot adopt half.
 *
 *       Exported because an application writing its own answering stage needs
 *       the same pairing, and hand-assembling it there is how the two halves
 *       come apart.
 * param: role - the stage's system prompt.
 * param: user - the stage's input.
 * param: block - what EdgeReFrame returned, or "".
 * return: the two prompts, unchanged when there is no block.
 */
func WithReframe(role, user, block string) (string, string) {
	if block == "" {
		return role, user
	}
	return role + "\n\n" + prompt.ReframeHook, block + "\n\n" + user
}
