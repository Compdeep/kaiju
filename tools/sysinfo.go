package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

/*
 * Sysinfo returns basic system information including hostname, OS, architecture, and time.
 * desc: Tool that gathers and returns system metadata as a JSON object.
 */
type Sysinfo struct {
	workspace string
}

/*
 * NewSysinfo creates a new Sysinfo tool instance.
 * desc: Returns a zero-value Sysinfo ready for use.
 * return: pointer to a new Sysinfo
 */
func NewSysinfo(workspace ...string) *Sysinfo {
	s := &Sysinfo{}
	if len(workspace) > 0 {
		s.workspace = workspace[0]
	}
	return s
}

/*
 * Name returns the tool identifier.
 * desc: Returns "sysinfo" as the tool name.
 * return: the string "sysinfo"
 */
func (s *Sysinfo) Name() string { return "sysinfo" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that this tool returns system information such as hostname, OS, arch, cwd, and time.
 * return: description string
 */
func (s *Sysinfo) Description() string {
	return "Returns system information: hostname, OS, architecture, working directory, and current time."
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactObserve since reading system info is non-destructive.
 * param: _ - unused parameters
 * return: ImpactObserve (0)
 */
func (s *Sysinfo) Impact(map[string]any) int { return toolapi.ImpactObserve }

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Returns an empty object schema since sysinfo takes no parameters.
 * return: JSON schema as raw bytes
 */
func (s *Sysinfo) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure with hostname, os, arch, cwd, time, and cpus fields.
 * return: JSON schema as raw bytes
 */
func (s *Sysinfo) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(`{"type":"object","description":"System information. Chain individual fields into downstream steps via ${step.N.<field>} placeholders.","properties":{"hostname":{"type":"string","description":"machine hostname"},"os":{"type":"string","description":"operating system name (e.g. linux, darwin, windows)"},"arch":{"type":"string","description":"CPU architecture"},"cwd":{"type":"string","description":"current working directory path"},"time":{"type":"string","description":"current time"},"cpus":{"type":"integer","description":"number of CPU cores"},"snapshot":{"type":"string","description":"uptime, total and free memory, and load average as the platform reports them; absent when the host would not say"}}}`)
}

/*
 * Execute gathers and returns system information as a JSON string.
 * desc: Collects hostname, OS, architecture, working directory, current UTC time, and CPU count.
 * param: _ - unused context
 * param: _ - unused parameters
 * return: JSON string with system information fields
 */
// Execute satisfies the Tool interface for callers outside the DAG.
func (s *Sysinfo) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(s.ExecuteTyped(ctx, params))
}

func (s *Sysinfo) ExecuteTyped(ctx context.Context, _ map[string]any) (toolapi.ToolMessage, error) {
	hostname, _ := os.Hostname()
	cwd := s.workspace
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	// workspace was here as a second copy of cwd under another name, declared
	// nowhere, so it reached every consumer as an undeclared duplicate of the field
	// beside it. Found by comparing what each tool produces against what it declares.
	info := map[string]any{
		"hostname": hostname,
		"os":       runtime.GOOS,
		"arch":     runtime.GOARCH,
		"cwd":      cwd,
		"time":     time.Now().UTC().Format(time.RFC3339),
		"cpus":     runtime.NumCPU(),
	}
	// What the machine is doing, as opposed to what it is: uptime, memory
	// pressure, load. It costs a subprocess, so it is the one part of this tool
	// that can fail — and a failure only omits the field, because a host that
	// will not report its uptime still has a hostname and a clock, and failing
	// the whole call would lose those too.
	if snap, err := platformSnapshot(ctx); err == nil && snap != "" {
		info["snapshot"] = snap
	}

	// No empty case: the machine always has a hostname, an architecture and a
	// clock. Content is left empty on purpose — the payload IS the readable
	// form, and Evidence() falls back to it, so filling both would carry the
	// same JSON twice.
	return toolapi.ToolOK("sysinfo", "", info), nil
}

// Verify interface compliance at compile time.
var _ toolapi.Tool = (*Sysinfo)(nil)
var _ toolapi.Outputter = (*Sysinfo)(nil)
var _ toolapi.TypedExecutor = (*Sysinfo)(nil)

func init() {
	// Ensure sysinfo is always available as a reference tool.
	_ = fmt.Sprintf
}

// platformSnapshot asks the host what it is doing right now — uptime, memory,
// load — in whatever form that platform reports it.
//
// The text is left as the platform produced it rather than parsed into fields.
// Three platforms report three different shapes, and a parser for the two that
// cannot be tested here would be a guess presented as structure.
func platformSnapshot(ctx context.Context) (string, error) {
	// This tool answered without doing any I/O until the snapshot arrived, so a
	// caller passing a nil ctx was harmless and some do. exec.CommandContext
	// panics on one.
	if ctx == nil {
		ctx = context.Background()
	}
	switch runtime.GOOS {
	case "windows":
		cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command",
			"$os = Get-CimInstance Win32_OperatingSystem; "+
				"'Uptime: ' + (((Get-Date) - $os.LastBootUpTime).ToString('d\\.hh\\:mm\\:ss')) + "+
				"\"`nTotalMemMB: \" + [math]::Round($os.TotalVisibleMemorySize/1024,0) + "+
				"\"`nFreeMemMB: \" + [math]::Round($os.FreePhysicalMemory/1024,0) + "+
				"\"`nLoadAvg: \" + ((Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average).Average) + '%'")
		out, err := cmd.Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	case "linux":
		cmd := exec.CommandContext(ctx, "sh", "-c",
			"uptime && echo '---' && head -3 /proc/meminfo")
		out, err := cmd.Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	case "darwin":
		cmd := exec.CommandContext(ctx, "sh", "-c",
			"uptime && echo '---' && vm_stat | head -10")
		out, err := cmd.Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	return "", nil
}
