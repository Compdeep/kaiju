package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// BootLLMConfig holds LLM settings from BOOT.md frontmatter.
type BootLLMConfig struct {
	Endpoint    string  `json:"endpoint"`
	Model       string  `json:"model"`
	Temperature *float64 `json:"temperature"`
	MaxTokens   *int     `json:"max_tokens"`
	APIKey      string  `json:"api_key"`
}

// BootEmbConfig holds embedding settings from BOOT.md frontmatter.
type BootEmbConfig struct {
	Enabled   *bool    `json:"enabled"`
	Endpoint  string   `json:"endpoint"`
	Model     string   `json:"model"`
	APIKey    string   `json:"api_key"`
	TopK      *int     `json:"top_k"`
	Threshold *float64 `json:"threshold"`
}

// BootGatesConfig holds gate settings from BOOT.md frontmatter.
type BootGatesConfig struct {
	RateLimit *int `json:"rate_limit"`
	MaxTurns  *int `json:"max_turns"`
	Clearance *int `json:"clearance"` // IGX clearance override
}

// BootDAGConfig holds DAG engine settings from BOOT.md frontmatter.
type BootDAGConfig struct {
	Enabled       *bool  `json:"enabled"`
	Mode          string `json:"mode"` // "reflect", "nReflect", "orchestrator"
	MaxNodes      *int   `json:"max_nodes"`
	MaxPerSkill   *int   `json:"max_per_skill"`
	MaxLLMCalls   *int   `json:"max_llm_calls"`
	MaxObserverCalls *int `json:"max_observer_calls"`
	BatchSize     *int   `json:"batch_size"`
	WallClockSec  *int   `json:"wall_clock_sec"`
}

// BootConfig is the parsed representation of a BOOT.md file.
type BootConfig struct {
	LLM           BootLLMConfig   `json:"llm"`
	Embeddings    BootEmbConfig   `json:"embeddings"`
	Gates         BootGatesConfig `json:"gates"`
	DAG           BootDAGConfig   `json:"dag"`
	AlwaysInclude []string        `json:"always_include"`
	SystemPrompt  string          // markdown body after frontmatter
}

// ParseBootMD reads and parses a BOOT.md file.
// Returns nil, nil if the file does not exist (agent boots normally without it).
func ParseBootMD(path string) (*BootConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read BOOT.md: %w", err)
	}

	content := string(data)

	// Find frontmatter delimiters
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return nil, fmt.Errorf("BOOT.md missing opening --- delimiter")
	}

	// Trim leading whitespace then skip opening ---
	trimmed := strings.TrimSpace(content)
	rest := trimmed[3:] // skip "---"
	rest = strings.TrimLeft(rest, " \t")
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	} else if len(rest) > 1 && rest[0] == '\r' && rest[1] == '\n' {
		rest = rest[2:]
	}

	// Find closing ---
	closeIdx := strings.Index(rest, "\n---")
	if closeIdx < 0 {
		// Try \r\n
		closeIdx = strings.Index(rest, "\r\n---")
		if closeIdx < 0 {
			return nil, fmt.Errorf("BOOT.md missing closing --- delimiter")
		}
	}

	frontmatter := rest[:closeIdx]
	afterClose := rest[closeIdx:]

	// Skip the closing --- line to get the body
	nlIdx := strings.Index(afterClose[1:], "\n")
	body := ""
	if nlIdx >= 0 {
		body = strings.TrimSpace(afterClose[1+nlIdx+1:])
	}

	var bc BootConfig
	if err := json.Unmarshal([]byte(frontmatter), &bc); err != nil {
		return nil, fmt.Errorf("parse BOOT.md frontmatter JSON: %w", err)
	}

	bc.SystemPrompt = body
	return &bc, nil
}

// ApplyToConfig merges BOOT.md settings onto an agent Config.
// BOOT.md wins over config.json; CLI flags should win over both (caller's responsibility).
func (bc *BootConfig) ApplyToConfig(cfg *Config) {
	if bc == nil {
		return
	}

	// LLM
	if bc.LLM.Endpoint != "" {
		cfg.LLMEndpoint = bc.LLM.Endpoint
	}
	if bc.LLM.Model != "" {
		cfg.LLMModel = bc.LLM.Model
	}
	if bc.LLM.APIKey != "" {
		cfg.LLMAPIKey = bc.LLM.APIKey
	}
	if bc.LLM.Temperature != nil {
		cfg.Temperature = *bc.LLM.Temperature
	}
	if bc.LLM.MaxTokens != nil {
		cfg.MaxTokens = *bc.LLM.MaxTokens
	}

	// Embeddings
	if bc.Embeddings.Enabled != nil {
		cfg.EmbeddingsEnabled = *bc.Embeddings.Enabled
	}
	if bc.Embeddings.Endpoint != "" {
		cfg.EmbedEndpoint = bc.Embeddings.Endpoint
	}
	if bc.Embeddings.Model != "" {
		cfg.EmbedModel = bc.Embeddings.Model
	}
	if bc.Embeddings.APIKey != "" {
		cfg.EmbedAPIKey = bc.Embeddings.APIKey
	}
	if bc.Embeddings.TopK != nil {
		cfg.EmbedTopK = *bc.Embeddings.TopK
	}
	if bc.Embeddings.Threshold != nil {
		cfg.EmbedThreshold = *bc.Embeddings.Threshold
	}

	// Gates
	if bc.Gates.RateLimit != nil {
		cfg.RateLimit = *bc.Gates.RateLimit
	}
	if bc.Gates.MaxTurns != nil {
		cfg.MaxTurns = *bc.Gates.MaxTurns
	}
	if bc.Gates.Clearance != nil {
		cfg.NodeClearance = *bc.Gates.Clearance
	}

	// DAG
	if bc.DAG.Enabled != nil {
		cfg.DAGEnabled = *bc.DAG.Enabled
	}
	if bc.DAG.Mode != "" {
		cfg.DAGMode = bc.DAG.Mode
	}
	if bc.DAG.MaxNodes != nil {
		cfg.MaxNodes = *bc.DAG.MaxNodes
	}
	if bc.DAG.MaxPerSkill != nil {
		cfg.MaxPerSkill = *bc.DAG.MaxPerSkill
	}
	if bc.DAG.MaxLLMCalls != nil {
		cfg.MaxLLMCalls = *bc.DAG.MaxLLMCalls
	}
	if bc.DAG.MaxObserverCalls != nil {
		cfg.MaxObserverCalls = *bc.DAG.MaxObserverCalls
	}
	if bc.DAG.BatchSize != nil {
		cfg.BatchSize = *bc.DAG.BatchSize
	}
	if bc.DAG.WallClockSec != nil {
		cfg.DAGWallClock = time.Duration(*bc.DAG.WallClockSec) * time.Second
	}

	if len(bc.AlwaysInclude) > 0 {
		cfg.AlwaysInclude = bc.AlwaysInclude
	}

	// System prompt
	if bc.SystemPrompt != "" {
		cfg.CustomSystemPrompt = bc.SystemPrompt
	}
}
