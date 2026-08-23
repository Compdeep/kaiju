package tools

import (
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Impact answers two different questions with one method, and the second one
// is easy to answer wrongly.
//
// With parameters it means "what tier is THIS call" — bash("ls") is observe,
// bash("rm -rf /") is control. That is the design and it is what the gate uses.
//
// With no parameters it means "what tier is this TOOL", asked when deciding
// whether to offer it at all. A tool that grades itself by reading its
// parameters finds none, matches nothing, and answers with its cheapest case —
// which is the unsafe direction for that question. A shell with no command in
// it is not a read-only tool; it is a shell nobody has typed into yet.
//
// So: the abstract answer is the worst case. This test holds every graded tool
// to it, by asking each one about calls it would rate highly and requiring the
// no-parameter answer to be at least as high.

func TestTheAbstractImpactIsTheWorstCase(t *testing.T) {
	cases := []struct {
		name  string
		tool  toolapi.Tool
		calls []map[string]any
	}{
		{"bash", NewBash("", ""), []map[string]any{
			{"command": "ls"},
			{"command": "echo hello > /etc/hosts"},
			{"command": "rm -rf /"},
		}},
		{"git", NewGit(), []map[string]any{
			{"action": "status"},
			{"action": "commit"},
			{"action": "push"},
			{"action": "reset"},
		}},
		{"clipboard", NewClipboard(), []map[string]any{
			{"action": "read"},
			{"action": "write"},
		}},
		{"service", NewService(""), []map[string]any{
			{"action": "status"},
			{"action": "start"},
			{"action": "remove"},
		}},
		{"env_list", NewEnvList(), []map[string]any{{}}},
		{"archive", NewArchive(), []map[string]any{{"action": "extract"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			abstract := tc.tool.Impact(nil)
			worst := 0
			var worstCall map[string]any
			for _, call := range tc.calls {
				if got := tc.tool.Impact(call); got > worst {
					worst, worstCall = got, call
				}
			}
			if abstract < worst {
				t.Errorf("Impact(nil) = %d but Impact(%v) = %d — asked what tier this tool is, "+
					"it answers with a cheaper tier than a call it accepts. Whatever decides "+
					"whether to offer this tool is being told it is safer than it is.",
					abstract, worstCall, worst)
			}
		})
	}
}

// The other half: grading still works for a real call. A rule that made every
// tool answer control would satisfy the test above and destroy the point of
// per-invocation impact.
func TestAGradedToolStillGradesARealCall(t *testing.T) {
	b := NewBash("", "")
	if got := b.Impact(map[string]any{"command": "ls -la"}); got != toolapi.ImpactObserve {
		t.Errorf("bash(ls) = %d, want observe — reading a directory is not a control action", got)
	}
	if got := b.Impact(map[string]any{"command": "rm -rf /"}); got != toolapi.ImpactControl {
		t.Errorf("bash(rm -rf /) = %d, want control", got)
	}

	g := NewGit()
	if got := g.Impact(map[string]any{"action": "status"}); got != toolapi.ImpactObserve {
		t.Errorf("git status = %d, want observe", got)
	}
	if got := g.Impact(map[string]any{"action": "push"}); got != toolapi.ImpactControl {
		t.Errorf("git push = %d, want control", got)
	}
}

// A shell is not offered by default. This is the consequence the rule exists
// for, stated where a change to it would be noticed: an application deciding
// what to enable from the abstract impact must not be told a shell is
// read-only.
// Cutting a host off the network is a control action wherever it is done. The
// pattern named shutdown, reboot and halt but no firewall command, so a rule
// that drops every packet from an address rated observe — the tier a listing
// gets — and an agent could reach through the shell for what a firewall tool
// is gated for.
func TestTheShellRatesAFirewallChangeAsControl(t *testing.T) {
	b := NewBash("", "")
	for _, cmd := range []string{
		"iptables -A INPUT -s 203.0.113.9 -j DROP",
		"ip6tables -A OUTPUT -d 2001:db8::1 -j DROP",
		"netsh advfirewall firewall add rule name=block dir=in action=block remoteip=203.0.113.9",
	} {
		if got := b.Impact(map[string]any{"command": cmd}); got != toolapi.ImpactControl {
			t.Errorf("%q = %d, want control", cmd, got)
		}
	}
}

func TestAShellIsNotAReadOnlyTool(t *testing.T) {
	if got := NewBash("", "").Impact(nil); got != toolapi.ImpactControl {
		t.Errorf("bash's abstract impact = %d, want control — anything gating on this "+
			"would ship an unrestricted shell enabled", got)
	}
}
