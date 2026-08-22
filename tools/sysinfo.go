package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
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
 * effectiveUser names the account this process runs as.
 * desc: user.Current reads the password database, which a statically linked
 *       build cannot always do; the environment is the fallback, and an empty
 *       result leaves the field out rather than guessing at a name.
 * return: the account name, or empty when it cannot be established
 */
func effectiveUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	for _, key := range []string{"USER", "USERNAME", "LOGNAME"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure: what the machine is, what it is doing, and
 *       who this process is on it — the last of which is what turns a refusal by
 *       the operating system into something a reader can attribute.
 * return: JSON schema as raw bytes
 */
func (s *Sysinfo) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(`{"type":"object","description":"System information. Chain individual fields into downstream steps via ${step.N.<field>} placeholders.","properties":{"hostname":{"type":"string","description":"machine hostname"},"os":{"type":"string","description":"operating system name (e.g. linux, darwin, windows)"},"arch":{"type":"string","description":"CPU architecture"},"cwd":{"type":"string","description":"current working directory path"},"time":{"type":"string","description":"current time"},"cpus":{"type":"integer","description":"number of CPU cores"},"snapshot":{"type":"string","description":"uptime, total and free memory, and load average as the platform reports them; absent when the host would not say"},"user":{"type":"string","description":"the account this process runs as"},"uid":{"type":"integer","description":"its numeric user id; absent on platforms that do not have one"},"root":{"type":"boolean","description":"true when this process runs as root. When false, writing outside this user\u0027s own files, changing service configuration, and installing software will be refused by the operating system — a permission denied is that, not a missing file"},"path":{"type":"string","description":"the PATH this process searches for programs. A command not found means the program is not on THIS list, which is not the same as not installed: administrative programs usually live in /usr/sbin and /sbin, which a non-root PATH often omits"}}}`)
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

	// Who this process is, and what it can reach. Without these, a run that is
	// refused by the operating system has symptoms and no cause: a permission
	// denied writing under /etc, a command not found for a program that is
	// installed in /usr/sbin, and a service it cannot restart all read as
	// separate faults, and the run reported that the software was missing when
	// it was installed and running. One fact — this process is not root —
	// explains all three, and nothing was reporting it.
	if name := effectiveUser(); name != "" {
		info["user"] = name
	}
	// Geteuid is -1 on Windows, where the question does not apply, so the field
	// is absent there rather than answered wrongly.
	if uid := os.Geteuid(); uid >= 0 {
		info["uid"] = uid
		info["root"] = uid == 0
	}
	if path := os.Getenv("PATH"); path != "" {
		info["path"] = path
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
