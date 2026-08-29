package agent

import (
	"encoding/json"
	"fmt"

	"github.com/Compdeep/kaiju/agent/llm"
)

// ── Stage schemas ───────────────────────────────────────────────────────────
//
// The shape each reasoning stage asks the model for. Not tools: nothing here is
// registered, nothing here is executed, and the registry has never heard of any
// of them. A tool is bash, web_search, file_read — a capability the agent can
// call. These are declared in a tool's clothing because that is the shape the
// provider takes a schema in.
//
// Declaring one is what asks for the shape; it is not what enforces it. A
// request carrying a single one of these, with the model pinned to it, is
// rewritten at Client.Complete into a schema request the provider constrains as
// the model writes — see llm/structured.go, which measured what the tool-calling
// wire actually guarantees (0 of 3 replies valid) against what this buys
// (3 of 3).
//
// A stage whose schema cannot be constrained is named at startup rather than
// left to look like the ones that can — see schema_check.go.

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

// ── The schemas ─────────────────────────────────────────────────────────────
//
// One per stage that parses what comes back. Each mirrors the Go struct that
// reads it — in compute.go, reflection.go and the rest — written as JSON Schema
// so the provider can constrain what the model writes.

func curatorSchema() llm.ToolDef {
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

// reflectorSchema is the reflection decision.
//
// progress carries "" as an enum member on purpose. The struct reads an absent
// progress as "productive" (see ReflectionOutput) and the scheduler's streak
// counter resets on anything that is not "diminishing" — both were written when
// a field could simply be left out. Strict decoding closes the schema and marks
// every property required, so nothing can be left out any more: without ""
// present, a reflector with no view on the trend has to claim one, and the only
// other thing it can claim is the value that ends a run after two of them.
//
// It is not offered in the prompt, which still asks for "productive" when
// unsure. It is here so the schema permits what the Go on both sides already
// handles.
func reflectorSchema() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "reflector_decision",
			Description: "Submit the reflection decision.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"decision": {
						"type": "string",
						"enum": ["continue", "replan", "conclude"],
						"description": "What to do next."
					},
					"progress": {
						"type": "string",
						"enum": ["", "productive", "diminishing"],
						"description": "How the recent cycles are trending. Use 'productive' when unsure; '' is accepted and read the same way. Two consecutive 'diminishing' batches downgrade replan→conclude; see prompt for rules."
					},
					"summary": {
						"type": "string",
						"description": "What happened, current state, and SPECIFIC error messages from failures (exact module names, paths, error text)."
					},
					"next": {
						"type": "string",
						"description": "Only if replan: the concrete next move. SUCCESS lead → e.g. 'fetch the 3 URLs the searches surfaced'. FAILURE to fix → describe the failure with exact error text, file paths, module names (the executive will plan a debug step to diagnose + fix it). Name the move, not the tool call."
					},
					"outcome": {
						"type": "string",
						"description": "Only if conclude: final answer for the user."
					},
					"aggregate": {
						"type": "boolean",
						"description": "Only if conclude: whether the aggregator should run on the outcome."
					}
				},
				"required": ["decision", "summary"]
			}`),
		},
	}
}

func observerSchema() llm.ToolDef {
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
					"steps": {
						"type": "array",
						"description": "When action=inject: new steps to add. Each is {tool, params, depends_on, tag}.",
						"items": {
							"type": "object",
							"properties": {
								"tool": {"type": "string"},
								"params": {"type": "string", "description": "The tool's parameters as a JSON object written INSIDE A STRING, e.g. \"{\\\"path\\\": \\\"project/app/server.js\\\"}\". Write \"{}\" for a tool that takes none. A value may be a reference to an earlier step, written ${step.<that step's tag>.<dot-path into its output>}: \"{\\\"url\\\": \\\"${step.find_docs.results.0.url}\\\"}\"."},
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

// holmesSchema defines the function-calling schema for one iteration of the
// Holmes investigator. The model emits ONE thought + one or more actions OR a
// final conclusion. Actions run in parallel; Holmes sees all results on the
// next iteration.
func holmesSchema() llm.ToolDef {
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
								"params": {"type": "string", "description": "The tool's parameters as a JSON object written INSIDE A STRING. ALWAYS wrap them in params, never put fields at the action top level. Example for bash: \"{\\\"command\\\": \\\"cat .services/backend.err.log\\\"}\". Example for service: \"{\\\"action\\\": \\\"logs\\\", \\\"name\\\": \\\"backend\\\", \\\"stream\\\": \\\"err\\\"}\". Example for file_read: \"{\\\"path\\\": \\\"/path/to/file\\\"}\". Required params for the chosen tool MUST be present; write \"{}\" only for a tool that takes none."}
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

func debuggerSchema() llm.ToolDef {
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
					"steps": {
						"type": "array",
						"description": "Fix plan steps. Each is {tool, params, depends_on, tag}.",
						"items": {
							"type": "object",
							"properties": {
								"tool": {"type": "string"},
								"params": {"type": "string", "description": "The tool's parameters as a JSON object written INSIDE A STRING, e.g. \"{\\\"path\\\": \\\"project/app/server.js\\\"}\". Write \"{}\" for a tool that takes none. A value may be a reference to an earlier step, written ${step.<that step's tag>.<dot-path into its output>}: \"{\\\"url\\\": \\\"${step.find_docs.results.0.url}\\\"}\"."},
								"depends_on": {"type": "array", "items": {"type": "integer"}},
								"tag": {"type": "string"}
							}
						}
					}
				},
				"required": ["summary", "steps"]
			}`),
		},
	}
}

func preflightSchema() llm.ToolDef {
	// The context shape comes from PreflightContext, not from a copy written
	// here — see PreflightContextSchema. It is the field that carries every URL,
	// path and selector to a planner that cannot see the conversation, so a
	// description of it that has drifted from what Go reads loses exactly what
	// it exists to carry.
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "submit_preflight",
			Description: "Submit the preflight classification.",
			Parameters: json.RawMessage(fmt.Sprintf(`{
				"type": "object",
				"properties": {
					"mode": {
						"type": "string",
						"enum": ["chat", "agent"]
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
					"context": %s,
					"compute_mode": {
						"type": "string",
						"enum": ["", "shallow", "deep"],
						"description": "Authoritative compute-node depth. 'deep' = build a new codebase. 'shallow' = one-off script / calculation / ranking (even over many inputs). '' = no compute needed. Presence of existing workspace files is NOT a signal."
					},
					"needs_synthesis": {
						"type": "boolean",
						"description": "True when the answer needs a written-up synthesis over gathered evidence: deep/multi-source research, a report or analysis, or 'build/flesh out/draft a section/document'. False for a single fact lookup, a yes/no, or a quick status check. When true, the run always ends with the aggregator (full synthesis) rather than a short reflector summary."
					}
				},
				"required": ["mode", "intent", "context", "compute_mode"]
			}`, PreflightContextSchema())),
		},
	}
}

// routeSchema is the minimal tool for the cheap first-pass router: mode only,
// no skills/intent/categories — those are decided only on the agentic path.
func routeSchema() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "route",
			Description: "Route the user's latest message to a handling mode.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"mode": { "type": "string", "enum": ["chat", "agent"] },
					"lacking_context": {
						"type": "array",
						"items": { "type": "string" },
						"description": "Words to look up in earlier messages, when answering needs something said earlier that is not in the summary or the messages shown. Use the words the conversation itself would have used — they are matched against the earlier text as written. Leave empty when what is shown is enough."
					}
				},
				"required": ["mode"]
			}`),
		},
	}
}

func architectSchema() llm.ToolDef {
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
						"type": "string",
						"description": "API contracts and types, as a JSON object written INSIDE A STRING: \"{\\\"POST /api/todos\\\": {\\\"body\\\": {\\\"title\\\": \\\"string\\\"}}}\". Its keys are the endpoint and type names you are choosing, which is why it travels as a string. Write \"\" when the project defines none."
					},
					"schema": {
						"type": "string",
						"description": "Database schema as {type, tables}, as a JSON object written INSIDE A STRING: \"{\\\"type\\\": \\\"postgres\\\", \\\"tables\\\": {\\\"todos\\\": {\\\"id\\\": \\\"serial primary key\\\"}}}\". Write \"\" when the project has no database."
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

// coderSchema builds the Coder's reply shape. editable says whether a file
// this call may edit already exists.
//
// When it does not, "edits" is left out. Both fields used to be offered on
// every call, with which one applies said only in their descriptions — so a
// Coder naming a brand new file could reply with text replacements for content
// that was never written, and the run failed on "no such file or directory".
// It came down to which of two allowed answers the model happened to give:
// two runs with identical inputs went opposite ways, one writing its file and
// one failing on it. An answer that cannot be carried out should not be on
// offer, which settles it before the model is asked rather than after it
// replies.
func coderSchema(editable bool) llm.ToolDef {
	editsField := ""
	// Writing the file whole is the only shape on offer when nothing exists to
	// edit, so the content field is demanded. A reply naming a file and a
	// language and nothing else is schema-valid without this, and says what is
	// about to be written without ever saying what goes in it.
	required := `["language", "filename", "code"]`
	description := "Submit code for a new file, in 'code'."
	if editable {
		editsField = `,
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
					}`
		// Both shapes apply to a file that is already there, and a JSON Schema
		// "required" array cannot say "one of these two" — so neither is
		// demanded and the caller reads whichever arrived.
		required = `["language", "filename"]`
		description = "Submit code to write or edit. Use 'code' to replace a file wholesale, 'edits' for text replacements within an existing one."
	}
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "submit_code",
			Description: description,
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
						"description": "Complete file content."
					},
					"execute": {
						"type": "string",
						"description": "Shell command that runs the file you just wrote, relative to the workspace project root — for example: python3 compute.py. Give it whenever the printed output of the file is the answer; without it the file is written and never runs."
					}` + editsField + `
				},
				"required": ` + required + `
			}`),
		},
	}
}
