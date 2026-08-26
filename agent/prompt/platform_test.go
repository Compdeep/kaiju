package prompt

import (
	"regexp"
	"strings"
	"testing"
)

// Platform leakage: one operating system's vocabulary inside a prompt.
//
// The same shape as the contamination guard in the agent package, and the same
// reason. A prompt is read on every host this engine runs on. Naming a command
// in it asserts that the command exists there, and nothing checks that — so the
// assertion is made once, by whoever was on Linux that day, and is wrong for
// every Windows deployment from then on.
//
// It got in exactly that way. The `bash` tool is called "bash" on every
// platform — the name is an identity a plan writes, not an executable — and on
// Windows NewBash runs PowerShell behind it. A prompt saying "use grep" told the
// planner to write something PowerShell cannot run, and the tool's own
// description, which is the one thing that knows which shell is live, said
// nothing at all. Both halves have been fixed; this stops the prompt half
// coming back.
//
// The rule is not "never mention a command". It is that a prompt says WHAT to
// do and the tool says HOW to say it: the tool's description is built from the
// shell it actually holds, so it is right on every host by construction. A
// prompt cannot be.
//
// If one of these words is genuinely wanted — a package manager named in a
// sentence about package managers, say — the fix is to say so here and take it
// off the list, not to work around the check.
// posixOnly are commands and paths that exist on a POSIX host and not on a
// Windows one. Matched as whole words, because "sed" is inside "used" and
// "addressed" and a substring check reports the prompts as riddled with
// commands they never mention.
//
// Deliberately NOT here: && , $( , 2>&1. PowerShell and cmd both understand
// them, so naming them in a prompt is not a claim about the platform.
var posixOnly = []*regexp.Regexp{
	// Text and file handling. The Windows shells have none of these.
	regexp.MustCompile(`\b(grep|sed|awk|chmod|chown|sudo)\b`),
	// Invocations with a POSIX flag, which is how these appear when a prompt is
	// telling the model to run one.
	regexp.MustCompile(`\b(ls|cat|tail|head|wc|du|df) +-`),
	// Paths that only exist on a POSIX filesystem.
	regexp.MustCompile(`(/dev/null|/usr/bin|/tmp/)`),
	// A runtime whose launcher is named differently on Windows.
	regexp.MustCompile(`\bpython3\b`),
}

// allowed carries the mentions that are deliberate, with the reason.
var allowed = map[string]string{
	// A sentence ABOUT package managers has to name them to be about anything,
	// and the point it makes — that these are outside the agent's control — is
	// true on every platform.
	"apt/brew/yum":       "names package managers to say they are NOT the debugger's territory",
	"npm/pip/cargo":      "names package managers to say command-not-found for their tools IS fixable",
	"`pip install`":      "one clause about a missing Python library, true wherever pip is",
	"use pip to install": "the same clause",
}

// TestNoPlatformLeakageInPrompts reads every prompt and reports a command named
// as though it exists everywhere.
func TestNoPlatformLeakageInPrompts(t *testing.T) {
	for name, body := range targets {
		for _, line := range strings.Split(*body, "\n") {
			lower := strings.ToLower(line)
			if excused(line) {
				continue
			}
			for _, re := range posixOnly {
				hit := re.FindString(lower)
				if hit == "" {
					continue
				}
				t.Errorf("the %s prompt names %q, which does not exist on a Windows host:\n    %s\n"+
					"  A prompt says WHAT to do; the bash tool's description says HOW to say it, "+
					"and it is built from the shell that deployment actually runs.",
					name, strings.TrimSpace(hit), strings.TrimSpace(line))
			}
		}
	}
}

// excused reports whether a line carries one of the deliberate mentions.
func excused(line string) bool {
	for phrase := range allowed {
		if strings.Contains(line, phrase) {
			return true
		}
	}
	return false
}

// A prompt that tells the model which shell to write for, without naming one,
// is what replaces the commands. Losing that sentence would leave the planner
// with a tool called "bash" and nothing pointing at its description.
func TestThePromptsSendTheModelToTheToolForItsShell(t *testing.T) {
	var found bool
	re := regexp.MustCompile(`(?i)shell that (the )?.?bash.? tool says it runs|shell the .?bash.? tool says it runs|its description names the one live on this host`)
	for _, body := range targets {
		if re.MatchString(*body) {
			found = true
			break
		}
	}
	if !found {
		t.Error("no prompt tells the model to write for the shell the bash tool declares. " +
			"Without it the tool's name — \"bash\" on every platform — is the only signal, " +
			"and on Windows it is the wrong one.")
	}
}
