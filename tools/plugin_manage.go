package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/agent/toolapi"
	"github.com/Compdeep/kaiju/internal/plugins"
)

// The two plugin tools: one reports, one installs.
//
// There were three, and the middle one — plugin_enable — existed only because a
// plugin could be compiled in and switched off. That state is gone: a plugin is in
// the binary and live, or it is not installed, and installing means building. The
// third, plugin_option, set a host URL, which is configuration and not a decision
// for a planner.

// -----------------------------------------------------------------------------
// plugin_list
// -----------------------------------------------------------------------------

type PluginList struct{}

func NewPluginList() *PluginList { return &PluginList{} }

func (p *PluginList) Name() string { return "plugin_list" }

func (p *PluginList) Description() string {
	return "List the plugins this kaiju can have and which of them this binary was built with. " +
		"Use when the user asks what plugins or capabilities exist, what is installed, or whether " +
		"you can do something that might need one. Reports every plugin in the catalogue as " +
		"installed or not installed; anything absent from the catalogue cannot be installed here."
}

func (p *PluginList) Impact(map[string]any) int { return toolapi.ImpactObserve }

func (p *PluginList) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (p *PluginList) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(`{"type":"array","description":"every plugin in the catalogue","items":{"type":"object","properties":{"name":{"type":"string"},"description":{"type":"string"},"installed":{"type":"boolean"},"kind":{"type":"string","description":"go (compiled in on its own tag) or python (served by the shared host)"},"tier":{"type":"string","description":"core or community"}}}}`)
}

func (p *PluginList) Execute(_ context.Context, _ map[string]any) (string, error) {
	return toolapi.StringResult(p.ExecuteTyped(nil, nil))
}

func (p *PluginList) ExecuteTyped(_ context.Context, _ map[string]any) (toolapi.ToolMessage, error) {
	type row struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Installed   bool   `json:"installed"`
		Kind        string `json:"kind"`
		Tier        string `json:"tier"`
	}
	// The catalogue is what could be installed; Missing says which of those this
	// binary lacks. Reading them together is the whole tool: the previous version
	// read a hardcoded Go slice holding one entry, so a plugin that was running
	// could not be named.
	entries := plugins.Installable()
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	absent := map[string]bool{}
	for _, n := range plugins.Missing(names) {
		absent[n] = true
	}

	rows := make([]row, 0, len(entries))
	var b strings.Builder
	for _, e := range entries {
		installed := !absent[e.Name]
		rows = append(rows, row{e.Name, e.Description, installed, e.Kind, e.Tier})
		state := "not installed"
		if installed {
			state = "installed"
		}
		b.WriteString(fmt.Sprintf("- %s [%s]: %s\n", e.Name, state, e.Description))
	}
	if len(rows) == 0 {
		return toolapi.ToolEmpty("plugins", "the plugin catalogue is empty"), nil
	}
	return toolapi.ToolOK("plugins", strings.TrimRight(b.String(), "\n"), rows), nil
}

var (
	_ toolapi.Tool      = (*PluginList)(nil)
	_ toolapi.Outputter = (*PluginList)(nil)
)

// -----------------------------------------------------------------------------
// plugin_install
// -----------------------------------------------------------------------------

// handoverDelay is how long the installed binary waits before replacing this
// process. It has to outlast the delivery of the answer that says it is going to
// happen, and nothing observable depends on it being short.
const handoverDelay = 20 * time.Second

// PluginInstall builds this binary again with a plugin in it, proves the result
// boots, replaces the binary, and hands over.
//
// Impact is Control, not Affect. Installing a plugin links its code into kaiju's
// own binary: no sandbox, no path check, no impact tier of its own, and from then
// on it is as privileged as every builtin. That is a larger act than writing a
// file, and the tier is where that gets said.
type PluginInstall struct {
	cfg PluginConfig
}

func NewPluginInstall(cfg PluginConfig) *PluginInstall { return &PluginInstall{cfg: cfg} }

func (p *PluginInstall) Name() string { return "plugin_install" }

func (p *PluginInstall) Description() string {
	return "Install a plugin by name from the catalogue, which rebuilds kaiju with it and " +
		"restarts into the new binary. Use ONLY when the user asks for a plugin or for a " +
		"capability plugin_list shows as not installed. Names come from plugin_list; anything " +
		"else is refused. The rebuild needs kaiju's source tree and a Go toolchain on this " +
		"machine — without them this reports that it cannot install here, which is an answer, " +
		"not a failure to work around. It takes a minute or so and ends by restarting, so say " +
		"what you are doing before calling it."
}

func (p *PluginInstall) Impact(map[string]any) int { return toolapi.ImpactControl }

func (p *PluginInstall) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"The plugin to install, exactly as plugin_list names it"}},"required":["name"],"additionalProperties":false}`)
}

func (p *PluginInstall) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(`{"type":"object","properties":{"name":{"type":"string"},"installed":{"type":"array","items":{"type":"string"},"description":"every plugin the new binary carries"},"restarting_in_seconds":{"type":"integer"}}}`)
}

func (p *PluginInstall) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(p.ExecuteTyped(ctx, params))
}

func (p *PluginInstall) ExecuteTyped(_ context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	name, _ := params["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return toolapi.ToolFail("plugin", "name is required — call plugin_list for the names", nil), nil
	}
	if p.cfg == nil {
		return toolapi.ToolFail("plugin", "no config is wired up, so an installed plugin could not be recorded and would vanish on the next restart", nil), nil
	}
	entry, ok := plugins.Lookup(name)
	if !ok {
		var have []string
		for _, e := range plugins.Installable() {
			have = append(have, e.Name)
		}
		return toolapi.ToolFail("plugin", fmt.Sprintf("the catalogue does not list %q, so it cannot be installed here. It lists: %s",
			name, strings.Join(have, ", ")), nil), nil
	}
	if entry.Tier == "community" && entry.Source != "" {
		// Nothing fetches a remote source yet, and the agent is not the thing that
		// should start. Compiling code from a URL into this binary is the most
		// dangerous step in the whole design.
		return toolapi.ToolFail("plugin", fmt.Sprintf("%q is a community plugin served from %s. Fetching and compiling third-party code is an operator action, not mine — it has to be vendored into the tree first", name, entry.Source), nil), nil
	}
	if len(plugins.Missing([]string{name})) == 0 {
		return toolapi.ToolOK("plugin", fmt.Sprintf("%s is already installed in this binary", name),
			map[string]any{"name": name, "installed": plugins.Compiled()}), nil
	}
	// Say why it cannot be done before attempting it, so the answer names the
	// missing precondition rather than quoting a compiler.
	if err := plugins.CanBuild(); err != nil {
		return toolapi.ToolFail("plugin", fmt.Sprintf("cannot install %s here: %v", name, err), nil), nil
	}

	// Everything already wanted, plus this. Rebuilding with only the new name
	// would drop the plugins this installation already runs.
	want := appendUnique(p.cfg.PluginNames(), name)
	if err := plugins.Rebuild(want); err != nil {
		return toolapi.ToolFail("plugin", fmt.Sprintf("installing %s failed, and the running binary was left untouched: %v", name, err), nil), nil
	}
	// Persist before handing over: the new process reads this to know what it
	// should be, and an unrecorded install would be rebuilt away on the next start.
	if err := p.cfg.SetPluginNames(want); err != nil {
		log.Printf("[plugin] installed %s but could not record it in config: %v", name, err)
	}
	plugins.ArmReExec(handoverDelay)

	return toolapi.ToolOK("plugin",
		fmt.Sprintf("%s is built into the binary. kaiju restarts into it in about %d seconds, and %s is usable after that.",
			name, int(handoverDelay.Seconds()), name),
		map[string]any{
			"name":                  name,
			"installed":             want,
			"restarting_in_seconds": int(handoverDelay.Seconds()),
		}), nil
}

var (
	_ toolapi.Tool      = (*PluginInstall)(nil)
	_ toolapi.Outputter = (*PluginInstall)(nil)
)

// -----------------------------------------------------------------------------
// The Python host, supervised
// -----------------------------------------------------------------------------

// HostSupervisor brings up the shared Python plugin host before the bridge tries
// to talk to it.
//
// Order is the point. The bridge registers a host's tools when it activates, and
// that happens once at startup — so if the host is not answering then, the bridge
// contributes nothing and the plugins are silently absent. This used to be patched
// afterwards by re-registering through a second Host implementation; starting the
// host first makes that unnecessary and the second implementation went away.
type HostSupervisor struct {
	cfg       PluginConfig
	svc       *Service
	workspace string
}

func NewHostSupervisor(cfg PluginConfig, svc *Service, workspace string) *HostSupervisor {
	return &HostSupervisor{cfg: cfg, svc: svc, workspace: workspace}
}

// defaultStartCmd launches the reference host. {plugins} is resolved at use time
// because the one time it was written down it was an absolute path to one
// developer's machine; {workspace} travels because the remote protocol carries no
// workspace, so a plugin that touches files is told where its sandbox is and
// enforces it itself.
const defaultStartCmd = "{plugins}/start.sh {port} {workspace}"

// EnsureUp makes sure the host serving `want`'s Python plugins is answering,
// starting it through the service manager when it is not. It returns before
// activation so the bridge finds a live host, and reports what it could not do
// rather than failing the boot: a plugin whose host is down should leave kaiju
// running without it.
func (h *HostSupervisor) EnsureUp(want []string) {
	if h.cfg == nil {
		return
	}
	var enabled []plugins.Entry
	for _, n := range want {
		if e, ok := plugins.Lookup(strings.TrimSpace(n)); ok && e.Kind == plugins.KindPython {
			enabled = append(enabled, e)
		}
	}
	if len(enabled) == 0 {
		return
	}
	hostURL := strings.TrimRight(h.cfg.PluginHost(), "/")
	if hostURL == "" {
		hostURL = fmt.Sprintf("http://127.0.0.1:%d", enabled[0].Port)
	}
	// The bridge reads this; set it whether or not we start anything, so a host
	// someone else is running is still found.
	os.Setenv("KAIJU_PLUGIN_HOST", hostURL)
	if hostUp(hostURL) {
		return
	}

	startCmd := h.cfg.PluginHostStart()
	if startCmd == "" {
		startCmd = defaultStartCmd
	}
	port := strconv.Itoa(enabled[0].Port)
	if u, err := url.Parse(hostURL); err == nil && u.Port() != "" {
		port = u.Port()
	}
	cmd := strings.ReplaceAll(startCmd, "{port}", port)
	if strings.Contains(cmd, "{plugins}") {
		dir := plugins.PluginsDir()
		if dir == "" {
			log.Printf("[plugin] the host's folder is not beside this binary or in the working directory, so it cannot be started here; run it yourself and set remote_plugin_host")
			return
		}
		cmd = strings.ReplaceAll(cmd, "{plugins}", dir)
	}
	cmd = strings.ReplaceAll(cmd, "{workspace}", h.workspace)
	// Which plugins the host should load. Without it the host loads every folder
	// on disk, so "what is enabled" had two different answers on the two sides.
	var names []string
	for _, e := range enabled {
		names = append(names, e.Name)
	}
	cmd = "KAIJU_PLUGINS=" + strings.Join(names, ",") + " " + cmd

	if h.svc == nil {
		log.Printf("[plugin] no service manager, so the plugin host at %s was not started", hostURL)
		return
	}
	pnum, _ := strconv.Atoi(port)
	log.Printf("[plugin] host at %s not answering — starting it for %s", hostURL, strings.Join(names, ", "))
	if err := h.svc.StartManaged("plugin-host", cmd, "", pnum); err != nil {
		log.Printf("[plugin] couldn't start the plugin host: %v", err)
		return
	}
	if !waitHostUp(hostURL, 30*time.Second) {
		log.Printf("[plugin] the plugin host at %s did not answer within 30s; check `service logs plugin-host`", hostURL)
	}
}

// waitHostUp polls until the host answers or the budget runs out.
func waitHostUp(base string, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if hostUp(base) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// hostUp asks the host for its manifest. /plugins rather than /health because the
// manifest is what the bridge needs: a process that is listening but cannot
// describe its plugins is not up for our purposes.
func hostUp(base string) bool {
	req, err := http.NewRequest("GET", strings.TrimRight(base, "/")+"/plugins", nil)
	if err != nil {
		return false
	}
	if tok := os.Getenv("KAIJU_PLUGIN_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// appendUnique adds s to ss if it is not already there, preserving order.
func appendUnique(ss []string, s string) []string {
	for _, x := range ss {
		if x == s {
			return ss
		}
	}
	return append(append([]string{}, ss...), s)
}
