package tools

import "testing"

// A refusal is recognised in Windows' words too, so a locked command's
// failure names the level instead of reading as a fault.
func TestRefusedKnowsWindowsWords(t *testing.T) {
	for _, s := range []string{
		"Access is denied.",
		`Set-Content : Access to the path 'C:\Windows\x' is denied.`,
		"+ FullyQualifiedErrorId : System.UnauthorizedAccessException",
		"rm: cannot remove 'x': Permission denied",
	} {
		if !refused(s) {
			t.Errorf("not recognised as a refusal: %q", s)
		}
	}
	if refused("file not found") {
		t.Error("an ordinary failure read as a refusal")
	}
}
