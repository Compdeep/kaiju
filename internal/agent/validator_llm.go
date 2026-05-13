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
//
// Philosophy: bash exit-code is the primary contract. This classifier only
// fires when bash already exited 0. Our job is to catch the narrow case where
// a script exited 0 but obviously crashed (unset set -e, unchecked pipes,
// commands that swallow errors). Default to PASS unless something is clearly
// broken. False negatives (passing a flawed script) are recoverable by the
// reflector reading the final evidence; false positives (failing a working
// script) trigger a full Holmes+debugger cycle and waste minutes.
const validatorClassifierPrompt = `You classify validator output. The bash already exited 0 (success). Your only job: did the script actually run, or did it crash early?

Return JSON only: {"pass": bool, "reason": "<one short sentence>"}.

PASS (default) — the script ran. Output looks weird, partial, warning-laden, or unfamiliar — that is still PASS. Examples:
  • JSON with all expected keys
  • build logs ending in success
  • HTTP responses (any status)
  • test runners reporting zero failures (or no test summary at all)
  • lint complaints, deprecation notices, slow performance
  • the word "error" appearing inside otherwise-normal output
  • partial or surprising results

FAIL — only when the output shows the script could not run at all:
  • "command not found" / "No such file or directory" as the whole substance
  • syntax error or import error preventing execution
  • an uncaught exception or stack trace that aborted the program
  • a test runner reporting a non-zero failure count (e.g. "1 failed", "FAIL: …")
  • "Connection refused" when a server was clearly expected to answer

When in doubt, PASS. False fails cost a full debug cycle; false passes are caught later by the reflector.

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
		// Empty output + bash exit 0 = quiet check passed. `grep -q`,
		// `test`, `[ … ]`, `diff -q` etc. are *defined* to be silent on
		// success; treating empty stdout as failure was wrongly flagging
		// every key-existence check as a false positive. Bash exit-code
		// is the source of truth — failed validators come through the
		// isBashError branch in the scheduler, never here.
		return false, "quiet check passed (exit 0, no output)", nil
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
