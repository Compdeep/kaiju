package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/user/kaiju/internal/agent/llm"
)

const classifierSystemPrompt = `You are a query classifier. Given a user query and a list of capability domains, select which domains are relevant to addressing the query.

Available domains:
%s
Select 1-3 domains. If uncertain, include general_reasoning.
Output ONLY JSON: {"select": ["key1", "key2"]}`

/*
 * classifierOutput is the parsed response from the classifier LLM call.
 * desc: Contains the list of selected capability card keys.
 */
type classifierOutput struct {
	Select []string `json:"select"`
}

/*
 * classifyCapabilities makes a lightweight LLM call to determine which
 * capability cards and guidance skills are relevant to the user's query.
 * desc: Sends the query to the LLM with a classifier system prompt listing
 *       the union of embedded capability cards and user-installed guidance
 *       skills (SkillMD files without command_dispatch). Parses the JSON
 *       response and validates keys against both registries. Falls back to
 *       the full union on any failure.
 * param: ctx - context for the LLM call.
 * param: query - the user query text to classify.
 * return: slice of selected keys (may resolve to either a capability card or
 *         a guidance skill at lookup time).
 */
func (a *Agent) classifyCapabilities(ctx context.Context, query string) []string {
	manifest := a.buildClassifierManifest()
	if manifest == "" {
		return nil
	}

	sysPrompt := fmt.Sprintf(classifierSystemPrompt, manifest)

	// Classifier uses the executor (mini) model, not the reasoning model.
	// The task is a structured multi-label pick from a short manifest —
	// well within mini capability, and mini is several times faster.
	resp, err := a.executor.Complete(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: query},
		},
		Temperature: 0.0,
		MaxTokens:   128,
	})
	if err != nil {
		log.Printf("[dag] classifier failed, using all cards: %v", err)
		return a.allGuidanceKeys()
	}

	if len(resp.Choices) == 0 {
		log.Printf("[dag] classifier returned no choices, using all cards")
		return a.allGuidanceKeys()
	}

	raw := resp.Choices[0].Message.Content
	cleaned := Text.StripCodeFence(raw)

	var out classifierOutput
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		log.Printf("[dag] classifier parse failed (%v), using all cards", err)
		return a.allGuidanceKeys()
	}

	// Validate keys against both registries (capability cards + guidance skills)
	var valid []string
	for _, key := range out.Select {
		if _, ok := a.capabilities[key]; ok {
			valid = append(valid, key)
			continue
		}
		if _, ok := a.skillGuidance[key]; ok {
			valid = append(valid, key)
			continue
		}
		log.Printf("[dag] classifier returned unknown key %q, skipping", key)
	}

	if len(valid) == 0 {
		log.Printf("[dag] classifier selected no valid keys, using all")
		return a.allGuidanceKeys()
	}

	return valid
}

/*
 * buildClassifierManifest returns a single "- key: description" listing
 * that unions embedded capability cards and SkillMD guidance skills.
 * desc: The classifier sees both kinds as a flat list of candidates. Name
 *       collisions between the two registries are unlikely in practice;
 *       if one occurs, the capability card takes precedence (listed first).
 * return: manifest string for the classifier prompt, or empty if neither
 *         registry has content.
 */
func (a *Agent) buildClassifierManifest() string {
	if len(a.capabilities) == 0 && len(a.skillGuidance) == 0 {
		return ""
	}
	var sb strings.Builder
	seen := make(map[string]bool)
	for _, card := range a.capabilities {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", card.Key, card.Description))
		seen[card.Key] = true
	}
	for name, s := range a.skillGuidance {
		if seen[name] {
			continue
		}
		sb.WriteString(fmt.Sprintf("- %s: %s\n", name, s.Description()))
	}
	return sb.String()
}

/*
 * allGuidanceKeys returns the union of capability card keys and guidance
 * skill names as a fallback when classifier selection fails.
 * return: slice of all guidance keys.
 */
func (a *Agent) allGuidanceKeys() []string {
	out := make([]string, 0, len(a.capabilities)+len(a.skillGuidance))
	seen := make(map[string]bool)
	for k := range a.capabilities {
		out = append(out, k)
		seen[k] = true
	}
	for name := range a.skillGuidance {
		if !seen[name] {
			out = append(out, name)
		}
	}
	return out
}
