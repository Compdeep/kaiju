package llm

import (
	"encoding/json"
	"errors"
	"testing"
)

// The reflector's reply on run 77f395aa (2026-11-21), shortened: one complete
// decision, then stray text and the same decision again until the cap.
const runaway = `{"decision":"replan","summary":"no distance yet","next":"query Titan"} 
} 
<style> .json { } </style> 
{"decision":"replan","summary":"no distance yet","next":"query Titan"} 
<style> .json { } </style> 
{"decision":"replan","summa`

func forcedReflect() *ChatRequest {
	return &ChatRequest{
		MaxTokens:  8192,
		Tools:      []ToolDef{{Type: "function", Function: FunctionDef{Name: "reflect", Parameters: json.RawMessage(`{"type":"object"}`)}}},
		ToolChoice: "required",
	}
}

func cut(args string) *ChatResponse {
	return &ChatResponse{Choices: []Choice{{FinishReason: "length", Message: Message{Role: "assistant",
		ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: FunctionCall{Name: "reflect", Arguments: args}}}}}}}
}

// A forced reply that finished its object and then ran to the cap keeps the
// object and is no longer an error.
func TestACappedReplyWithACompleteObjectIsKept(t *testing.T) {
	resp := cut(runaway)
	if !KeepCappedObject(forcedReflect(), resp) {
		t.Fatal("the complete decision was not kept")
	}
	got := resp.Choices[0].Message.ToolCalls[0].Function.Arguments
	if got != `{"decision":"replan","summary":"no distance yet","next":"query Titan"}` {
		t.Errorf("kept %q", got)
	}
	if Truncated(resp) {
		t.Error("the kept reply still reads as truncated")
	}
}

// A reply cut before its object closed is still cut: there is no answer to keep.
func TestACappedReplyStillOpenIsAnError(t *testing.T) {
	for name, args := range map[string]string{
		"open object": `{"decision":"replan","summary":"no dist`,
		"not object":  `["replan"]`,
		"prose":       `I think we should replan`,
	} {
		resp := cut(args)
		if KeepCappedObject(forcedReflect(), resp) {
			t.Errorf("%s: kept %q", name, resp.Choices[0].Message.ToolCalls[0].Function.Arguments)
		}
		if !Truncated(resp) {
			t.Errorf("%s: no longer reads as truncated", name)
		}
	}
}

// Only a forced shape is rescued: a free reply cut at the cap is a short
// answer, and its caller decides what to do with it.
func TestACappedFreeReplyIsNotRescued(t *testing.T) {
	req := forcedReflect()
	req.ToolChoice = "auto"
	if KeepCappedObject(req, cut(runaway)) {
		t.Error("a reply that was not forced to one shape was rescued")
	}
}

// A schema reply in the content is rescued the same way.
func TestACappedSchemaReplyIsKept(t *testing.T) {
	req := &ChatRequest{MaxTokens: 100, ResponseFormat: &ResponseFormat{Type: "json_schema"}}
	resp := &ChatResponse{Choices: []Choice{{FinishReason: "length", Message: Message{Role: "assistant", Content: "  " + runaway}}}}
	if !KeepCappedObject(req, resp) {
		t.Fatal("the complete object was not kept")
	}
	if c := resp.Choices[0].Message.Content; c != `{"decision":"replan","summary":"no distance yet","next":"query Titan"}` {
		t.Errorf("kept %q", c)
	}
	if err := TruncationError(1); !errors.Is(err, ErrReplyTruncated) {
		t.Fatal(err)
	}
}
