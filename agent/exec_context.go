package agent

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/llm"
)

// Reaching the run's state from a tool.
//
// Two things about a tool are independent: what it returns, and what it needs
// to do its work. Most tools need nothing beyond their arguments. A few —
// compute, edit_file — run their own model calls and add nodes to the graph, so
// they need the graph, the budget and the clients.
//
// Those were once the same question, because the only way to receive the run's
// state was to implement an interface whose method returned a string. A tool
// could have the state or return a typed message, never both, and the
// dispatcher's if/else silently chose the typed branch and left the state
// unbuilt.
//
// So the state travels on the ctx instead, the way interjections and vision
// images already do. Every tool declares one method and fetches what it needs.

type execContextKey struct{}

/*
 * WithExecContext returns ctx carrying the run state a tool may ask for.
 * desc: Set by the dispatcher before every tool call, so a tool that needs the
 *       graph can reach it whichever execution path it is on.
 * param: ctx - the call's context.
 * param: ec - the state, built once per tool call.
 * return: a ctx carrying it.
 */
func WithExecContext(ctx context.Context, ec *ExecuteContext) context.Context {
	if ec == nil {
		return ctx
	}
	return context.WithValue(ctx, execContextKey{}, ec)
}

/*
 * ExecContextFrom returns the run state a tool was called with.
 * desc: Nil when there is none — a tool invoked outside a DAG run, from the
 *       ReAct loop or a direct call. A tool that needs the graph must say so
 *       rather than dereference: absence means "not part of a run", not
 *       "something went wrong".
 * param: ctx - the ctx passed to Execute or ExecuteTyped.
 * return: the state, or nil.
 */
func ExecContextFrom(ctx context.Context) *ExecuteContext {
	ec, _ := ctx.Value(execContextKey{}).(*ExecuteContext)
	return ec
}

/*
 * ExecuteContext carries runtime references to tools that need more than
 * plain (ctx, params) can provide.
 * desc: Tools like compute run a sub-pipeline (architect + parallel coders)
 *       that needs access to the live graph, budget, LLM clients, workspace,
 *       and intent. The standard Tool interface passes only (ctx, params), so
 *       the dispatcher builds this struct from scheduler-held state and puts it
 *       on the ctx, where ExecContextFrom reaches it.
 */
type ExecuteContext struct {
	Ctx        context.Context
	Node       *Node
	Graph      *Graph
	Budget     *Budget
	LLM        *llm.Client // reasoning model
	Executor   *llm.Client // executor model
	Workspace  string
	TriggerID  string
	Intent     gates.Intent
	SkillCards map[string]string // phase 2: resolved architect/coder guidance

	// cardNames is which cards actually contributed guidance, recorded on the
	// node by the tools that consume it. Unexported: it is bookkeeping for the
	// trace, not something a tool should read.
	cardNames []string
}

/*
 * resolveSkillCards pulls ## Architect Guidance and ## Coder Guidance
 * sections from every classifier-active card/skill that has them.
 * desc: Iterates the given card keys and looks each up in both the capability
 *       card registry and the skill cards registry. Extracts any
 *       "## Architect Guidance" and "## Coder Guidance" sections, prefixes
 *       each with "### <name>" so multiple sources compose cleanly, and
 *       returns both the concatenated text (for prompt injection) and the
 *       list of contributing skill names (for node attribution / UI).
 * param: a - the agent (for capabilities and skillGuidance registries).
 * return: SkillCards map with "architect"/"coder" keys, and a slice of
 *         contributing skill names. Both nil/empty if nothing applies.
 */
// resolveComputeSkillCards extracts architect/coder guidance for the given
// list of skill keys. Caller passes the cards (typically graph.ActiveCards).
func (a *Agent) resolveComputeSkillCards(cards []string) (map[string]string, []string) {
	if len(cards) == 0 {
		return nil, nil
	}
	var architectParts, coderParts []string
	var contributed []string
	for _, key := range cards {
		body, name := a.lookupGuidanceBody(key)
		if body == "" {
			continue
		}
		arch := Text.ExtractSection(body, "## Architect Guidance")
		rules := Text.ExtractSection(body, "## RULES")
		if rules != "" {
			arch += "\n\n## RULES\n" + rules
		}
		code := Text.ExtractSection(body, "## Coder Guidance")
		if arch == "" && code == "" {
			continue
		}
		contributed = append(contributed, name)
		if arch != "" {
			architectParts = append(architectParts, fmt.Sprintf("### %s\n%s", name, arch))
		}
		if code != "" {
			coderParts = append(coderParts, fmt.Sprintf("### %s\n%s", name, code))
		}
	}
	if len(contributed) == 0 {
		log.Printf("[dag] compute: no skill guidance matched for architect/coder (cards=%v)", cards)
		return nil, nil
	}
	out := make(map[string]string)
	if len(architectParts) > 0 {
		out["architect"] = strings.Join(architectParts, "\n\n")
		log.Printf("[dag] compute: injected architect guidance from %v (%d chars)", contributed, len(out["architect"]))
	}
	if len(coderParts) > 0 {
		out["coder"] = strings.Join(coderParts, "\n\n")
	}
	return out, contributed
}

/*
 * lookupGuidanceBody resolves a classifier key against both the capability
 * card registry and the skill cards registry.
 * desc: Returns the markdown body and display name for the key. Capability
 *       cards take precedence if both exist.
 * param: key - the classifier-returned key.
 * return: body markdown and display name, or empty strings if not found.
 */
func (a *Agent) lookupGuidanceBody(key string) (string, string) {
	if s, ok := a.skillGuidance[key]; ok {
		return s.Body(), s.Name()
	}
	return "", ""
}

/*
 * composeGuidance concatenates the guidance bodies for a run's selected keys.
 * desc: Resolves each key the one way keys are resolved — through
 *       lookupGuidanceBody, which knows about both registries. A stage that
 *       reaches into one registry itself works until the guidance it needs was
 *       registered in the other, and then it silently composes nothing: the run
 *       proceeds with a prompt that is complete apart from the domain knowledge
 *       it was supposed to bring.
 * param: keys - the keys selected for this run.
 * return: the bodies, each followed by a blank line, or "" when none matched.
 */
func (a *Agent) composeGuidance(keys []string) string {
	var sb strings.Builder
	for _, key := range keys {
		if body, _ := a.lookupGuidanceBody(key); body != "" {
			sb.WriteString(body)
			sb.WriteString("\n\n")
		}
	}
	return sb.String()
}

/*
 * guidanceKeys returns every key that resolves to guidance, from either store.
 * desc: Sorted, so a prompt built from all of them reads the same way twice.
 *       Used when a run selected nothing and every piece of guidance applies.
 * return: the keys.
 */
func (a *Agent) guidanceKeys() []string {
	if a == nil {
		return nil
	}
	keys := make([]string, 0, len(a.skillGuidance))
	for k := range a.skillGuidance {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
