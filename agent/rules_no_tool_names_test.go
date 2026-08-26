package agent

import (
	"context"
	"strings"
	"testing"
)

// Tools the Rules section is allowed to name. Each is either always present or
// governed by a rule about HOW to use it that is meaningless without the name —
// "never use bash for a foreground server", "use service for daemons", "compute
// is for writing code". Naming these costs nothing: the rule is about conduct,
// not about choosing them.
var rulesMayName = map[string]bool{
	"bash": true, "compute": true, "service": true, "file_read": true,
	"file_write": true, "file_list": true, "git": true,
}

// Tools the Rules section must NOT name. A rule that names one of these
// advertises it to every run, including the ones where preflight did not select
// it and the planner has no signature for it — so the planner reaches for a tool
// it cannot see the parameters of, and the tool index stops being the authority
// on what this run can call. It is how web_search came to be called in runs that
// never chose it.
//
// Maintenance: a tool added to the registry is not added here automatically.
// This is the list of names that have leaked or would leak; extend it when a new
// domain tool starts appearing in generic guidance.
var rulesMustNotName = []string{
	"web_search", "web_fetch", "web_research",
	"process_kill", "process_list", "sysinfo", "env_list", "disk_usage",
	"net_info", "archive", "clipboard", "office_extract", "panel_push",
	"memory_store", "memory_recall", "memory_search", "message_search",
	"image_read", "edit_file",
}

// rulesSection returns the "## Rules" block of the planner prompt, which is the
// part every run receives regardless of which tools it was given.
func rulesSection(t *testing.T) string {
	t.Helper()
	a := reframeAgent(t)
	prompt := a.executiveSystemPrompt(context.Background(), nil, nil, "", "", "")
	start := strings.Index(prompt, "## Rules")
	if start < 0 {
		t.Fatal("the planner prompt has no ## Rules section")
	}
	rest := prompt[start+len("## Rules"):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func TestRules_NameNoOptionalTool(t *testing.T) {
	rules := rulesSection(t)
	for _, name := range rulesMustNotName {
		if strings.Contains(rules, name) {
			t.Errorf("the Rules section names %q. Every run reads these rules, including runs "+
				"where preflight did not select that tool and the planner has no signature for "+
				"it. Say what to do, not which tool does it, and let the tool index answer that.", name)
		}
	}
}

// The allowlist is not a licence to stop checking: a name on it should still be
// there for a reason. This asserts the ones that are load-bearing are present,
// so a rewrite that quietly drops the bash/service distinction is caught.
func TestRules_StillGovernTheAlwaysPresentTools(t *testing.T) {
	rules := rulesSection(t)
	for _, name := range []string{"bash", "service", "compute"} {
		if !rulesMayName[name] {
			t.Fatalf("test bug: %q is asserted but not allowed", name)
		}
		if !strings.Contains(rules, name) {
			t.Errorf("the Rules section no longer governs %q — the conduct rule for it was lost", name)
		}
	}
}
