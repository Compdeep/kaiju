package agent

import (
	"strings"
	"testing"
)

// The reflector's own move survives. It concluded that from evidence this
// function cannot see, and a run whose remaining work is a real fetch should
// still do it — the bias says what ELSE to plan, not what to plan instead.
func TestBiasKeepsTheReflectorsOwnNextMove(t *testing.T) {
	got := biasNextToDiagnosis("read /var/log/auth.log for the session that ran cron", "")
	if !strings.Contains(got, "read /var/log/auth.log") {
		t.Errorf("the reflector's next move was dropped:\n%s", got)
	}
	if !strings.Contains(got, "`debug` step") {
		t.Errorf("the diagnosis instruction is missing:\n%s", got)
	}
}

// An instruction with no subject reads to the planner as a step about nothing,
// so an empty next falls back to what the reflector said it was looking at.
func TestBiasFallsBackToTheSummaryWhenThereIsNoNextMove(t *testing.T) {
	got := biasNextToDiagnosis("", "cron read /etc/shadow and no cron job accounts for it")
	if !strings.Contains(got, "cron read /etc/shadow") {
		t.Errorf("the summary was not used as the subject:\n%s", got)
	}
	if !strings.Contains(got, "`debug` step") {
		t.Errorf("the diagnosis instruction is missing:\n%s", got)
	}
}

// Neither field carries anything: the instruction alone is still better than an
// empty move, and the executive is told what shape to plan.
func TestBiasWithNothingToWorkFromIsStillAnInstruction(t *testing.T) {
	got := biasNextToDiagnosis("  ", "\t")
	if got != diagnosisBias {
		t.Errorf("got %q, want the bare instruction", got)
	}
}

// Applied twice it must not stack. The scheduler guards this with a flag, but a
// helper that quietly doubles its own text on a second call is a trap for the
// next caller rather than a property of this one.
func TestBiasDoesNotStack(t *testing.T) {
	once := biasNextToDiagnosis("check the PAM stack", "")
	twice := biasNextToDiagnosis(once, "")
	if once != twice {
		t.Errorf("applying the bias twice changed it:\n once: %s\ntwice: %s", once, twice)
	}
	if n := strings.Count(twice, "`debug` step"); n != 1 {
		t.Errorf("the instruction appears %d times, want 1", n)
	}
}

// The instruction has one job: stop the run reaching for more of the same. If
// it stops saying so, a stuck replan goes back to gathering.
func TestBiasSaysNotToGatherMoreEvidence(t *testing.T) {
	for _, want := range []string{"Do not gather more of the same evidence", "problem"} {
		if !strings.Contains(diagnosisBias, want) {
			t.Errorf("the instruction no longer says %q:\n%s", want, diagnosisBias)
		}
	}
}
