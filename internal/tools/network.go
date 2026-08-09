package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"time"

	agenttools "github.com/Compdeep/kaiju/agent/tools"
)

// ─── NetInfo ────────────────────────────────────────────────────────────────

/*
 * NetInfo returns network interface information and performs connectivity checks.
 * desc: Tool for listing network interfaces, checking host reachability, resolving DNS, and listing listening ports.
 */
type NetInfo struct{}

/*
 * NewNetInfo creates a new NetInfo tool instance.
 * desc: Returns a zero-value NetInfo ready for use.
 * return: pointer to a new NetInfo
 */
func NewNetInfo() *NetInfo { return &NetInfo{} }

/*
 * Name returns the tool identifier.
 * desc: Returns "net_info" as the tool name.
 * return: the string "net_info"
 */
func (n *NetInfo) Name() string { return "net_info" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains the available network actions: interfaces, connectivity, dns, and ports.
 * return: description string
 */
func (n *NetInfo) Description() string {
	return "List network interfaces with IP addresses, or check connectivity to a host."
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactObserve since querying network info is non-destructive.
 * param: _ - unused parameters
 * return: ImpactObserve (0)
 */
func (n *NetInfo) Impact(map[string]any) int { return agenttools.ImpactObserve }

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines the required action enum and optional host/port parameters for connectivity and DNS checks.
 * return: JSON schema as raw bytes
 */
func (n *NetInfo) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["interfaces", "connectivity", "dns", "ports"],
				"description": "Action: interfaces (list IPs), connectivity (check host reachability), dns (resolve hostname), ports (list listening ports)"
			},
			"host": {"type": "string", "description": "Hostname or IP for connectivity/dns checks"},
			"port": {"type": "integer", "description": "Port number for connectivity check"}
		},
		"required": ["action"],
		"additionalProperties": false
	}`)
}

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure with action type and result string.
 * return: JSON schema as raw bytes
 */
func (n *NetInfo) OutputSchema() json.RawMessage {
	return agenttools.EnvelopeSchema("")
}

/*
 * Execute performs the specified network action.
 * desc: Routes to the appropriate handler based on action: interfaces, connectivity, dns, or ports.
 * param: ctx - context for cancellation and timeout
 * param: params - must contain "action"; optionally "host" and "port" for connectivity/dns checks
 * return: JSON string with network information, or error for unknown actions or missing params
 */
// Execute satisfies the Tool interface for callers outside the DAG.
func (n *NetInfo) Execute(ctx context.Context, params map[string]any) (string, error) {
	return agenttools.StringResult(n.ExecuteTyped(ctx, params))
}

func (n *NetInfo) ExecuteTyped(ctx context.Context, params map[string]any) (agenttools.ToolMessage, error) {
	action, _ := params["action"].(string)

	switch action {
	case "interfaces":
		return netInterfaces()
	case "connectivity":
		host, _ := params["host"].(string)
		port := 443
		if p, ok := agenttools.ParamNum(params, "port"); ok && p > 0 {
			port = int(p)
		}
		return netConnectivity(ctx, host, port)
	case "dns":
		host, _ := params["host"].(string)
		return netDNS(ctx, host)
	case "ports":
		return netListeningPorts(ctx)
	default:
		return agenttools.ToolMessage{}, fmt.Errorf("net_info: unknown action %q (use: interfaces, connectivity, dns, ports)", action)
	}
}

/*
 * netInterfaces lists active network interfaces with their addresses.
 * desc: Queries the OS for network interfaces that are up and returns their name, flags, MAC, and IP addresses as JSON.
 * return: JSON string with interface details, or error on failure
 */
func netInterfaces() (agenttools.ToolMessage, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return agenttools.ToolMessage{}, fmt.Errorf("net_info: %w", err)
	}

	type ifaceInfo struct {
		Name  string   `json:"name"`
		Flags string   `json:"flags"`
		MAC   string   `json:"mac,omitempty"`
		Addrs []string `json:"addrs"`
	}

	var result []ifaceInfo
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		addrStrings := make([]string, 0, len(addrs))
		for _, a := range addrs {
			addrStrings = append(addrStrings, a.String())
		}
		result = append(result, ifaceInfo{
			Name:  iface.Name,
			Flags: iface.Flags.String(),
			MAC:   iface.HardwareAddr.String(),
			Addrs: addrStrings,
		})
	}

	return agenttools.ToolOK("net", "", map[string]any{"action": "interfaces", "interfaces": result}), nil
}

/*
 * netConnectivity checks TCP connectivity to a host and port.
 * desc: Attempts a TCP dial with a 5-second timeout and reports reachability and latency.
 * param: ctx - context for cancellation
 * param: host - hostname or IP to connect to
 * param: port - TCP port number to connect to
 * return: JSON string with reachability status and latency, or error if host is empty
 */
func netConnectivity(ctx context.Context, host string, port int) (agenttools.ToolMessage, error) {
	if host == "" {
		return agenttools.ToolMessage{}, fmt.Errorf("net_info: host is required for connectivity check")
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	start := time.Now()
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
	elapsed := time.Since(start)

	if err != nil {
		return agenttools.ToolOK("net", "", map[string]any{"action": "connectivity", "host": host, "port": port, "reachable": false, "error": err.Error(), "latency_ms": elapsed.Milliseconds()}), nil
	}
	conn.Close()
	return agenttools.ToolOK("net", "", map[string]any{"action": "connectivity", "host": host, "port": port, "reachable": true, "latency_ms": elapsed.Milliseconds()}), nil
}

/*
 * netDNS resolves a hostname to its IP addresses.
 * desc: Uses the system DNS resolver to look up all addresses for the given hostname.
 * param: ctx - context for cancellation
 * param: host - hostname to resolve
 * return: JSON string with resolved addresses, or error if host is empty
 */
func netDNS(ctx context.Context, host string) (agenttools.ToolMessage, error) {
	if host == "" {
		return agenttools.ToolMessage{}, fmt.Errorf("net_info: host is required for DNS lookup")
	}

	resolver := &net.Resolver{}
	addrs, err := resolver.LookupHost(ctx, host)
	if err != nil {
		return agenttools.ToolOK("net", "", map[string]any{"action": "dns", "host": host, "error": err.Error()}), nil
	}

	return agenttools.ToolOK("net", "", map[string]any{"action": "dns", "host": host, "addresses": addrs}), nil
}

/*
 * netListeningPorts lists TCP ports currently in the LISTEN state.
 * desc: Runs ss/netstat (Linux), lsof (macOS), or Get-NetTCPConnection (Windows) to find listening ports.
 * param: ctx - context for cancellation
 * return: formatted text output of listening ports (truncated to 4KB), or error on command failure
 */
func netListeningPorts(ctx context.Context) (agenttools.ToolMessage, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command",
			"Get-NetTCPConnection -State Listen | Select-Object LocalAddress, LocalPort, OwningProcess | Format-Table -AutoSize")
	case "darwin":
		cmd = exec.CommandContext(ctx, "lsof", "-iTCP", "-sTCP:LISTEN", "-P", "-n")
	default:
		cmd = exec.CommandContext(ctx, "ss", "-tlnp")
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		// Fallback for systems without ss
		if runtime.GOOS == "linux" {
			cmd = exec.CommandContext(ctx, "netstat", "-tlnp")
			out, err = cmd.CombinedOutput()
			if err != nil {
				return agenttools.ToolMessage{}, fmt.Errorf("net_info: %w", err)
			}
		} else {
			return agenttools.ToolMessage{}, fmt.Errorf("net_info: %w", err)
		}
	}

	output := string(out)
	if len(output) > 4096 {
		output = output[:4096] + "\n... (truncated)"
	}
	return agenttools.ToolOK("net", output, nil), nil
}

var _ agenttools.Tool = (*NetInfo)(nil)
var _ agenttools.Outputter = (*NetInfo)(nil)
