package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

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
 * capability cards are relevant to the user's query.
 * desc: Sends the query to the LLM with a classifier system prompt listing
 *       all available capability cards. Parses the JSON response and validates
 *       keys against the registry. Falls back to all cards on any failure.
 * param: ctx - context for the LLM call.
 * param: query - the user query text to classify.
 * return: slice of selected capability card keys.
 */
func (a *Agent) classifyCapabilities(ctx context.Context, query string) []string {
	if len(a.capabilities) == 0 {
		return nil
	}

	manifest := a.capabilities.ClassifierManifest()
	sysPrompt := fmt.Sprintf(classifierSystemPrompt, manifest)

	resp, err := a.llm.Complete(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: query},
		},
		Temperature: 0.0,
		MaxTokens:   128,
	})
	if err != nil {
		log.Printf("[dag] classifier failed, using all cards: %v", err)
		return a.capabilities.AllKeys()
	}

	if len(resp.Choices) == 0 {
		log.Printf("[dag] classifier returned no choices, using all cards")
		return a.capabilities.AllKeys()
	}

	raw := resp.Choices[0].Message.Content
	cleaned := Text.StripCodeFence(raw)

	var out classifierOutput
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		log.Printf("[dag] classifier parse failed (%v), using all cards", err)
		return a.capabilities.AllKeys()
	}

	// Validate keys against registry
	var valid []string
	for _, key := range out.Select {
		if _, ok := a.capabilities[key]; ok {
			valid = append(valid, key)
		} else {
			log.Printf("[dag] classifier returned unknown card %q, skipping", key)
		}
	}

	if len(valid) == 0 {
		log.Printf("[dag] classifier selected no valid cards, using all")
		return a.capabilities.AllKeys()
	}

	return valid
}
