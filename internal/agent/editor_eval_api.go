package agent

import "github.com/Compdeep/kaiju/internal/agent/llm"

// EditorEvalResult is the parsed coder tool-call response, stripped down
// to what an editor-layer harness needs. Production compute code goes
// through computeCode, which wraps this same parsing with extra plumbing.
type EditorEvalResult struct {
	Language string   `json:"language"`
	Filename string   `json:"filename"`
	Code     string   `json:"code,omitempty"`
	Edits    []EditOp `json:"edits,omitempty"`
}

// EditorEvalBundle returns the coder's real system prompt and tool
// definition so external eval harnesses can exercise the editor without
// duplicating prompts (which would silently drift from production).
func EditorEvalBundle() (systemPrompt string, toolDef llm.ToolDef) {
	return baseComputeCoderPrompt, coderToolDef()
}

// EditorEvalExtractToolArgs unwraps the tool call arguments (or content
// fallback) from an LLM response using the exact same logic production
// uses in the coder path.
func EditorEvalExtractToolArgs(resp *llm.ChatResponse) (string, error) {
	return extractToolArgs(resp)
}

// EditorEvalParseResponse parses raw coder tool-call JSON into the
// stripped-down EditorEvalResult.
func EditorEvalParseResponse(raw string) (EditorEvalResult, error) {
	var r EditorEvalResult
	if err := ParseLLMJSON(raw, &r); err != nil {
		return EditorEvalResult{}, err
	}
	return r, nil
}
