package agent

import (
	"encoding/json"
	"fmt"
)

// MicroPlannerBody is the typed output of a micro-planner (clean-room debugger)
// node: a diagnosis summary plus fix-plan steps. Raw keeps the original JSON so
// Field/Evidence match legacy behavior and the scheduler's graft re-parse plus
// the reflector's debug-history read (both on the raw string) stay unchanged.
type MicroPlannerBody struct {
	Out microPlannerOutput
	Raw string
}

func parseMicroPlannerBody(raw string) MicroPlannerBody {
	var out microPlannerOutput
	_ = json.Unmarshal([]byte(raw), &out)
	return MicroPlannerBody{Out: out, Raw: raw}
}

func (b MicroPlannerBody) Field(path string) (any, bool) { return RawText(b.Raw).Field(path) }
func (b MicroPlannerBody) Evidence() string              { return b.Raw }
func (b MicroPlannerBody) Summary() string {
	if b.Out.Summary != "" {
		return "fix: " + Text.TruncateLog(b.Out.Summary, 150)
	}
	if len(b.Out.Nodes) > 0 {
		return fmt.Sprintf("%d fix step(s)", len(b.Out.Nodes))
	}
	return RawText(b.Raw).Summary()
}

// ObserverBody is the typed output of an observer node: an action decision
// (continue/inject/cancel/reflect) with its reason and any injected/cancelled
// nodes. Raw preserves the JSON the scheduler's parseObserverOutput re-parses.
type ObserverBody struct {
	Out observerOutput
	Raw string
}

func parseObserverBody(raw string) ObserverBody {
	var out observerOutput
	_ = json.Unmarshal([]byte(raw), &out)
	return ObserverBody{Out: out, Raw: raw}
}

func (b ObserverBody) Field(path string) (any, bool) { return RawText(b.Raw).Field(path) }
func (b ObserverBody) Evidence() string              { return b.Raw }
func (b ObserverBody) Summary() string {
	if b.Out.Action == "" {
		return RawText(b.Raw).Summary()
	}
	if b.Out.Reason != "" {
		return b.Out.Action + ": " + Text.TruncateLog(b.Out.Reason, 150)
	}
	return b.Out.Action
}

// HolmesBody is the typed output of a single Holmes (RCA) iteration: the
// reasoning, current hypothesis, diagnostic actions, and — on conclude — the
// RCAReport. The investigation trace across iterations lives in HolmesState;
// this body is one iteration, per the per-iteration design. Raw preserves the
// JSON the scheduler's parseHolmesOutput and the frontend's
// JSON.parse(node.result) both read.
type HolmesBody struct {
	Out holmesOutput
	Raw string
}

func parseHolmesBody(raw string) HolmesBody {
	var out holmesOutput
	_ = json.Unmarshal([]byte(raw), &out)
	return HolmesBody{Out: out, Raw: raw}
}

func (b HolmesBody) Field(path string) (any, bool) { return RawText(b.Raw).Field(path) }
func (b HolmesBody) Evidence() string              { return b.Raw }
func (b HolmesBody) Summary() string {
	if b.Out.Conclude && b.Out.RCA != nil && b.Out.RCA.RootCause != "" {
		return "RCA: " + Text.TruncateLog(b.Out.RCA.RootCause, 150)
	}
	if b.Out.Hypothesis != "" {
		return "hypothesis: " + Text.TruncateLog(b.Out.Hypothesis, 150)
	}
	if b.Out.Reasoning != "" {
		return Text.TruncateLog(b.Out.Reasoning, 150)
	}
	return RawText(b.Raw).Summary()
}
