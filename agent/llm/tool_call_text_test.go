package llm

import "testing"

// A model that writes its call as <tool_call> text, which a host then hands
// back whole as the call's arguments, has the call's own arguments read out.
func TestToolCallTextIsUnwrapped(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"tagged":         {"\n\n<tool_call>\n{\"name\": \"plan\", \"arguments\": {\"steps\": []}}\n</tool_call>\n", `{"steps": []}`},
		"extra brace":    {"<tool_call>\n{\"name\": \"plan\", \"arguments\": {\"steps\": []}}}\n</tool_call>", `{"steps": []}`},
		"unclosed tag":   {"<tool_call>{\"name\": \"plan\", \"arguments\": {\"a\": 1}}", `{"a": 1}`},
		"bare arguments": {"<tool_call>{\"steps\": []}</tool_call>", `{"steps": []}`},
		"already JSON":   {`{"steps": [1]}`, `{"steps": [1]}`},
		"not a call":     {"I cannot plan this.", "I cannot plan this."},
	} {
		m := Message{ToolCalls: []ToolCall{{Function: FunctionCall{Name: "plan", Arguments: c.in}}}}
		tidyReply(&m)
		if got := m.ToolCalls[0].Function.Arguments; got != c.want {
			t.Errorf("%s: arguments = %q, want %q", name, got, c.want)
		}
	}
}

// Thinking written into the answer is still lifted out.
func TestTidyReplyStillLiftsThinking(t *testing.T) {
	m := Message{Content: "<think>hmm</think>answer"}
	tidyReply(&m)
	if m.Content != "answer" || m.Reasoning != "hmm" {
		t.Errorf("content %q reasoning %q", m.Content, m.Reasoning)
	}
}
