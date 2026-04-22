package agent

import (
	"encoding/json"
	"fmt"

	"github.com/Compdeep/kaiju/internal/agent/llm"
)

// ── Function-calling helpers ────────────────────────────────────────────────
//
// Every LLM caller that needs structured JSON output uses function calling
// instead of plain JSON parsing. The model is given a single tool with the
// expected output schema and forced (ToolChoice="required") to call it. The
// returned tool_calls[0].arguments is guaranteed to match the schema thanks
// to grammar-constrained decoding on modern providers.
//
// This eliminates the entire class of "model emitted free-form JSON that
// doesn't match our struct" parser bugs.

// extractToolArgs returns the arguments string from the first tool call in
// the response, or falls back to the message content if the model didn't
// call the tool. Errors only when the response is structurally empty.
func extractToolArgs(resp *llm.ChatResponse) (string, error) {
	if resp == nil || len(resp.Choices) == 0 {
		return "", fmt.Errorf("llm response: no choices")
	}
	choice := resp.Choices[0]
	if len(choice.Message.ToolCalls) > 0 {
		return choice.Message.ToolCalls[0].Function.Arguments, nil
	}
	// Fallback: model didn't call the tool, returned plain content.
	// Some providers/models occasionally do this; let the caller's
	// existing JSON parser try to handle it.
	if choice.Message.Content != "" {
		return choice.Message.Content, nil
	}
	return "", fmt.Errorf("llm response: empty content and no tool calls")
}

// ── Tool definitions ────────────────────────────────────────────────────────
//
// Each parser has a corresponding ToolDef returning the JSON schema for its
// output. The schemas mirror the Go structs in compute.go / reflection.go /
// etc., but in JSON Schema form so the LLM provider can constrain decoding.

func curatorToolDef() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "submit_summary",
			Description: "Submit the curated summary of relevant context.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"summary": {
						"type": "string",
						"description": "Verbatim relevant content extracted from the sources, ordered by relevance to the query. Empty if nothing relevant."
					}
				},
				"required": ["summary"]
			}`),
		},
	}
}

func reflectorToolDef() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "submit_decision",
			Description: "Submit the reflection decision.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"decision": {
						"type": "string",
						"enum": ["continue", "conclude", "investigate"],
						"description": "What to do next."
					},
					"progress": {
						"type": "string",
						"enum": ["productive", "diminishing"],
						"description": "How the recent cycles are trending. Defaults to 'productive' if unsure. Two consecutive 'diminishing' waves downgrade investigate→conclude; see prompt for rules."
					},
					"summary": {
						"type": "string",
						"description": "What happened, current state, and SPECIFIC error messages from failures (exact module names, paths, error text)."
					},
					"problem": {
						"type": "string",
						"description": "Only if investigate: the root problem for Holmes — include exact error messages, file paths, module names from the failure output."
					},
					"verdict": {
						"type": "string",
						"description": "Only if conclude: final answer for the user."
					},
					"aggregate": {
						"type": "boolean",
						"description": "Only if conclude: whether the aggregator should run on the verdict."
					}
				},
				"required": ["decision", "summary"]
			}`),
		},
	}
}

func observerToolDef() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "submit_observation",
			Description: "Submit the observer's decision about the completed node.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"action": {
						"type": "string",
						"enum": ["continue", "inject", "cancel", "reflect"]
					},
					"reason": {"type": "string"},
					"nodes": {
						"type": "array",
						"description": "When action=inject: new steps to add. Each is {tool, params, depends_on, tag}.",
						"items": {
							"type": "object",
							"properties": {
								"tool": {"type": "string"},
								"params": {"type": "object"},
								"depends_on": {"type": "array", "items": {"type": "integer"}},
								"tag": {"type": "string"}
							}
						}
					},
					"cancel": {
						"type": "array",
						"description": "When action=cancel: tags or IDs to cancel.",
						"items": {"type": "string"}
					}
				},
				"required": ["action", "reason"]
			}`),
		},
	}
}

// holmesToolDef defines the function-calling schema for one iteration of the
// Holmes investigator. The model emits ONE thought + one or more actions OR a
// final conclusion. Actions run in parallel; Holmes sees all results on the
// next iteration.
func holmesToolDef() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "submit_investigation",
			Description: "Submit one Holmes investigation iteration: a thought + one or more actions, or a final RCA conclusion.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"reasoning": {
						"type": "string",
						"description": "Holmes-style first-person prose explaining the current state of the investigation. One paragraph, max ~200 words. This is what Watson reads and what is fed back to you on the next iteration."
					},
					"hypothesis": {
						"type": "string",
						"description": "Your current working theory in one line, plain English. When concluding, this is the proven root cause."
					},
					"actions": {
						"type": "array",
						"description": "Read-only diagnostic actions to perform in parallel. Each action runs as its own tool call; you see all results on the next iteration. Set to empty array or omit when concluding.",
						"items": {
							"type": "object",
							"properties": {
								"tool": {"type": "string", "description": "Tool name from the available tools list"},
								"params": {"type": "object", "description": "Tool parameters wrapped as a key-value object. ALWAYS wrap inside params, never put fields at the action top level. Example for bash: {\"command\": \"cat .services/backend.err.log\"}. Example for service: {\"action\": \"logs\", \"name\": \"backend\", \"stream\": \"err\"}. Example for file_read: {\"path\": \"/path/to/file\"}. Example for process_list: {\"filter\": \"node\", \"limit\": 20}. Required params for the chosen tool MUST be present."}
							},
							"required": ["tool", "params"]
						}
					},
					"conclude": {
						"type": "boolean",
						"description": "True when you have enough evidence to name the root cause, OR when you've exhausted reasonable hypotheses. False to continue investigating."
					},
					"rca": {
						"type": ["object", "null"],
						"description": "The final root-cause analysis. Set ONLY when conclude is true; null otherwise.",
						"properties": {
							"root_cause": {
								"type": "string",
								"description": "One-sentence statement of the underlying defect. Not a symptom."
							},
							"evidence": {
								"type": "array",
								"items": {"type": "string"},
								"description": "List of observed facts that support the root cause. Each entry is a concrete observation, not a guess."
							},
							"confidence": {
								"type": "string",
								"enum": ["high", "medium", "low"],
								"description": "How certain you are. Use low when you ran out of iterations or cannot prove the theory."
							},
							"suggested_strategy": {
								"type": "string",
								"description": "One paragraph for the fix planner: what kind of change is needed (architectural direction), not the exact code."
							}
						}
					}
				},
				"required": ["reasoning", "hypothesis", "conclude"]
			}`),
		},
	}
}

func debuggerToolDef() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "submit_fix_plan",
			Description: "Submit the debugger's diagnosis and fix plan.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"summary": {
						"type": "string",
						"description": "Diagnosis of the root cause."
					},
					"nodes": {
						"type": "array",
						"description": "Fix plan steps. Each is {tool, params, depends_on, tag}.",
						"items": {
							"type": "object",
							"properties": {
								"tool": {"type": "string"},
								"params": {"type": "object"},
								"depends_on": {"type": "array", "items": {"type": "integer"}},
								"tag": {"type": "string"}
							}
						}
					}
				},
				"required": ["summary", "nodes"]
			}`),
		},
	}
}

func preflightToolDef() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "submit_preflight",
			Description: "Submit the preflight classification.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"mode": {
						"type": "string",
						"enum": ["chat", "meta", "investigate"]
					},
					"intent": {
						"type": "string",
						"description": "Intent rank name from the registry, e.g. rank(0), rank(100), etc."
					},
					"skills": {
						"type": "array",
						"items": {"type": "string"},
						"description": "Skill keys relevant to this query."
					},
					"required_categories": {
						"type": "array",
						"items": {"type": "string"},
						"description": "Tool categories the plan MUST include."
					},
					"context": {
						"type": "string",
						"description": "One sentence framing the user's intent for the executor, based on query + conversation history."
					},
					"compute_mode": {
						"type": "string",
						"enum": ["", "shallow", "deep"],
						"description": "Authoritative compute-node depth. 'deep' = build a new codebase. 'shallow' = one-off script / calculation / ranking (even over many inputs). '' = no compute needed. Presence of existing workspace files is NOT a signal."
					}
				},
				"required": ["mode", "intent", "context", "compute_mode"]
			}`),
		},
	}
}

func architectToolDef() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "submit_blueprint",
			Description: "Submit the architect's blueprint and task plan.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"blueprint": {
						"type": "string",
						"description": "Full markdown blueprint document."
					},
					"project_root": {
						"type": "string",
						"description": "Project root directory path, e.g. project/kaiju_webapp. All file paths, setup commands, service workdirs, and validators use this as their base."
					},
					"interfaces": {
						"type": "object",
						"description": "API contracts and types as a JSON object."
					},
					"schema": {
						"type": "object",
						"description": "Database schema as {type, tables}."
					},
					"setup": {
						"type": "array",
						"items": {"type": "string"},
						"description": "Sequential shell commands run before coders."
					},
					"tasks": {
						"type": "array",
						"description": "Work items, one per file or coherent unit.",
						"items": {
							"type": "object",
							"properties": {
								"goal": {"type": "string"},
								"task_files": {
									"type": "array",
									"items": {"type": "string"},
									"description": "Exactly ONE file path."
								},
								"brief": {"type": "string"},
								"execute": {"type": "string", "description": "Shell command run AFTER this coder finishes."},
								"service": {
									"type": "object",
									"properties": {
										"command": {"type": "string"},
										"name": {"type": "string"},
										"workdir": {"type": "string"},
										"port": {"type": "integer"}
									}
								},
								"depends_on_tasks": {
									"type": "array",
									"items": {"type": "integer"}
								}
							},
							"required": ["goal", "task_files"]
						}
					},
					"services": {
						"type": "array",
						"description": "Top-level long-running processes.",
						"items": {
							"type": "object",
							"properties": {
								"name": {"type": "string"},
								"command": {"type": "string"},
								"workdir": {"type": "string"},
								"port": {"type": "integer"}
							},
							"required": ["name", "command"]
						}
					},
					"validation": {
						"type": "array",
						"description": "Structural health checks.",
						"items": {
							"type": "object",
							"properties": {
								"name": {"type": "string"},
								"check": {"type": "string"},
								"expect": {"type": "string"}
							},
							"required": ["name", "check"]
						}
					}
				},
				"required": ["blueprint", "tasks"]
			}`),
		},
	}
}

func coderToolDef() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "submit_code",
			Description: "Submit code to write or edit. Use 'code' for new files, 'edits' for modifying existing files.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"language": {
						"type": "string",
						"description": "Language identifier (javascript, python, go, html, css, etc.)."
					},
					"filename": {
						"type": "string",
						"description": "Path to write to, relative to the workspace project root."
					},
					"code": {
						"type": "string",
						"description": "Complete file content. Use this for NEW files (write mode)."
					},
					"edits": {
						"type": "array",
						"description": "Text-replacement edits. Use this for EXISTING files (edit mode). Each edit is a verbatim old_content → new_content replacement.",
						"items": {
							"type": "object",
							"properties": {
								"old_content": {"type": "string"},
								"new_content": {"type": "string"}
							},
							"required": ["old_content", "new_content"]
						}
					}
				},
				"required": ["language", "filename"]
			}`),
		},
	}
}
