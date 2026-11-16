package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// The objective is in the prompt because a command that no longer serves the
// goal is not a fix. Without it the model can "succeed" by narrowing the
// question — the failure mode the system prompt warns about.
func TestShellFixPrompt_CarriesObjectiveCommandAndError(t *testing.T) {
	got := shellFixPrompt("find the config under /etc", "find /etc -nam '*.conf'", "find: unknown predicate `-nam'")

	var back map[string]any
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("prompt is not the JSON object the schema promises: %v\n%s", err, got)
	}
	for k, want := range map[string]string{
		"objective": "find the config under /etc",
		"command":   "find /etc -nam '*.conf'",
		"result":    "find: unknown predicate `-nam'",
	} {
		if back[k] != want {
			t.Errorf("%s = %v, want %q", k, back[k], want)
		}
	}
	if len(back) != 3 {
		t.Errorf("expected exactly objective/command/result, got %v", back)
	}
}

// A node with no tag still gets a usable prompt — the objective is stated as
// absent rather than left blank, so the model is not guessing whether it was
// omitted or empty.
func TestShellFixPrompt_EmptyObjectiveIsStated(t *testing.T) {
	got := shellFixPrompt("   ", "ls /nope", "no such file")
	if !strings.Contains(got, "(not stated)") {
		t.Errorf("empty objective was left blank:\n%s", got)
	}
}

// A long error is truncated so one failure cannot fill the reply budget.
func TestShellFixPrompt_TruncatesTheError(t *testing.T) {
	got := shellFixPrompt("do a thing", "cmd", strings.Repeat("x", 5000))
	if len(got) > 1200 {
		t.Errorf("prompt is %d chars; the error was not truncated", len(got))
	}
}

// Models return the command in several shapes despite being asked for one.
// Each of these has been seen from some model; all must reduce to the command.
func TestCleanShellFix(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"bare", `find /etc -name '*.conf'`, `find /etc -name '*.conf'`},
		{"trailing newline", "ls -la /tmp\n", "ls -la /tmp"},
		{"backticked", "`ls -la`", "ls -la"},
		{"fenced", "```\nls -la\n```", "ls -la"},
		{"fenced with language", "```bash\nls -la\n```", "ls -la"},
		{"fenced with prose after", "```sh\nls -la\n```\nThis lists files.", "ls -la"},
		{"explanation on later lines", "ls -la\nThis lists the directory.", "ls -la"},
		{"leading blank lines", "\n\nls -la", "ls -la"},
		{"empty", "", ""},
		{"whitespace only", "   \n  ", ""},
	} {
		if got := cleanShellFix(tc.raw); got != tc.want {
			t.Errorf("%s: cleanShellFix(%q) = %q, want %q", tc.name, tc.raw, got, tc.want)
		}
	}
}

// A reply that reproduces the failing command is not a fix — the caller
// discards it, and this pins the comparison that decision rests on.
func TestCleanShellFix_UnchangedIsDetectable(t *testing.T) {
	cmd := "find /etc -nam '*.conf'"
	if cleanShellFix("```bash\n"+cmd+"\n```") != cmd {
		t.Error("a fenced reply did not reduce to the original command, so the unchanged-check would miss it")
	}
}

// Two attempts, and the SECOND must be built on how the FIRST one failed —
// that is the only reason a second call is worth making. Re-asking the same
// question at temperature 0 returns the same answer.
func TestShellFixPrompt_SecondAttemptCarriesTheNewError(t *testing.T) {
	first := shellFixPrompt("delete the temp files", `find /var/tmp -name '*.tmp' -exec rm {}`,
		"find: missing argument to `-exec'")
	second := shellFixPrompt("delete the temp files", `find /var/tmp -name '*.tmp' -exec rm {} \;`,
		"rm: cannot remove '/var/tmp/x.tmp': Permission denied")

	if first == second {
		t.Fatal("the second attempt sent an identical prompt; at temperature 0 it can only return the same fix")
	}
	var f, s map[string]any
	if err := json.Unmarshal([]byte(first), &f); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := json.Unmarshal([]byte(second), &s); err != nil {
		t.Fatalf("second: %v", err)
	}
	if f["result"] == s["result"] {
		t.Error("the second attempt carries the first attempt's error, not the new one")
	}
	if f["command"] == s["command"] {
		t.Error("the second attempt describes the original command, not the one that was actually run")
	}
	if f["objective"] != s["objective"] {
		t.Error("the objective changed between attempts; it is the one thing that must not")
	}
}

// The retry budget is bounded. Unbounded rewriting is how one broken step
// consumes a run.
func TestShellFixAttemptsIsBounded(t *testing.T) {
	if shellFixAttempts < 2 {
		t.Errorf("shellFixAttempts = %d; a second attempt is the point of the change", shellFixAttempts)
	}
	if shellFixAttempts > 3 {
		t.Errorf("shellFixAttempts = %d; that is an unbounded-retry budget on one step", shellFixAttempts)
	}
}

// A fix must still do the command's work. Each refused case is from run
// fe1e1df3 (2026-11-16), where a failing script was turned into a success.
func TestShellFixChangesTheWork(t *testing.T) {
	script := "python3 -c \"\nfrom skyfield.api import load\nplanets = load('media/de421.bsp')\nprint(planets)\n\""
	for _, tc := range []struct {
		name, original, fixed string
		refused               bool
	}{
		{"replaced by hello", script, `python3 -c "print('hello')"`, true},
		{"output piped to head", "python3 project/x/compute_ssb.py", "python3 project/x/compute_ssb.py 2>&1 | head -50", true},
		{"failure swallowed", "ls /etc/ssh/sshd_config", "ls /etc/ssh/sshd_config || true", true},
		{"engine wrapper dropped", "( python3 project/x/compute_ssb.py ) 2>&1", "python3 project/x/compute_ssb.py", false},
		{"quoting fixed", `grep -r "foo bar /etc/hosts`, `grep -r "foo bar" /etc/hosts`, false},
		{"pipe already there", "cat /var/log/syslog | grep error", "cat /var/log/syslog | grep -i error", false},
		{"flag fixed", "find /etc -nam '*.conf'", "find /etc -name '*.conf'", false},
		{"nothing to anchor on", "uptime -x", "uptime", false},
		{"path typo corrected", "cat /etc/hostnme", "cat /etc/hostname", false},
		{"other file, same directory", "cat /var/log/syslog.1", "cat /var/log/syslog", false},
		{"cut to an open quote", script, `python3 -c "`, true},
		{"ends in true", "python3 project/x/run.py", "python3 project/x/run.py; true", true},
		{"exit 0 on a new line", script, script + "\nexit 0", true},
		{"different file elsewhere", "python3 project/x/compute_ssb.py", "python3 /tmp/other.py", true},
	} {
		why := shellFixChangesTheWork(tc.original, tc.fixed)
		if (why != "") != tc.refused {
			t.Errorf("%s: refused=%v (%q), want refused=%v", tc.name, why != "", why, tc.refused)
		}
	}
}

// A command over several lines is fixed over several lines: taking the
// reply's first line cut a python3 -c script to `python3 -c "`.
func TestCleanShellFixLines_KeepsAMultiLineCommand(t *testing.T) {
	cmd := "python3 -c \"\nfrom skyfield.api import load\nprint(load)\n\""
	if got := cleanShellFixLines("```bash\n" + cmd + "\n```"); got != cmd {
		t.Errorf("got %q, want %q", got, cmd)
	}
	if got := cleanShellFixLines(cmd); got != cmd {
		t.Errorf("unfenced: got %q, want %q", got, cmd)
	}
}
