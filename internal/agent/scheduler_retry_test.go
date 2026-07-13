package agent

import "testing"

// classifyRetryTier decides how a failed tool node is retried. The key
// regression this guards: a dependency-injection failure (a broken
// ${node.<id>.field} wiring) is STRUCTURAL — routing it to the "oneshot"
// shell-fixer LLM only mangles the placeholder into an invalid shell expansion
// ("sh: Bad substitution") and loops until the wall clock. It must be "skip".
func TestClassifyRetryTier(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want string
	}{
		// The bug: dependency-injection / template wiring failures are structural.
		{"injection failure skips", `dependency injection failed: template on n6: field "content" absent in dep n5`, "skip"},
		{"injection failure skips (dep missing)", "dependency injection failed: template on n6: dep node n5 not found", "skip"},

		// Other structural errors still skip.
		{"file not found skips", "bash: no such file or directory", "skip"},
		{"gate block skips", "gate: intent below clearance", "skip"},
		{"command timeout skips", "command timed out after 60s", "skip"},

		// Transient errors blind-rerun.
		{"conn refused reruns", "dial tcp: connection refused", "blind"},
		{"rate limit reruns", "provider returned http 429 rate limit", "blind"},

		// Genuine, non-structural command errors still get one LLM fix attempt.
		{"unknown flag → oneshot", "grep: unrecognized option '--foo'", "oneshot"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyRetryTier(c.err); got != c.want {
				t.Errorf("classifyRetryTier(%q) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}
