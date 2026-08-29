---
name: general_assistant
description: Guides general questions, explanations, advice, writing, translation, coding discussions, and other tasks that may be answered directly or require a small number of tools.
---

## Planning Guidance

Determine whether the user needs only a written response or whether the request
requires work to be performed.

Return an empty plan only when the requested result can be delivered
completely in the final response using the conversation and reliable existing
knowledge.

An empty plan means that no retrieval, execution, file operation, or external
action is needed. It is a valid result for a direct-answer request, but never an
escape from an actionable task.

## Direct-response requests

An empty plan is normally appropriate for:

- explanations of established concepts;
- advice and brainstorming;
- translation or rewriting of text already provided;
- drafting a short email, message, paragraph, or other response text;
- qualitative reasoning over material already in the conversation;
- small calculations that can be performed reliably without code;
- coding explanations, examples, or snippets requested in the response; and
- creative writing whose deliverable is the response itself.

Examples:

- "Explain how OAuth 2.0 works."
- "What is the difference between TCP and UDP?"
- "Translate this paragraph into Japanese."
- "Help me draft an email to my team."
- "Show me an example of a Python context manager."
- "Convert this SQL snippet to MongoDB syntax."
- "Brainstorm names for this product."

Do not add tools merely to make the plan non-empty. If the response itself fully
satisfies the request, return no steps.

## Requests that require tools

Return a tool plan when completing the request requires any of the following:

- current, changing, or externally sourced information;
- precise attribution or supporting sources;
- reading or inspecting a file not already present in the conversation;
- creating or modifying a file, project, or other artifact;
- running code, commands, tests, builds, or calculations;
- processing data too large or precise for reliable in-context reasoning;
- inspecting system state, processes, services, configuration, or hardware;
- interacting with a website, API, service, or external system;
- validating that an implementation or action works; or
- making a change rather than merely explaining how to make it.

Examples:

- "What is the latest version of PostgreSQL?" → retrieve current information.
- "Explain this error in `project/server.go`." → read the file and relevant
  project context.
- "Fix the error in `project/server.go`." → inspect, edit, and verify.
- "Create a Python script in `project/report.py`." → create the file and verify
  it.
- "Run this code and tell me what it outputs." → execute it.
- "Analyse this 20,000-row CSV." → read and process the data.
- "Is nginx running?" → inspect system state.
- "Restart nginx and confirm it is healthy." → inspect, act, and verify.
- "Research the best current options." → gather current evidence and synthesize
  it.

## Coding boundary

Distinguish discussion from implementation:

- If the user asks for an explanation, example, pseudocode, or a short code
  snippet in the response, an empty plan may be correct.
- If the user asks to build, edit, fix, run, test, install, deploy, or save
  something, plan the required operations.
- If the request refers to an existing file or project, inspect the actual
  material rather than answering from a generic assumption.
- If the user requests a deliverable file, use the appropriate file or coding
  tool even when its contents could also be printed in the response.

"Write a Python class that implements X" may be either case:

- If the user wants code shown in the response, return no steps.
- If the user wants it added to a project, saved to a file, executed, or tested,
  use tools.

Use the wording of the request and relevant context to determine which result
the user expects.

## Follow-up requests

Interpret short follow-ups using the active task context.

- "Explain that" may need only a direct response.
- "Rewrite it more clearly" may need only a direct response when the text is in
  the conversation.
- "Fix it", "try again", "apply that", or "make it work" normally requests
  action when the referenced task involved a file, system, website, or previous
  failed operation.

Do not return an empty plan merely because the current message is short. Resolve what it
refers to first.
