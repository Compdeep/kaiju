package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/llm"
)

// Impact tiers for tool classification (IGX Triad Gate). Values are ranks
// on the same scale as the intent registry's builtin ranks — the gate
// compares impact and intent directly. These ranks are locked by invariant
// (UpdateIntent rejects rank changes on builtins), so tool authors can
// safely hardcode them.
const (
	ImpactObserve = 0   // read-only, no side effects
	ImpactAffect  = 100 // reversible side effects
	ImpactControl = 200 // irreversible / destructive
)

/*
 * Tool is the interface for agent capabilities (compiled builtins and SKILL.md wrappers).
 * desc: Core abstraction that every tool must implement; declares its IGX impact level at compile time.
 */
type Tool interface {
	/*
	 * Name returns the tool's unique identifier.
	 * desc: Returns the canonical name used for registry lookups and LLM function-calling
	 * return: the tool name string
	 */
	Name() string

	/*
	 * Description returns a human-readable summary of the tool.
	 * desc: Provides the description sent to the LLM in the function-calling schema
	 * return: a short description string
	 */
	Description() string

	/*
	 * Parameters returns the JSON Schema for the tool's input parameters.
	 * desc: Supplies the parameter schema in OpenAI function-calling format
	 * return: raw JSON representing the parameter schema
	 */
	Parameters() json.RawMessage

	/*
	 * Impact returns the IGX impact tier for the given params.
	 * desc: Classifies the tool invocation's side-effect severity so the gate can enforce policy
	 * param: params - the parameters that will be passed to Execute
	 * return: impact tier integer (0, 1, or 2; the intent registry maps
	 *         these to ranks on the configured ladder)
	 */
	Impact(params map[string]any) int

	/*
	 * Execute runs the tool with the given parameters.
	 * desc: Performs the tool's action and returns the result as a string
	 * param: ctx - context for cancellation and value propagation
	 * param: params - the input parameters matching the declared schema
	 * return: the result string and any error encountered
	 */
	Execute(ctx context.Context, params map[string]any) (string, error)
}

/*
 * GetImpact returns the tool's declared impact for the given params.
 * desc: Convenience wrapper that calls Impact on the tool
 * param: t - the tool to query
 * param: params - the parameters to evaluate impact for
 * return: the IGX impact tier integer
 */
func GetImpact(t Tool, params map[string]any) int {
	return t.Impact(params)
}

/*
 * Outputter is an optional interface for tools that return structured JSON.
 * desc: Declares output fields so the executive can build valid param_refs chains and the scheduler can validate field paths; tools returning plain text do not implement this
 */
type Outputter interface {
	/*
	 * OutputSchema returns the JSON Schema describing the Execute return value.
	 * desc: Provides the output schema so downstream tools can reference fields via param_refs
	 * return: raw JSON representing the output schema, or nil
	 */
	OutputSchema() json.RawMessage
}

/*
 * GetOutputSchema returns the tool's declared output schema, or nil
 * if the tool does not implement the Outputter interface.
 * desc: Checks whether the tool implements Outputter and returns its schema
 * param: t - the tool to query
 * return: the output JSON Schema or nil if not an Outputter
 */
func GetOutputSchema(t Tool) json.RawMessage {
	if o, ok := t.(Outputter); ok {
		return o.OutputSchema()
	}
	return nil
}

/*
 * Throttled is an optional interface that tools can implement to declare
 * a minimum interval between consecutive invocations.
 * desc: Allows tools to rate-limit themselves; the DAG scheduler enforces the interval per-tool to avoid overwhelming external APIs
 */
type Throttled interface {
	/*
	 * Throttle returns the minimum interval between consecutive invocations.
	 * desc: Declares how long the scheduler must wait between calls to this tool
	 * return: the minimum duration between invocations, or zero for no throttle
	 */
	Throttle() time.Duration
}

/*
 * GetThrottle returns the tool's declared throttle interval, or zero
 * if the tool does not implement the Throttled interface.
 * desc: Checks whether the tool implements Throttled and returns its interval
 * param: t - the tool to query
 * return: the throttle duration or zero if not throttled
 */
func GetThrottle(t Tool) time.Duration {
	if th, ok := t.(Throttled); ok {
		return th.Throttle()
	}
	return 0
}

/*
 * DisplayHint tells the frontend to show tool output in the composable panel.
 * desc: Carries rendering metadata so the frontend knows which panel plugin to use and what content to display
 */
type DisplayHint struct {
	Plugin  string `json:"plugin"`            // panel plugin id
	Title   string `json:"title,omitempty"`   // tab label
	Path    string `json:"path,omitempty"`    // file path (for file-based content)
	Content string `json:"content,omitempty"` // inline content (for ephemeral push)
	Mime    string `json:"mime,omitempty"`    // content type hint
	Line    int    `json:"line,omitempty"`    // scroll-to line (code plugin)
}

/*
 * Displayer is an optional interface for tools that want to push results to the panel.
 * desc: The scheduler checks for this after Execute and emits a panel_show SSE event with the returned hint
 */
type Displayer interface {
	/*
	 * DisplayHint returns panel rendering metadata for this execution.
	 * desc: Produces a hint telling the frontend how to display the tool's output
	 * param: params - the parameters that were passed to Execute
	 * param: result - the string returned by Execute
	 * return: a DisplayHint pointer or nil if no panel display is needed
	 */
	DisplayHint(params map[string]any, result string) *DisplayHint
}

/*
 * GetDisplayHint returns the tool's display hint for the given execution, or nil.
 * desc: Checks whether the tool implements Displayer and returns its hint
 * param: t - the tool to query
 * param: params - the parameters that were passed to Execute
 * param: result - the string returned by Execute
 * return: a DisplayHint pointer or nil if the tool is not a Displayer
 */
func GetDisplayHint(t Tool, params map[string]any, result string) *DisplayHint {
	if d, ok := t.(Displayer); ok {
		return d.DisplayHint(params, result)
	}
	return nil
}

/*
 * ToolMeta is an optional interface for enriched metadata.
 * desc: Provides source and invocability info; only SkillMD wrappers implement this, builtins do not need to
 */
type ToolMeta interface {
	/*
	 * Source returns the origin of the tool.
	 * desc: Identifies whether the tool is a compiled builtin or a SKILL.md wrapper
	 * return: "builtin" or "skillmd"
	 */
	Source() string

	/*
	 * IsUserInvocable reports whether the user can trigger this tool via a slash command.
	 * desc: Determines if the tool is directly callable by the user from the chat input
	 * return: true if the tool supports /slash-command invocation
	 */
	IsUserInvocable() bool
}

/*
 * dataDirKey is the context key for overriding the data directory.
 * desc: Private key type used with context.WithValue to store a data directory path
 */
type dataDirKey struct{}

/*
 * WithDataDir returns a context carrying an alternate data directory.
 * desc: Injects a data directory override into the context for retrieval tools
 * param: ctx - the parent context
 * param: dir - the directory path to store
 * return: a new context containing the directory override
 */
func WithDataDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, dataDirKey{}, dir)
}

/*
 * DataDir returns the data directory override from ctx, or fallback if none set.
 * desc: Extracts the data directory from context, falling back to the provided default
 * param: ctx - the context to check for a directory override
 * param: fallback - the default directory if no override is present
 * return: the data directory path string
 */
func DataDir(ctx context.Context, fallback string) string {
	if v, ok := ctx.Value(dataDirKey{}).(string); ok && v != "" {
		return v
	}
	return fallback
}

/*
 * ToToolDef converts a Tool to an OpenAI function calling ToolDef.
 * desc: Maps the tool's name, description, and parameter schema into the LLM ToolDef format
 * param: t - the tool to convert
 * return: an llm.ToolDef ready for inclusion in a chat completion request
 */
func ToToolDef(t Tool) llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		},
	}
}
