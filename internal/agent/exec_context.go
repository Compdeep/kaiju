package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/user/kaiju/internal/agent/gates"
	"github.com/user/kaiju/internal/agent/llm"
)

/*
 * ExecuteContext carries runtime references to tools that need more than
 * plain (ctx, params) can provide.
 * desc: Tools like compute run a sub-pipeline (architect + parallel coders)
 *       that needs access to the live graph, budget, LLM clients, workspace,
 *       and intent. The standard Tool interface passes only (ctx, params);
 *       ContextualExecutor extends it for these tools. The dispatcher builds
 *       this struct at execution time from scheduler-held state.
 */
type ExecuteContext struct {
	Ctx        context.Context
	Node       *Node
	Graph      *Graph
	Budget     *Budget
	LLM        *llm.Client // reasoning model
	Executor   *llm.Client // executor model
	Workspace  string
	AlertID    string
	Intent     gates.Intent
	SkillCards map[string]string // phase 2: resolved architect/coder guidance
}

/*
 * ContextualExecutor is an optional interface for tools that need rich
 * runtime context beyond (ctx, params).
 * desc: Tools implementing this are invoked via ExecuteWithContext by the
 *       dispatcher; tools not implementing it fall through to the normal
 *       Execute path. Currently only ComputeTool implements this.
 */
type ContextualExecutor interface {
	ExecuteWithContext(ec *ExecuteContext, params map[string]any) (string, error)
}

/*
 * resolveSkillCards pulls ## Architect Guidance and ## Coder Guidance
 * sections from every classifier-active card/skill that has them.
 * desc: Iterates a.activeCards and looks each key up in both the capability
 *       card registry and the guidance skill registry. Extracts any
 *       "## Architect Guidance" and "## Coder Guidance" sections, prefixes
 *       each with "### <name>" so multiple sources compose cleanly, and
 *       returns both the concatenated text (for prompt injection) and the
 *       list of contributing skill names (for node attribution / UI).
 * param: a - the agent (for capabilities and skillGuidance registries).
 * return: SkillCards map with "architect"/"coder" keys, and a slice of
 *         contributing skill names. Both nil/empty if nothing applies.
 */
func (a *Agent) resolveComputeSkillCards() (map[string]string, []string) {
	if len(a.activeCards) == 0 {
		return nil, nil
	}
	var architectParts, coderParts []string
	var contributed []string
	for _, key := range a.activeCards {
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
		log.Printf("[dag] compute: no skill guidance matched for architect/coder (activeCards=%v)", a.activeCards)
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
 * card registry and the guidance skill registry.
 * desc: Returns the markdown body and display name for the key. Capability
 *       cards take precedence if both exist.
 * param: key - the classifier-returned key.
 * return: body markdown and display name, or empty strings if not found.
 */
func (a *Agent) lookupGuidanceBody(key string) (string, string) {
	if card, ok := a.capabilities[key]; ok {
		return card.Body, card.Key
	}
	if s, ok := a.skillGuidance[key]; ok {
		return s.Body(), s.Name()
	}
	return "", ""
}
