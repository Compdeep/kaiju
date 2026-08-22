package config

import (
	"fmt"
	"os"
	"path/filepath"
)

/*
 * Validate checks the config for required fields and consistency.
 * desc: Ensures API key and model are set, DAG mode is valid, and required directories exist.
 *       Says nothing about agent.safety_level: it is a rank from the intent
 *       registry, which the registry itself defines and this package cannot see.
 * return: an error describing the first validation failure, or nil if valid
 */
func (c *Config) Validate() error {
	if c.LLM.APIKey == "" {
		return fmt.Errorf("config: llm.api_key is required (set LLM_API_KEY env var or config)")
	}
	if c.LLM.Model == "" {
		return fmt.Errorf("config: llm.model is required")
	}
	if c.Agent.DAGMode != "" && c.Agent.DAGMode != "reflect" && c.Agent.DAGMode != "nReflect" && c.Agent.DAGMode != "orchestrator" {
		return fmt.Errorf("config: agent.dag_mode must be reflect, nReflect, or orchestrator")
	}

	// Ensure system state dir exists
	if err := os.MkdirAll(c.Agent.DataDir, 0700); err != nil {
		return fmt.Errorf("config: create data dir %s: %w", c.Agent.DataDir, err)
	}

	// Ensure workspace directory structure exists
	workspaceDirs := []string{
		c.Agent.Workspace,
		filepath.Join(c.Agent.Workspace, "memory"),
		filepath.Join(c.Agent.Workspace, "skills"),
		filepath.Join(c.Agent.Workspace, "canvas"),
	}
	for _, dir := range workspaceDirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("config: create workspace dir %s: %w", dir, err)
		}
	}

	return nil
}
