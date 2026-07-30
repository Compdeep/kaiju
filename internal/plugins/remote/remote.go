//go:build plugin_remote

// Package remote is the generic bridge to an out-of-process plugin host that
// speaks the kaiju remote-plugin protocol:
//
//	GET  /plugins        -> a manifest (every plugin, its tools + JSON schemas + skill)
//	POST /invoke/{tool}  -> run a tool, return a kaiju ToolMessage envelope
//
// At activation the bridge fetches the manifest and synthesizes ONE kaiju tool
// per advertised tool, so adding a plugin to the host needs no kaiju rebuild.
// The host can be written in any language (the reference host is Python); kaiju
// only speaks the protocol.
//
// Enable with `-tags plugin_remote` + config `plugins: ["remote"]`. Point it at
// the host with KAIJU_PLUGIN_HOST (default http://127.0.0.1:8091); an optional
// KAIJU_PLUGIN_TOKEN is sent as a bearer token on every call.
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
	"github.com/Compdeep/kaiju/internal/plugins"
)

func init() { plugins.Add(bridge{}) }

type bridge struct{}

func (bridge) Name() string { return "remote" }

func (bridge) Description() string {
	return "Remote plugin bridge: connects to an out-of-process plugin host (any language) over REST and adds its tools."
}

func hostURL() string {
	if u := strings.TrimRight(os.Getenv("KAIJU_PLUGIN_HOST"), "/"); u != "" {
		return u
	}
	return "http://127.0.0.1:8091"
}

// manifest matches the host's GET /plugins response shape.
type manifest struct {
	Plugins []struct {
		Name  string         `json:"name"`
		Skill string         `json:"skill"`
		Tools []manifestTool `json:"tools"`
	} `json:"plugins"`
}

type manifestTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Impact      string          `json:"impact"`
}

// Register fetches the host manifest and adds one kaiju tool per advertised tool.
// Best-effort: if the host is down, it logs and contributes nothing (the agent
// simply runs without those tools) rather than failing boot — the same graceful
// degradation kaiju already applies to a missing tool.
func (bridge) Register(h plugins.Host) {
	base := hostURL()
	man, err := fetchManifest(base)
	if err != nil {
		log.Printf("[plugin/remote] host %s unreachable, no remote tools: %v", base, err)
		return
	}
	token := os.Getenv("KAIJU_PLUGIN_TOKEN")
	tools := 0
	for _, p := range man.Plugins {
		for _, t := range p.Tools {
			h.AddTool(&remoteTool{base: base, token: token, spec: t})
			tools++
		}
		if p.Skill != "" {
			// The skill travels with the plugin in the manifest. Injecting it into
			// kaiju's live skill registry needs a Host.AddSkill hook — a small
			// follow-up; the tool works without it today.
			log.Printf("[plugin/remote] plugin %q ships a skill (%d bytes) — carried, not yet injected", p.Name, len(p.Skill))
		}
	}
	log.Printf("[plugin/remote] host %s: %d tool(s) from %d plugin(s)", base, tools, len(man.Plugins))
}

func fetchManifest(base string) (*manifest, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/plugins", nil)
	if err != nil {
		return nil, err
	}
	if tok := os.Getenv("KAIJU_PLUGIN_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /plugins: HTTP %d", resp.StatusCode)
	}
	var m manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// remoteTool is a kaiju tool backed by a POST to the host's /invoke/{name}. The
// host returns a ToolMessage envelope, which Execute passes straight through.
type remoteTool struct {
	base, token string
	spec        manifestTool
}

var _ agenttools.Tool = (*remoteTool)(nil)

func (t *remoteTool) Name() string        { return t.spec.Name }
func (t *remoteTool) Description() string { return t.spec.Description }

func (t *remoteTool) Parameters() json.RawMessage {
	if len(t.spec.Parameters) == 0 || string(t.spec.Parameters) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return t.spec.Parameters
}

func (t *remoteTool) Impact(map[string]any) int {
	switch strings.ToLower(strings.TrimSpace(t.spec.Impact)) {
	case "", "observe", "read":
		return agenttools.ImpactObserve
	case "control", "destroy", "delete", "irreversible":
		return agenttools.ImpactControl
	default:
		return agenttools.ImpactAffect // unknown side effects → treat as reversible-write
	}
}

func (t *remoteTool) Execute(ctx context.Context, params map[string]any) (string, error) {
	body, _ := json.Marshal(map[string]any{"params": params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/invoke/"+t.spec.Name, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// A down host is a tool failure, not a kaiju crash — report it as one.
		return agenttools.ToolFail(t.spec.Name, "plugin host unreachable: "+err.Error(), nil).JSON(), nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if resp.StatusCode != http.StatusOK {
		return agenttools.ToolFail(t.spec.Name, fmt.Sprintf("plugin host HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))), nil).JSON(), nil
	}
	// The host already returns a ToolMessage envelope — validate and pass through.
	if _, ok := agenttools.ParseToolMessage(string(raw)); ok {
		return string(raw), nil
	}
	// Non-envelope response: wrap the raw body as ok text so it still flows.
	return agenttools.ToolText(string(raw)).JSON(), nil
}
