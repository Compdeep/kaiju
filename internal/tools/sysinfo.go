package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/user/kaiju/internal/agent/tools"
)

/*
 * Sysinfo returns basic system information including hostname, OS, architecture, and time.
 * desc: Tool that gathers and returns system metadata as a JSON object.
 */
type Sysinfo struct{}

/*
 * NewSysinfo creates a new Sysinfo tool instance.
 * desc: Returns a zero-value Sysinfo ready for use.
 * return: pointer to a new Sysinfo
 */
func NewSysinfo() *Sysinfo { return &Sysinfo{} }

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
func (s *Sysinfo) Impact(map[string]any) int { return tools.ImpactObserve }

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
	return json.RawMessage(`{"type":"object","description":"System information. Chain individual fields into downstream steps via param_refs.","properties":{"hostname":{"type":"string","description":"machine hostname"},"os":{"type":"string","description":"operating system name (e.g. linux, darwin, windows)"},"arch":{"type":"string","description":"CPU architecture"},"cwd":{"type":"string","description":"current working directory path"},"time":{"type":"string","description":"current time"},"cpus":{"type":"integer","description":"number of CPU cores"}}}`)
}

/*
 * Execute gathers and returns system information as a JSON string.
 * desc: Collects hostname, OS, architecture, working directory, current UTC time, and CPU count.
 * param: _ - unused context
 * param: _ - unused parameters
 * return: JSON string with system information fields
 */
func (s *Sysinfo) Execute(_ context.Context, _ map[string]any) (string, error) {
	hostname, _ := os.Hostname()
	cwd, _ := os.Getwd()

	info := map[string]any{
		"hostname": hostname,
		"os":       runtime.GOOS,
		"arch":     runtime.GOARCH,
		"cwd":      cwd,
		"time":     time.Now().UTC().Format(time.RFC3339),
		"cpus":     runtime.NumCPU(),
	}
	b, _ := json.Marshal(info)
	return string(b), nil
}

// Verify interface compliance at compile time.
var _ tools.Tool = (*Sysinfo)(nil)
var _ tools.Outputter = (*Sysinfo)(nil)

func init() {
	// Ensure sysinfo is always available as a reference tool.
	_ = fmt.Sprintf
}
