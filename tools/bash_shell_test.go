package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// The tool is called "bash" on every platform — the name is an identity a plan
// writes, not an executable — and NewBash runs PowerShell behind it on Windows.
// So the name tells a planner something false, and nothing else told it
// anything: the description said "execute any command" and the parameter said
// "the shell command to execute".
//
// A planner reading that writes grep and sed, and PowerShell receives them.
func TestBashSaysWhichShellItRuns(t *testing.T) {
	for _, tc := range []struct {
		shell   string
		wants   []string
		refuses []string
	}{
		{"powershell", []string{"POWERSHELL", "Get-Content", "Select-String", "not present"}, nil},
		{"cmd", []string{"cmd.exe", "findstr", "not present"}, nil},
		{"sh", []string{"POSIX SHELL", "grep", "awk"}, []string{"not present"}},
	} {
		d := NewBash(tc.shell).Description()
		for _, want := range tc.wants {
			if !strings.Contains(d, want) {
				t.Errorf("%s: the description never says %q — a planner cannot tell which language to write:\n%s",
					tc.shell, want, d)
			}
		}
		for _, no := range tc.refuses {
			if strings.Contains(d, no) {
				t.Errorf("%s: the description says %q, which is wrong for this shell", tc.shell, no)
			}
		}
	}
}

// The schema is what a planner reads at the moment it writes the parameter, so
// it is the closest place to the mistake.
func TestBashParametersNameTheShell(t *testing.T) {
	for shell, want := range map[string]string{
		"powershell": "PowerShell",
		"cmd":        "cmd.exe",
		"sh":         "POSIX",
	} {
		raw := NewBash(shell).Parameters()
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: the schema is not valid JSON after the shell was folded in: %v", shell, err)
		}
		props := m["properties"].(map[string]any)
		desc, _ := props["command"].(map[string]any)["description"].(string)
		if !strings.Contains(desc, want) {
			t.Errorf("%s: the command parameter reads %q, and never names the shell", shell, desc)
		}
	}
}

// Auto-detection is what a deployment gets when it says nothing, and it must
// still describe itself correctly — an empty shell string used to mean "sh" in
// the description regardless of what NewBash actually chose.
func TestBashDescribesTheShellItActuallyChose(t *testing.T) {
	b := NewBash("") // auto
	d := b.Description()
	switch b.shell {
	case "powershell":
		if !strings.Contains(d, "POWERSHELL") {
			t.Error("auto-detected PowerShell, described something else")
		}
	default:
		if !strings.Contains(d, "POSIX SHELL") {
			t.Errorf("auto-detected %q, described something else", b.shell)
		}
	}
}
