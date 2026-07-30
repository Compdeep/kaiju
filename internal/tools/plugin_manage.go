package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
	"github.com/Compdeep/kaiju/internal/config"
	"github.com/Compdeep/kaiju/internal/plugins"
)

// PluginList reports the optional plugins compiled into this build and whether
// each is active — the read-only half of plugin management. It answers "what
// plugins/capabilities are available?" and "can you do X (does a plugin add it)?".
type PluginList struct{}

// NewPluginList returns the plugin_list tool.
func NewPluginList() *PluginList { return &PluginList{} }

func (p *PluginList) Name() string { return "plugin_list" }

func (p *PluginList) Description() string {
	return "List the optional plugins built into this kaiju and whether each is active or available (compiled in but switched off). Use when the user asks what plugins or capabilities exist, or whether you can do something that might need a plugin."
}

func (p *PluginList) Impact(map[string]any) int { return agenttools.ImpactObserve }

func (p *PluginList) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (p *PluginList) OutputSchema() json.RawMessage {
	return agenttools.EnvelopeSchema(`{"type":"array","description":"compiled-in plugins","items":{"type":"object","properties":{"name":{"type":"string"},"description":{"type":"string"},"active":{"type":"boolean"}}}}`)
}

func (p *PluginList) Execute(_ context.Context, _ map[string]any) (string, error) {
	cat := plugins.Catalog()
	if len(cat) == 0 {
		return agenttools.ToolEmpty("plugins", "no optional plugins are compiled into this build").JSON(), nil
	}
	var b strings.Builder
	for _, info := range cat {
		state := "available (off)"
		if info.Active {
			state = "active"
		}
		b.WriteString(fmt.Sprintf("- %s [%s]: %s\n", info.Name, state, info.Description))
	}
	return agenttools.ToolOK("plugins", strings.TrimRight(b.String(), "\n"), cat).JSON(), nil
}

var (
	_ agenttools.Tool      = (*PluginList)(nil)
	_ agenttools.Outputter = (*PluginList)(nil)
)

// liveHost implements plugins.Host by writing into the RUNNING tool registry, so
// a plugin activated at runtime (plugin_enable) adds its tools live — the planner
// picks them up on the next turn. Seams delegate to the core registrars.
type liveHost struct {
	reg       *agenttools.Registry
	workspace string
	added     []string
}

var _ plugins.Host = (*liveHost)(nil)

func (h *liveHost) Workspace() string { return h.workspace }

func (h *liveHost) AddTool(t agenttools.Tool) {
	h.reg.Replace(t, "plugin")
	h.added = append(h.added, t.Name())
}

func (h *liveHost) RegisterBinaryDecoder(mime string, fn func([]byte) (string, error)) {
	agenttools.RegisterBinaryDecoder(mime, fn)
}

func (h *liveHost) RegisterReaderFallback(fn func(ctx context.Context, rawURL string) (string, error)) {
	agenttools.RegisterReaderFallback(fn)
}

// PluginEnable switches on a compiled-but-off plugin at runtime (adding its tools
// now) and persists the choice to config. Registered ONLY when the host set
// AllowRuntimePluginActivation — so when the capability isn't offered, the tool
// isn't even present. The plugins skill gates WHEN it is used (explicit user ask).
type PluginEnable struct {
	reg       *agenttools.Registry
	cfg       *config.Config
	workspace string
}

// NewPluginEnable returns the plugin_enable tool, wired to the live registry + config.
func NewPluginEnable(reg *agenttools.Registry, cfg *config.Config) *PluginEnable {
	return &PluginEnable{reg: reg, cfg: cfg, workspace: cfg.Agent.Workspace}
}

func (p *PluginEnable) Name() string { return "plugin_enable" }

func (p *PluginEnable) Description() string {
	return "Switch on an optional plugin that is built in but currently off, making its tools available immediately. Use ONLY when the user explicitly asks to enable a plugin (or confirms enabling one you offered). Get names and states from plugin_list."
}

// Impact is Affect: enabling a plugin grants the agent new capabilities — a real,
// but reversible, side effect — so it runs through the intent gate and is audited.
func (p *PluginEnable) Impact(map[string]any) int { return agenttools.ImpactAffect }

func (p *PluginEnable) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"The plugin to enable (a name from plugin_list marked available)."}},"required":["name"],"additionalProperties":false}`)
}

func (p *PluginEnable) Execute(_ context.Context, params map[string]any) (string, error) {
	name, _ := params["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return agenttools.ToolFail("plugin", "'name' is required — see plugin_list", nil).JSON(), nil
	}
	if plugins.IsActive(name) {
		return agenttools.ToolOK("plugin", fmt.Sprintf("plugin %q is already active", name), nil).JSON(), nil
	}
	pl, ok := plugins.Get(name)
	if !ok {
		return agenttools.ToolFail("plugin", fmt.Sprintf("plugin %q is not built into this binary — it needs a rebuild with -tags plugin_%s", name, name), nil).JSON(), nil
	}

	// A remote plugin's bridge reads KAIJU_PLUGIN_HOST; export the configured host
	// (set via plugin_option) so enabling "remote" connects to the right place.
	if p.cfg.RemotePluginHost != "" {
		os.Setenv("KAIJU_PLUGIN_HOST", p.cfg.RemotePluginHost)
	}

	host := &liveHost{reg: p.reg, workspace: p.workspace}
	pl.Register(host)
	plugins.MarkActive(name)

	note := ""
	if err := p.cfg.SetPluginsPersisted(appendUnique(p.cfg.Plugins, name)); err != nil {
		note = fmt.Sprintf(" (active now, but not persisted: %v — it won't survive a restart)", err)
	}

	if len(host.added) == 0 {
		// e.g. the remote bridge activated but its host was unreachable.
		return agenttools.ToolOK("plugin", fmt.Sprintf("Activated %q, but it added no tools — for a remote plugin the host may be unreachable (check KAIJU_PLUGIN_HOST).%s", name, note), map[string]any{"enabled": name, "tools": []string{}, "persisted": note == ""}).JSON(), nil
	}
	msg := fmt.Sprintf("Enabled %q. Added tool(s): %s.%s", name, strings.Join(host.added, ", "), note)
	return agenttools.ToolOK("plugin", msg, map[string]any{"enabled": name, "tools": host.added, "persisted": note == ""}).JSON(), nil
}

// PluginOption sets and persists a plugin configuration option. Today the
// meaningful one is the remote plugin host URL (key "host"/"url"), which the
// `remote` bridge connects to — so a user can point kaiju at their plugin host
// from chat, then enable it. Registered behind the same runtime-activation flag.
type PluginOption struct{ cfg *config.Config }

// NewPluginOption returns the plugin_option tool.
func NewPluginOption(cfg *config.Config) *PluginOption { return &PluginOption{cfg: cfg} }

func (p *PluginOption) Name() string { return "plugin_option" }

func (p *PluginOption) Description() string {
	return `Set and persist a plugin configuration option. Currently supported: the remote plugin host URL — call with {"name":"remote","key":"host","value":"http://127.0.0.1:8091"}, then plugin_enable {"name":"remote"}. Use only when the user asks to configure or point kaiju at a plugin host.`
}

func (p *PluginOption) Impact(map[string]any) int { return agenttools.ImpactAffect }

func (p *PluginOption) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Plugin name (e.g. \"remote\")."},"key":{"type":"string","description":"Option key (e.g. \"host\")."},"value":{"type":"string","description":"Option value (e.g. the host URL)."}},"required":["name","key","value"],"additionalProperties":false}`)
}

func (p *PluginOption) Execute(_ context.Context, params map[string]any) (string, error) {
	key, _ := params["key"].(string)
	value, _ := params["value"].(string)
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return agenttools.ToolFail("plugin", "'key' and 'value' are required", nil).JSON(), nil
	}
	switch key {
	case "host", "url", "remote_plugin_host":
		if err := p.cfg.SetRemotePluginHostPersisted(value); err != nil {
			return agenttools.ToolFail("plugin", "couldn't persist host: "+err.Error(), nil).JSON(), nil
		}
		os.Setenv("KAIJU_PLUGIN_HOST", value)
		return agenttools.ToolOK("plugin", fmt.Sprintf("Set the remote plugin host to %s. Enable it with plugin_enable name=\"remote\".", value), map[string]any{"remote_plugin_host": value}).JSON(), nil
	default:
		return agenttools.ToolFail("plugin", fmt.Sprintf("unknown option %q — supported: host (the remote plugin host URL)", key), nil).JSON(), nil
	}
}

var _ agenttools.Tool = (*PluginOption)(nil)

// appendUnique returns list with v appended if absent (a fresh slice, no aliasing).
func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(append([]string{}, list...), v)
}

var _ agenttools.Tool = (*PluginEnable)(nil)
