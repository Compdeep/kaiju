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

	"github.com/Compdeep/kaiju/agent/toolapi"
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
	// Reader marks a tool that renders+extracts a URL. When set, the bridge ALSO
	// registers it as web_fetch's ReaderFallback, so once the plugin is enabled the
	// fetch tool reads every page through it automatically (JS/SPA pages included) —
	// not only as a callable tool the planner has to pick.
	Reader bool `json:"reader"`
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
	tools, readers := 0, 0
	for _, p := range man.Plugins {
		for _, t := range p.Tools {
			rt := &remoteTool{base: base, token: token, spec: t}
			h.AddTool(rt)
			tools++
			// A reader tool ALSO becomes web_fetch's ReaderFallback, so an enabled
			// reader plugin reads every page automatically (see primaryContent in
			// web_fetch), not just when the planner calls the tool by name.
			if t.Reader {
				h.RegisterReaderFallback(readerFrom(rt))
				readers++
			}
		}
		if p.Skill != "" {
			// The skill travels with the plugin in the manifest. Injecting it into
			// kaiju's live skill registry needs a Host.AddSkill hook — a small
			// follow-up; the tool works without it today.
			log.Printf("[plugin/remote] plugin %q ships a skill (%d bytes) — carried, not yet injected", p.Name, len(p.Skill))
		}
	}
	log.Printf("[plugin/remote] host %s: %d tool(s) (%d reader) from %d plugin(s)", base, tools, readers, len(man.Plugins))
}

// readerFrom adapts a remote reader tool into web_fetch's ReaderFallback: it
// invokes the tool with {"url": rawURL} and returns the extracted text (empty when
// the tool reported empty/error, so web_fetch falls back to built-in extraction).
func readerFrom(rt *remoteTool) func(ctx context.Context, rawURL string) (string, error) {
	return func(ctx context.Context, rawURL string) (string, error) {
		out, err := rt.Execute(ctx, map[string]any{"url": rawURL})
		if err != nil {
			return "", err
		}
		if msg, ok := toolapi.ParseToolMessage(out); ok {
			if msg.Status == toolapi.StatusOK {
				return msg.Content, nil
			}
			return "", nil
		}
		return out, nil
	}
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

var _ toolapi.Tool = (*remoteTool)(nil)

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
		return toolapi.ImpactObserve
	case "control", "destroy", "delete", "irreversible":
		return toolapi.ImpactControl
	default:
		return toolapi.ImpactAffect // unknown side effects → treat as reversible-write
	}
}

// invokeHTTPClient bounds every tool invocation on a plugin host at 60s. A host
// that accepts the connection but then HANGS (a stuck render, a crash-looping
// process, a wedged worker) must never hang a web_fetch or the whole run — the
// call fails fast and web_fetch falls back to its built-in reader. This is a hard
// per-request ceiling on top of the caller's ctx, whichever fires first. (The
// manifest/health calls elsewhere use a shorter context timeout.)
var invokeHTTPClient = &http.Client{Timeout: 60 * time.Second}

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
	resp, err := invokeHTTPClient.Do(req)
	if err != nil {
		// A down/stalled host is a tool failure, not a kaiju crash — report it as
		// one (a timeout here is exactly the "hung reader" case, now capped at 60s).
		return toolapi.ToolFail(t.spec.Name, "plugin host unreachable: "+err.Error(), nil).JSON(), nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if resp.StatusCode != http.StatusOK {
		return toolapi.ToolFail(t.spec.Name, fmt.Sprintf("plugin host HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))), nil).JSON(), nil
	}
	// The host already returns a ToolMessage envelope — validate and pass through.
	if _, ok := toolapi.ParseToolMessage(string(raw)); ok {
		return string(raw), nil
	}
	// Non-envelope response: wrap the raw body as ok text so it still flows.
	return toolapi.ToolText(string(raw)).JSON(), nil
}
