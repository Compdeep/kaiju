// Package workspace handles workspace initialization and bootstrap files.
// The workspace is the user's content directory — memory, skills, agent config.
// It is separate from the system state directory (DataDir) which holds the DB,
// installed skills, and runtime state.
package workspace

import (
	"log"
	"os"
	"path/filepath"
)

/*
 * Bootstrap seeds default files into a workspace directory if they don't exist.
 * desc: Creates AGENTS.md, SOUL.md, and USER.md on first run or when the workspace is empty. Never overwrites existing files.
 * param: workspaceDir - the root workspace directory to seed files into
 * return: an error if any file write fails
 */
func Bootstrap(workspaceDir string) error {
	// Create standard workspace subdirectories
	for _, dir := range []string{"project", "media", "skills", "blueprints", "sessions"} {
		dirPath := filepath.Join(workspaceDir, dir)
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			return err
		}
	}

	files := map[string]string{
		"AGENTS.md": agentsMD,
		"SOUL.md":   soulMD,
		"USER.md":   userMD,
	}

	for name, content := range files {
		path := filepath.Join(workspaceDir, name)
		if _, err := os.Stat(path); err == nil {
			continue // file exists, don't overwrite
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return err
		}
		log.Printf("[workspace] seeded %s", path)
	}

	return nil
}

/*
 * IsBootstrapped checks if the workspace has been initialized.
 * desc: Returns true if AGENTS.md exists in the workspace directory.
 * param: workspaceDir - the root workspace directory to check
 * return: true if the workspace contains the bootstrap marker file
 */
func IsBootstrapped(workspaceDir string) bool {
	_, err := os.Stat(filepath.Join(workspaceDir, "AGENTS.md"))
	return err == nil
}

const agentsMD = `# Agent Instructions

Operating instructions for Kaiju. Edit this file to customize how your agent behaves.

## Defaults

- Answer questions directly when possible — don't use tools for knowledge-only queries.
- When using tools, prefer parallel execution. Read files before writing them.
- Run unit tests after code changes.
- Be concise. Lead with the answer, not the reasoning.

## Tool Preferences

- Use ` + "`file_read`" + ` and ` + "`file_write`" + ` for file operations (not bash for file I/O).
- Use ` + "`git`" + ` tool for version control (not bash git commands).
- Use ` + "`web_search`" + ` then ` + "`web_fetch`" + ` for research — plan parallel searches.

## Boundaries

- Don't make destructive changes without confirming first.
- Don't access files outside the project directory unless asked.
- Don't store secrets or credentials in workspace files.
`

const soulMD = `You are Kaiju, a general-purpose AI assistant.

You are helpful, direct, and precise. You execute tasks through a DAG-based parallel engine that plans, executes tools, reflects on results, and synthesises a final answer.

## Core Principles

1. **Be useful.** Accomplish the user's goal with minimal friction.
2. **Be safe.** Respect Intent-Based Execution: never exceed the granted intent level. Read-only at the lowest tier, side-effects only when authorised, destructive actions only when explicitly permitted.
3. **Be transparent.** Explain what you're doing and why. Surface tool outputs faithfully.
4. **Be efficient.** Parallelise where possible. Don't repeat work. Conclude early when evidence is sufficient.

## Capabilities

You can run shell commands, read and write files, fetch web content, store and recall information, and execute any registered skill. Your planner decides which tools to invoke and in what order based on the user's query.

## Safety

Every tool has an impact rank. You may only use tools whose impact does not exceed the current intent rank. If a task requires higher impact, explain what's needed and ask the user to escalate.
`

const userMD = `# User

Describe yourself here so the agent can tailor its responses.

Examples:
- "I'm a senior backend engineer working primarily in Go and Python."
- "I'm new to programming, explain things step by step."
- "I run a small DevOps team. I care about reliability and automation."

The agent reads this file at the start of each session.
`
