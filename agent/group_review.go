package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/prompt"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Reading a tool's replies together, once they are all in.
//
// A reply can only be judged against something. On its own it is output that
// arrived, and a tool that succeeded at asking cannot tell an answer from a
// refusal without knowing the service it asked — the refusal is well-formed, it
// is the right size, and it arrived the same way. Every rule that tries to tell
// them apart is a rule about one service's habits, and there is no standard for
// such a rule to be general about.
//
// Beside its siblings the same reply is plain. Calls to one tool asking one kind
// of question produce a set of answers that look alike and, when something went
// wrong, one or two that do not — and which is which needs no rule at all. That
// comparison exists for one moment, after the last sibling lands and before
// anything downstream reads them, and nothing was looking at it.
//
// So this fires at that moment, once per group, and only when a sibling already
// failed on its own terms. It is the cheap, local, early pass; the reflector
// remains the expensive, global, late one, and still sees everything afterwards.

// groupReview is the reviewer's verdict on one set of siblings.
type groupReview struct {
	Reason   string          `json:"reason"`
	Unusable []unusableReply `json:"unusable"`
}

// unusableReply is one sibling the reviewer will not accept, and what to do.
type unusableReply struct {
	Tag    string `json:"tag"`
	Action string `json:"action"` // "retry", "correct", "give_up"
	Params string `json:"params"` // a JSON object in a string, when Action is "correct"
	Why    string `json:"why"`
}

/*
 * completedGroup reports the siblings of a node when they have all finished.
 * desc: A group is the calls to one tool in one planning round. It is complete
 *       when none of them can still change — resolved, failed or skipped — and
 *       it is worth reading only when at least one of them failed.
 *
 *       That last condition is the whole cost control and it carries no
 *       judgement: a sibling failing is the tool's own report, not a reading of
 *       its output. A group where everything succeeded is never reviewed, so
 *       the ordinary run pays nothing.
 *
 *       A node still holding a blind retry is back to pending and therefore not
 *       terminal, so the group waits for it. The cheap transient retry runs
 *       first and this only ever sees what is still stuck.
 * param: g - the run.
 * param: n - the node that just completed.
 * return: the group and true, or nil and false.
 */
func completedGroup(g *Graph, n *Node) ([]*Node, bool) {
	if g == nil || n == nil || n.Type != NodeTool || n.ToolName == "" {
		return nil, false
	}
	group := g.SiblingCalls(n.ToolName, n.Round)
	if len(group) < 2 {
		return nil, false // nothing to compare against
	}
	failed := false
	for _, s := range group {
		if !s.IsTerminal() {
			return nil, false // one is still running, or retrying
		}
		if s.State == StateFailed {
			failed = true
		}
		if s.Retry == groupRetryTier {
			return nil, false // already reviewed this round; one pass per group
		}
	}
	if !failed {
		return nil, false
	}
	return group, true
}

// groupRetryTier marks a node re-run by this stage. It is its own tier so the
// scheduler's one-retry-per-node rule cannot spend the allowance twice: a node
// that already took a blind retry for a transient failure is still eligible
// here, and a node
// re-run here is not eligible for another pass.
const groupRetryTier = "group"

/*
 * fireGroupReview reads a set of sibling replies together and re-runs the ones
 * it will not accept.
 * desc: One LLM call for the whole group, never one per reply. It is shown what
 *       each sibling was asked, what came back, and the parameters the tool
 *       actually takes — the tool's own declared schema, so nothing here knows
 *       what any tool is for.
 *
 *       A sibling to re-run is rewritten in place rather than grafted beside:
 *       a downstream ${node.<id>.field} keeps pointing at the same node, and
 *       putting it back to pending makes it non-terminal, which holds every
 *       dependent without a single line saying so.
 * param: ctx - context for the LLM call.
 * param: group - the siblings, all terminal, at least one failed.
 * param: graph - the run.
 * param: budget - the execution budget.
 * param: ch - where this stage's own completion goes.
 * param: trigger - the run's starting point.
 */
func (a *Agent) fireGroupReview(ctx context.Context, group []*Node,
	graph *Graph, budget *Budget, ch chan<- nodeCompletion, trigger Trigger) {

	var revID string
	defer func() { a.guardNodeCompletion("fireGroupReview", revID, ch) }()

	toolName := group[0].ToolName
	revNode := &Node{Type: NodeGroupReview, Tag: "review_" + toolName}
	revID = graph.AddNode(revNode)
	graph.SetState(revID, StateRunning)

	user := a.groupReviewPrompt(group, toolName)
	sysPrompt := ComposeSystemPrompt(a.soulPrompt, prompt.GroupReview) + a.environmentSection()

	ctx = withTrace(ctx, TraceID{
		NodeID:   revID,
		NodeType: "group_review",
		Tag:      revNode.Tag,
		Input:    map[string]string{"tool": toolName, "siblings": fmt.Sprint(len(group))},
	})
	resp, err := a.completeLightChecked(ctx, &llm.ChatRequest{
		Messages:    BuildMessagesWithResults(sysPrompt, user, nil, graph.Arcs()),
		Tools:       []llm.ToolDef{groupReviewSchema()},
		ToolChoice:  "required",
		Temperature: a.cfg.Temperature,
		MaxTokens:   a.replyBudget(replyDecisionBudget),
	})
	if err != nil {
		log.Printf("[dag] group review of %s failed: %v", toolName, err)
		graph.SetResult(revID, "group review error: "+err.Error())
		ch <- nodeCompletion{NodeID: revID}
		return
	}
	raw, err := extractToolArgs(resp)
	if err != nil {
		graph.SetResult(revID, "no response")
		ch <- nodeCompletion{NodeID: revID}
		return
	}

	var out groupReview
	if perr := ParseLLMJSON(raw, &out); perr != nil {
		log.Printf("[dag] group review of %s did not parse, leaving the group alone: %v", toolName, perr)
		graph.SetResult(revID, raw)
		ch <- nodeCompletion{NodeID: revID, Result: raw}
		return
	}

	applied := a.applyGroupReview(graph, group, out)
	log.Printf("[dag] group review of %s: %d of %d unusable (%s), %d re-run",
		toolName, len(out.Unusable), len(group), Text.TruncateLog(out.Reason, 120), applied)
	appendWorklog(a.cfg.MetadataDir, graph.SessionID, revNode.Tag, "GROUP_REVIEW",
		fmt.Sprintf("%d of %d unusable, %d re-run: %s", len(out.Unusable), len(group), applied, out.Reason))

	graph.SetResult(revID, raw)
	ch <- nodeCompletion{NodeID: revID, Result: raw,
		TokensIn: resp.Usage.PromptTokens, TokensOut: resp.Usage.CompletionTokens}
}

/*
 * applyGroupReview re-runs the siblings the reviewer would not accept.
 * desc: In place, so a downstream reference to the node still reaches it, and
 *       back to pending, so nothing downstream fires until the re-run lands.
 *
 *       A correction whose parameters do not parse is dropped rather than
 *       applied half way: a node left with some of its old parameters and some
 *       of its new ones is a call nobody wrote.
 * param: graph - the run.
 * param: group - the siblings.
 * param: out - the verdict.
 * return: how many nodes were put back to pending.
 */
func (a *Agent) applyGroupReview(graph *Graph, group []*Node, out groupReview) int {
	byTag := make(map[string]*Node, len(group))
	for _, n := range group {
		byTag[n.Tag] = n
	}
	applied := 0
	for _, u := range out.Unusable {
		n, ok := byTag[u.Tag]
		if !ok {
			log.Printf("[dag] group review named %q, which is not in this group", u.Tag)
			continue
		}
		switch u.Action {
		case "give_up":
			// Left failed, carrying the reviewer's reason so the reflector reads
			// why rather than only that it broke.
			if u.Why != "" {
				graph.SetError(n.ID, fmt.Errorf("%s: %s", n.Tag, u.Why))
			}
			continue
		case "correct":
			params := map[string]any{}
			if err := ParseLLMJSON(u.Params, &params); err != nil || len(params) == 0 {
				log.Printf("[dag] group review's correction for %s does not parse, leaving it failed: %v", u.Tag, err)
				continue
			}
			for k, v := range params {
				graph.SetParam(n.ID, k, v)
			}
		case "retry":
			// Nothing to change; the same call is worth making again.
		default:
			continue
		}
		graph.SetRetry(n.ID, groupRetryTier)
		n.Error = nil // cleared on the node, as the blind tier does
		// Spaced, not fired together. The siblings left in one instant the first
		// time, because the scheduler fires everything ready at once — and a
		// group is siblings precisely because they share a tool, which usually
		// means they share whatever is at the far end of it. Re-running them
		// the same way asks the same thing the same question at the same
		// moment. Observed: nine calls left together, three came back refused
		// for being too many.
		graph.HoldUntil(n.ID, time.Duration(applied)*groupRetrySpacing)
		graph.SetState(n.ID, StatePending)
		applied++
		log.Printf("[dag] group review re-runs %s (%s) in %s: %s",
			n.Tag, u.Action, time.Duration(applied-1)*groupRetrySpacing, u.Why)
	}
	return applied
}

// groupRetrySpacing is how far apart the re-runs of one group are placed.
//
// The siblings share a tool, and a tool that talks to one service means they
// share the far end. Whatever it refused when they arrived together it will
// refuse again if they arrive together again. Short enough that a run waiting on
// a handful of them is still answering.
const groupRetrySpacing = 1200 * time.Millisecond

/*
 * groupReviewPrompt lays the siblings side by side.
 * desc: What each was asked, what came back, and the parameters the tool takes.
 *       The last comes from the tool's own declared schema, so this knows the
 *       name of no tool and the shape of no reply.
 *
 *       Each reply is cut. A group of replies at full length is not a prompt, and
 *       a reply that arrived carries the path to all of it — the reviewer is
 *       judging which replies are the odd ones out, which the opening of each
 *       one shows.
 * param: group - the siblings, in the order they were planned.
 * param: toolName - the tool they all ran.
 * return: the user message.
 */
func (a *Agent) groupReviewPrompt(group []*Node, toolName string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %d calls to `%s`\n\n", len(group), toolName)

	if a.registry != nil {
		if tool, ok := a.registry.Get(toolName); ok {
			fmt.Fprintf(&sb, "%s\n\n### The parameters it takes\n```json\n%s\n```\n\n",
				tool.Description(), string(tool.Parameters()))
			if schema := toolapi.GetOutputSchema(tool); schema != nil {
				fmt.Fprintf(&sb, "### What it returns when it works\n```json\n%s\n```\n\n", string(schema))
			}
		}
	}

	sb.WriteString("### The replies\n")
	for _, n := range group {
		params, _ := json.Marshal(n.Params)
		fmt.Fprintf(&sb, "\n#### %s — %s\n", n.Tag, n.State)
		fmt.Fprintf(&sb, "asked: `%s`\n", Text.TruncateLog(string(params), 600))
		if n.Error != nil {
			fmt.Fprintf(&sb, "failed: %s\n", Text.TruncateLog(n.Error.Error(), 400))
		}
		if n.Result != "" {
			fmt.Fprintf(&sb, "replied:\n```\n%s\n```\n", Text.TruncateLog(n.Result, groupReplyCut))
		} else if n.Error == nil {
			sb.WriteString("replied: nothing\n")
		}
	}
	return sb.String()
}

// groupReplyCut is how much of each reply the reviewer is shown.
//
// Enough to tell one kind of document from another, which is the only question
// being asked. A reply that arrived also carries the path to the whole of it,
// and a later step can read that; showing every reply in full would spend the
// budget of a reflection on a comparison the first lines already settle.
const groupReplyCut = 1200
