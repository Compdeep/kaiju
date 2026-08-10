package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// What the scheduler does when a node fails.
//
// Both of these are decisions inside runPlanAndSchedule's completion loop,
// reading local state that never leaves the function. Driving them needs a
// planner, a model and a run, so they are asserted against the source: the
// defect each guards is the line being dropped, and no test that could reach
// the loop would notice a line that is no longer there.

// failureBranch returns the source of the completion loop's failure handling,
// from the test for a failed node to the retry tiers below it.
func failureBranch(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("scheduler.go")
	if err != nil {
		t.Fatalf("read scheduler.go: %v", err)
	}
	text := string(src)
	start := strings.Index(text, "if comp.Err != nil {")
	if start < 0 {
		t.Fatal("no failure branch in scheduler.go — the shape this test reads has changed")
	}
	end := strings.Index(text[start:], "// ── Three-tier retry ──")
	if end < 0 {
		t.Fatal("the retry tiers no longer follow the failure branch")
	}
	return text[start : start+end]
}

// A reflection that failed clears the flag saying one is in flight.
//
// The flag is cleared where a reflection succeeds, and the failure branch
// returns before reaching there. So one failed reflection left it set for the
// rest of the run: no further reflection was ever scheduled, and observer and
// batch spawns piled up behind it.
func TestAFailedReflectionClearsTheInflightFlag(t *testing.T) {
	branch := failureBranch(t)
	if !strings.Contains(branch, "reflectionInflight = false") {
		t.Error("a node that failed does not clear reflectionInflight. If that node " +
			"was the reflection, nothing will schedule another one for the rest of " +
			"the run")
	}
	if !strings.Contains(branch, "NodeReflection") || !strings.Contains(branch, "NodeInterjection") {
		t.Error("the clear is not conditioned on the node being a reflection or an " +
			"interjection, so it either fires for every failure or for none")
	}
	if !strings.Contains(branch, "workSinceReflection = 0") {
		t.Error("the work counter is not reset with the flag, so the next reflection " +
			"is scheduled against a count that includes work the failed one covered")
	}
}

// Credentials the model rejects stop the run.
//
// A rejected key is a configuration problem, not a transient one. Without this
// every remaining node is retried against the same key, the whole budget is
// spent, and the answer says nothing about why.
func TestRejectedCredentialsStopTheRun(t *testing.T) {
	branch := failureBranch(t)
	if !strings.Contains(branch, "llm.IsAuthFailure(errMsg)") {
		t.Fatal("a failure is not checked for rejected credentials, so a bad key is " +
			"retried node by node until the budget is gone")
	}
	if !strings.Contains(branch, "graph.SkipAllPending()") {
		t.Error("pending work is not skipped, so the run continues against credentials " +
			"already known to be refused")
	}
	if !strings.Contains(branch, "reflectionVerdict =") {
		t.Error("no verdict is set, so the run ends with nothing said about why")
	}
}

// The verdict names the cause. Whoever reads it has to know it is the key and
// not the question.
func TestTheCredentialsVerdictSaysWhatIsWrong(t *testing.T) {
	branch := failureBranch(t)
	start := strings.Index(branch, "reflectionVerdict = \"")
	if start < 0 {
		t.Skip("no verdict to read; TestRejectedCredentialsStopTheRun covers that")
	}
	rest := branch[start+len("reflectionVerdict = \""):]
	verdict := rest[:strings.Index(rest, "\"")]

	for _, word := range []string{"credential", "key"} {
		if !strings.Contains(strings.ToLower(verdict), word) {
			t.Errorf("the verdict does not mention %q: %q", word, verdict)
		}
	}
}

// The detector this depends on. Listed here rather than left to the llm
// package, because the scheduler's behaviour is only as good as what it matches.
func TestTheAuthFailureDetectorMatchesWhatProvidersSay(t *testing.T) {
	for _, msg := range []string{
		"HTTP 401 Unauthorized",
		"http 403 forbidden",
		"invalid api key provided",
		"insufficient_quota",
	} {
		if !llm.IsAuthFailure(msg) {
			t.Errorf("%q is not read as a credentials failure, so the run would grind "+
				"through its whole budget against it", msg)
		}
	}
	for _, msg := range []string{
		"context deadline exceeded",
		"connection refused",
		"tool returned no rows",
	} {
		if llm.IsAuthFailure(msg) {
			t.Errorf("%q is read as a credentials failure, so a transient error would "+
				"abandon the run", msg)
		}
	}
}
