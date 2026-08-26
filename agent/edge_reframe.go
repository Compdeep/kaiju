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

// stepOutcomesSource is how each step ended, and what the run holds and has not
// used. Registered on every gate — see contextgate.go.
type stepOutcomesSource struct{}

func (s *stepOutcomesSource) Name() string { return SourceStepOutcomes }

/*
 * Load renders one line per step and one per unused value.
 * desc: Five outcomes, read off the ToolMessage envelope, because four of them
 *       are invisible everywhere else: the graph counts a search that returned
 *       nothing as resolved, and the node returns list failures and successes
 *       with nothing between.
 *
 *       Success says "succeeded". It used to share the default wording with a
 *       body that is not an envelope at all, which made a step that worked and a
 *       step nobody could read into the same sentence — and left the reader no
 *       way to tell a finished command from one that died, since a failure's
 *       opening line is often identical to a success's. Where the tool reports
 *       an exit status, that number is appended as a fact; see exitCodeOf.
 *
 *       A step whose body is not an envelope — a tool that returns a plain
 *       string — is still recorded as having produced a result, which remains
 *       all that can honestly be said about it.
 * param: g - the run so far.
 * param: a - the agent, for the registry the unused values are read against.
 * return: the text, empty when nothing has resolved and nothing has failed.
 */
// exitCodeOf reads a command tool's exit status off its payload, for the tools
// that carry one.
//
// It is reported as a fact, never as a verdict. exit 0 is not proof the goal was
// met — grep exits 1 on no match, and a command can exit 0 having done nothing
// useful — so the number is handed over and the reader judges it. Stating it as
// success would trade a false negative for a false positive, in the one stage
// whose job is catching work that only looks finished.
//
// It is here because dropping it cost a run its result: a clone that finished
// and a clone that died both open with "Cloning into '/tmp/xyz'...", which git
// prints before it knows the outcome. With the status word alone, the two were
// the same string, and the stale failure beside them carried its exit code while
// the success did not — so the reader resolved the ambiguity against the run.
func exitCodeOf(b toolMessageBody) (int, bool) {
	v, ok := b.Field("exit_code")
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64: // JSON numbers decode as float64
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func (s *stepOutcomesSource) Load(g *Graph, _ *Trigger, a *Agent, _ map[string]any) (string, error) {
	if g == nil {
		return "", nil
	}
	// Split by planning round. Everything a run has ever done is available
	// here, and handing it over as one list presents a step from the first
	// round as though it were the situation now — so a problem already solved
	// keeps being described as the problem, and the stage reading this keeps
	// solving it again.
	//
	// The current round is what the run just did. Earlier rounds are history:
	// still worth knowing, and not the thing in front of it.
	current := g.Round()
	var produced, earlier, failed, unused []string

	for _, n := range g.ResolvedByType(NodeTool) {
		outcome := "produced a result"
		if tb, ok := n.Body.(toolMessageBody); ok {
			env := tb.Envelope()
			switch env.Status {
			case toolapi.StatusOK:
				outcome = "succeeded"
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
			// The exit status, for the tools that have one. Not on the error
			// path: that detail already reads "exit 128: exit status 128", and
			// repeating the number says it twice.
			if env.Status != toolapi.StatusError {
				if code, found := exitCodeOf(tb); found {
					outcome += fmt.Sprintf(" — exit %d", code)
				}
			}
		}
		line := "- " + stepLabel(n) + ": " + outcome
		if n.Round < current {
			earlier = append(earlier, line)
			continue
		}
		produced = append(produced, line)
	}

	for _, n := range g.FailedNodes() {
		reason := ""
		if n.Error != nil {
			reason = " — " + n.Error.Error()
		}
		failed = append(failed, "- "+stepLabel(n)+": failed"+reason)
	}

	if a != nil {
		for _, r := range a.unresolvedReferences(g) {
			from := r.Tag
			if from == "" {
				from = "an earlier step"
			}
			unused = append(unused, fmt.Sprintf("- %s (from %s)", Text.TruncateLog(r.Value, 200), from))
		}
	}

	if len(produced) == 0 && len(earlier) == 0 && len(failed) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("HOW EACH STEP ENDED:\n")
	switch {
	case len(produced) > 0:
		sb.WriteString(strings.Join(produced, "\n") + "\n")
	case len(earlier) > 0:
		sb.WriteString("(nothing has run yet in this round)\n")
	default:
		sb.WriteString("(no step has produced anything yet)\n")
	}
	if len(earlier) > 0 {
		sb.WriteString("\nEARLIER ROUNDS, ALREADY BEEN THROUGH:\n")
		sb.WriteString(strings.Join(earlier, "\n") + "\n")
	}
	if len(failed) > 0 {
		sb.WriteString("\nSTEPS THAT DID NOT COMPLETE:\n")
		sb.WriteString(strings.Join(failed, "\n") + "\n")
	}
	sb.WriteString("\nALREADY IN HAND, NOT YET FOLLOWED UP:\n")
	if len(unused) == 0 {
		sb.WriteString("(nothing)\n")
	} else {
		sb.WriteString(strings.Join(unused, "\n") + "\n")
	}
	return sb.String(), nil
}

// stepLabel names a step: the planner's label and the tool that ran, or just
// the tool when the planner set no label. Printing both when they are the same
// word gives "file_read (file_read)", which reads as two facts and is one.
func stepLabel(n *Node) string {
	if n.Tag == "" || n.Tag == n.ToolName {
		return n.ToolName
	}
	return fmt.Sprintf("%s (%s)", n.Tag, n.ToolName)
}

/*
 * EdgeReFrame describes what a run has done so far, for the stage about to act
 * on it.
 * desc: One gate call for the material, one model call to write it. The gate
 *       assembles the request's evidence, how each step ended, and the timeline
 *       — in that order of priority, under a budget, so a single long tool
 *       result cannot crowd out the rest. Trimming is the gate's and it says
 *       when it trims.
 *
 *       Three stages read one of these and each does something different with
 *       it: one chooses whether to keep working, one writes the answer, one
 *       judges how serious the result is. reader is the sentence that says
 *       which, and it is the only thing that differs between them. One prompt
 *       with a slot rather than three prompts, because three prompts written
 *       days apart is how the two edges this replaces came to disagree.
 *
 *       Nothing here names a kind of value. The material says a step produced
 *       something or did not; whether that something is a link, a process id or
 *       an advisory is read off the evidence by the model, and written in the
 *       words the request used. The edge this replaces hard-coded one
 *       application's answer to that question into the engine.
 *
 *       Fails open at every step: no gate, no model, or a failed call, and the
 *       stage still gets the material. A stage never loses its account of the
 *       run because the wording could not be written.
 * param: ctx - the run's context.
 * param: graph - the run so far.
 * param: request - what was asked, as the calling stage renders it.
 * param: reader - what the reading stage is about to do, as a verb phrase
 *        completing "a stage that is about to …".
 * return: the block to prepend, or "" when nothing has run.
 */
func (a *Agent) EdgeReFrame(ctx context.Context, graph *Graph, request, reader string) string {
	material := a.reframeMaterial(ctx, graph, request)
	if material == "" {
		return "" // nothing has run; there is no situation to describe
	}

	// Only to ask whether a model is configured at all. Which model, and the
	// size of the reply, are the door's to settle.
	if client, _ := a.lightLane(ctx); client == nil {
		return reframeHeading + "\n\n" + material
	}

	// ask, not askParsed. This writes prose, and a paragraph that ran into the
	// cap is short rather than unusable — reporting that as an error would throw
	// away a good paragraph and fall back to the bare material.
	resp, err := a.ask(withTrace(ctx, TraceID{
		NodeType: "reframe",
		Tag:      "reframe",
		Input:    map[string]string{"reader": reader},
	}), Light, &llm.ChatRequest{
		// The arcs, not only the prose about them. This stage carries: it takes
		// what the nodes produced and forms it for the next one to read, and its
		// paragraph is placed FIRST in that stage's prompt. Given prose alone it
		// can only pass on what the prose says — so when a worklog line showed a
		// complete 74-byte result with a truncation marker on it, this stage
		// reported the data as incomplete and every stage downstream agreed.
		//
		// The material stays: it is the summary this stage is asked to rewrite.
		// The arcs are what let it check that summary against what actually came
		// back.
		Messages: BuildMessagesWithResults(
			fmt.Sprintf(prompt.Reframe, reader), material, nil, graph.Arcs()),
		Temperature: 0.2,
		MaxTokens:   a.replyBudget(replyEdgeBudget),
	})
	// An edge carries; this is what it carried. Recorded whether the model
	// answered or not, because a reframe that fell back to passing the material
	// through is exactly the case a reader of these records wants to see — see
	// debugrecord.go.
	rec := DebugRecord{
		ID: "reframe:" + reader, Kind: "edge", Label: reader, Round: graph.Round(),
		System: fmt.Sprintf(prompt.Reframe, reader), User: material,
	}
	if err != nil || resp == nil || len(resp.Choices) == 0 ||
		strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
		log.Printf("[reframe] no reframe written, passing the material through: %v", err)
		if err != nil {
			rec.Err = err.Error()
		}
		rec.Text = material
		graph.recordStage(rec)
		return reframeHeading + "\n\n" + material
	}
	written := strings.TrimSpace(resp.Choices[0].Message.Content)
	rec.Reply, rec.Text = written, written
	rec.TokensIn, rec.TokensOut = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	graph.recordStage(rec)
	return reframeHeading + "\n\n" + written
}

/*
 * reframeMaterial gathers what the model is asked to describe.
 * desc: Through the gate, in priority order and under a budget: how each step
 *       ended first, because it is what the evidence does not say; then the
 *       evidence itself, because a reframe with no content can only report that
 *       a step "produced a result" and hedge about what; then the timeline.
 *
 *       A graph with no gate still yields the outcomes, read directly. That is
 *       the case a caller assembling a graph by hand is in.
 * param: ctx, graph, request - as EdgeReFrame.
 * return: the material, empty when no step has ended.
 */
func (a *Agent) reframeMaterial(ctx context.Context, graph *Graph, request string) string {
	outcomes := ""
	if graph != nil && graph.Context != nil {
		resp, err := graph.Context.Get(ctx, ContextRequest{
			ReturnSources: Sources(
				StepOutcomes(),
				NodeReturns("all"),
				Worklog(20, "all"),
			),
			MaxBudget:       6000,
			OmitCurrentTime: true,
		})
		if err == nil {
			var sb strings.Builder
			for _, name := range []string{SourceStepOutcomes, SourceNodeReturns, SourceWorklog} {
				if v := strings.TrimSpace(resp.Sources[name]); v != "" {
					if name == SourceNodeReturns {
						sb.WriteString("WHAT THE STEPS RETURNED:\n")
					}
					if name == SourceWorklog {
						sb.WriteString("WHAT HAS BEEN DONE ALREADY:\n")
					}
					sb.WriteString(v + "\n\n")
				}
			}
			outcomes = sb.String()
		} else {
			log.Printf("[reframe] the gate could not assemble the run: %v", err)
		}
	}
	if outcomes == "" {
		// No gate, or it gave nothing back. The outcomes alone still describe a
		// run, and they are the half nothing else carries.
		s := &stepOutcomesSource{}
		v, _ := s.Load(graph, nil, a, nil)
		outcomes = strings.TrimSpace(v)
	}
	if strings.TrimSpace(outcomes) == "" {
		return ""
	}
	return "REQUEST:\n" + strings.TrimSpace(request) + "\n\n" + strings.TrimSpace(outcomes)
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
