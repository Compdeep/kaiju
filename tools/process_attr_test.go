package tools

import (
	"os/exec"
	"syscall"
	"testing"
)

// Putting a command in its own group or session adds to its SysProcAttr and
// keeps what was there: toolapi.Command sets the token that locks a command on
// Windows there, and a helper that replaced the struct started bash unlocked.
func TestOwnGroupKeepsTheCommandsOtherSettings(t *testing.T) {
	for name, set := range map[string]func(*exec.Cmd){"group": setOwnProcessGroup, "session": setOwnSession} {
		cmd := exec.Command("true")
		cmd.SysProcAttr = &syscall.SysProcAttr{}
		before := cmd.SysProcAttr
		markSysProcAttr(cmd.SysProcAttr)
		set(cmd)
		if cmd.SysProcAttr != before || !sysProcAttrMarked(cmd.SysProcAttr) {
			t.Errorf("%s: the command's existing settings were replaced", name)
		}
	}
}
