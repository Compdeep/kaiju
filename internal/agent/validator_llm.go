package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/llm"
)

// validatorClassifierPrompt is the system prompt for the LLM validator.
// Short on purpose — cheaper to cache, faster to classify.
const validatorClassifierPrompt = `You classify the output of a validator step that ran after a coder wrote code.

Return JSON only: {"pass": bool, "reason": "<one short sentence>"}.

- "pass: true"  — output shows the target ran successfully (e.g. HTTP 200 body, test summary with zero failures, build succeeded).
- "pass: false" — output shows any runtime error, crash, warning-that-aborts, failed assertion, connection refused, or empty response from a server that should have replied.

Treat warnings that do not abort execution as pass. Treat any uncaught exception, stack trace, or "error:"-style message as fail regardless of the language.

No markdown, no code fences, no commentary. JSON object only.`

// validatorLLMResp is the expected shape of the classifier response.
type validatorLLMResp struct {
	Pass   bool   `json:"pass"`
	Reason string `json:"reason"`
}

// classifyValidatorOutput asks the executor LLM whether captured output
// indicates failure. Returns (failed, reason, err). Callers that see err
// should fall back to the heuristic looksLikeFailure.
//
// Capped at 10s and 4KB of output; truncates the middle on longer output.
func (a *Agent) classifyValidatorOutput(ctx context.Context, tag, output string) (bool, string, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		// Empty output from a validator = nothing served = failure.
		// No LLM needed.
		return true, "empty validator output", nil
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	userPrompt := fmt.Sprintf("Validator tag: %s\n\n--- captured output ---\n%s", tag, Text.HeadTail(trimmed, 2000, 2000))

	resp, err := a.llm.Complete(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: validatorClassifierPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0,
		MaxTokens:   200,
	})
	if err != nil {
		return false, "", fmt.Errorf("llm classifier: %w", err)
	}
	if len(resp.Choices) == 0 {
		return false, "", fmt.Errorf("llm classifier: no choices in response")
	}

	var parsed validatorLLMResp
	if err := ParseLLMJSON(resp.Choices[0].Message.Content, &parsed); err != nil {
		return false, "", fmt.Errorf("llm classifier: parse response: %w", err)
	}

	failed := !parsed.Pass
	return failed, parsed.Reason, nil
}

// validatorFailed decides whether the validator's captured output represents
// a failure. Uses the LLM classifier as the source of truth; falls back to
// the heuristic looksLikeFailure if the LLM call fails or times out.
// Returns (failed, reason).
func (a *Agent) validatorFailed(ctx context.Context, tag, output string) (bool, string) {
	failed, reason, err := a.classifyValidatorOutput(ctx, tag, output)
	if err == nil {
		return failed, reason
	}
	log.Printf("[dag] validator LLM fallback for %s: %v", tag, err)
	if looksLikeFailure(output) {
		return true, "heuristic: output matches failure indicators"
	}
	return false, ""
}

// Ensure the JSON package is referenced so imports stay if code paths change.
var _ = json.Unmarshal
