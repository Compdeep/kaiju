package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	agenttools "github.com/user/kaiju/internal/agent/tools"
)

/*
 * Git provides git operations with impact that varies by action.
 * desc: Tool for executing git commands; observe for status/log/diff, affect for add/commit/branch, control for push/reset.
 */
type Git struct{}

/*
 * NewGit creates a new Git tool instance.
 * desc: Returns a zero-value Git ready for use.
 * return: pointer to a new Git
 */
func NewGit() *Git { return &Git{} }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing git command output as a string.
 * return: JSON schema as raw bytes
 */
func (g *Git) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"output":{"type":"string","description":"git command output"}}}`)
}

/*
 * Name returns the tool identifier.
 * desc: Returns "git" as the tool name.
 * return: the string "git"
 */
func (g *Git) Name() string { return "git" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains the supported git operations.
 * return: description string
 */
func (g *Git) Description() string {
	return "Execute git operations: status, log, diff, add, commit, branch, checkout, push, pull."
}

/*
 * Impact determines the safety level based on the git action being performed.
 * desc: Returns ImpactObserve for read-only actions, ImpactAffect for local mutations, ImpactControl for remote/destructive actions.
 * param: params - must contain "action" string to classify
 * return: impact level constant (ImpactObserve, ImpactAffect, or ImpactControl)
 */
func (g *Git) Impact(params map[string]any) int {
	action, _ := params["action"].(string)
	switch action {
	case "status", "log", "diff", "branch_list", "show":
		return agenttools.ImpactObserve
	case "add", "commit", "branch_create", "checkout", "stash", "tag":
		return agenttools.ImpactAffect
	case "push", "reset", "pull", "merge", "rebase":
		return agenttools.ImpactControl
	default:
		return agenttools.ImpactAffect
	}
}

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines the action enum, optional args string, and optional working directory path.
 * return: JSON schema as raw bytes
 */
func (g *Git) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["status", "log", "diff", "add", "commit", "branch_list", "branch_create", "checkout", "push", "pull", "show", "stash", "tag", "reset", "merge"],
				"description": "Git action to perform"
			},
			"args": {"type": "string", "description": "Additional arguments (e.g. file paths, branch names, commit messages)"},
			"path": {"type": "string", "description": "Working directory (default: current directory)"}
		},
		"required": ["action"],
		"additionalProperties": false
	}`)
}

/*
 * Execute runs the specified git action and returns its output.
 * desc: Translates the action and args into a git command, executes it, and returns stdout/stderr truncated to 8KB.
 * param: ctx - context for cancellation
 * param: params - must contain "action"; optionally "args" and "path"
 * return: git command output string, or error for unknown actions or missing required args
 */
func (g *Git) Execute(ctx context.Context, params map[string]any) (string, error) {
	action, _ := params["action"].(string)
	args, _ := params["args"].(string)
	path, _ := params["path"].(string)

	var gitArgs []string
	switch action {
	case "status":
		gitArgs = []string{"status", "--short"}
	case "log":
		gitArgs = []string{"log", "--oneline", "-20"}
		if args != "" {
			gitArgs = append(gitArgs, strings.Fields(args)...)
		}
	case "diff":
		gitArgs = []string{"diff"}
		if args != "" {
			gitArgs = append(gitArgs, strings.Fields(args)...)
		}
	case "add":
		if args == "" {
			args = "."
		}
		gitArgs = append([]string{"add"}, strings.Fields(args)...)
	case "commit":
		if args == "" {
			return "", fmt.Errorf("git: commit message required in args")
		}
		gitArgs = []string{"commit", "-m", args}
	case "branch_list":
		gitArgs = []string{"branch", "-a"}
	case "branch_create":
		if args == "" {
			return "", fmt.Errorf("git: branch name required in args")
		}
		gitArgs = []string{"branch", args}
	case "checkout":
		if args == "" {
			return "", fmt.Errorf("git: branch/ref required in args")
		}
		gitArgs = append([]string{"checkout"}, strings.Fields(args)...)
	case "push":
		gitArgs = []string{"push"}
		if args != "" {
			gitArgs = append(gitArgs, strings.Fields(args)...)
		}
	case "pull":
		gitArgs = []string{"pull"}
		if args != "" {
			gitArgs = append(gitArgs, strings.Fields(args)...)
		}
	case "show":
		gitArgs = []string{"show", "--stat"}
		if args != "" {
			gitArgs = append(gitArgs, strings.Fields(args)...)
		}
	case "stash":
		gitArgs = []string{"stash"}
		if args != "" {
			gitArgs = append(gitArgs, strings.Fields(args)...)
		}
	case "tag":
		if args == "" {
			gitArgs = []string{"tag", "-l"}
		} else {
			gitArgs = append([]string{"tag"}, strings.Fields(args)...)
		}
	case "reset":
		gitArgs = append([]string{"reset"}, strings.Fields(args)...)
	case "merge":
		if args == "" {
			return "", fmt.Errorf("git: branch name required in args")
		}
		gitArgs = append([]string{"merge"}, strings.Fields(args)...)
	default:
		return "", fmt.Errorf("git: unknown action %q", action)
	}

	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	if path != "" {
		cmd.Dir = path
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	if len(output) > 8192 {
		output = output[:8192] + "\n... (truncated)"
	}

	if err != nil {
		return fmt.Sprintf("%s\n[exit: %v]", output, err), nil
	}
	return output, nil
}

var _ agenttools.Tool = (*Git)(nil)
