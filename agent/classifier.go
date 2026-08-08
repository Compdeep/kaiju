package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/prompt"
)

// classifierSystemPrompt moved to prompt.Classifier
// (internal/agent/prompt/prompts.md).

/*
 * classifierOutput is the parsed response from the classifier LLM call.
 * desc: Contains the list of selected skill card keys.
 */
type classifierOutput struct {
	Select []string `json:"select"`
}

/*
 * classifyCapabilities makes a lightweight LLM call to determine which
 * skill card and skill cards are relevant to the user's query.
 * desc: Sends the query to the LLM with a classifier system prompt listing
 *       the union of embedded skill card and user-installed guidance
 *       skills (SkillMD files without command_dispatch). Parses the JSON
 *       response and validates keys against both registries. Falls back to
 *       the full union on any failure.
 * param: ctx - context for the LLM call.
 * param: query - the user query text to classify.
 * return: slice of selected keys (may resolve to either a skill card or
 *         a skill cards at lookup time).
 */
func (a *Agent) classifyCapabilities(ctx context.Context, query string) []string {
	manifest := a.buildClassifierManifest()
	if manifest == "" {
		return nil
	}

	sysPrompt := fmt.Sprintf(prompt.Classifier, manifest)

	// Classifier uses the executor (mini) model, not the reasoning model.
	// The task is a structured multi-label pick from a short manifest —
	// well within mini capability, and mini is several times faster.
	resp, err := a.completeLight(ctx, &llm.ChatRequest{
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
	var out classifierOutput
	if err := ParseLLMJSON(raw, &out); err != nil {
		log.Printf("[dag] classifier parse failed (%v), using all cards", err)
		return a.allGuidanceKeys()
	}

	// A key the model invented resolves to nothing, so drop it rather than
	// carry it into a run where every stage would look it up and find nothing.
	var valid []string
	for _, key := range out.Select {
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
 * that unions embedded skill card and SkillMD skill cards.
 * desc: The classifier sees both kinds as a flat list of candidates. Name
 *       collisions between the two registries are unlikely in practice;
 * return: manifest string for the classifier prompt, or empty if neither
 *         registry has content.
 */
func (a *Agent) buildClassifierManifest() string {
	if len(a.skillGuidance) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, key := range a.guidanceKeys() {
		if s, ok := a.skillGuidance[key]; ok {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", key, s.Description()))
		}
	}
	return sb.String()
}

/*
 * allGuidanceKeys returns every guidance key, as a fallback when the
 * classifier's selection cannot be used.
 * return: slice of all guidance keys.
 */
func (a *Agent) allGuidanceKeys() []string {
	out := make([]string, 0, len(a.skillGuidance))
	seen := make(map[string]bool)
	for name := range a.skillGuidance {
		if !seen[name] {
			out = append(out, name)
		}
	}
	return out
}
