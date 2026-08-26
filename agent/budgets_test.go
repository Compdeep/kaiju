package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

func agentWithWindow(tokens int) *Agent {
	a := &Agent{}
	a.cfg.LLMModel = "m"
	a.cfg.ExecutorModel = "m"
	a.cfg.Limits = func(string) (int, int) { return tokens, 0 }
	return a
}

// The whole table, at every window a deployment might run on.
//
// This is the artefact. Four caps that were four constants in four files, set
// at different times, unaware of each other — a string tool cut at 4096 while a
// typed tool doing the same work was cut at 8000, and a page fetched whole cut
// to 8000 before the gate that decides what a prompt holds had seen it.
//
// Run with -v to read it. A change to one number is visibly a change relative
// to the rest, which is the point of having them in one place.
func TestBudgets_TheTable(t *testing.T) {
	windows := []int{8192, 32768, 131072, 262144, 1048576}
	specs := []struct {
		name string
		spec budgetSpec
	}{
		{"tool result", toolResultBudget},
		{"evidence", evidenceBudget},
		{"payload", payloadBudget},
		{"tool index", toolIndexBudget},
	}

	var sb strings.Builder
	sb.WriteString("\n                  ")
	for _, w := range windows {
		sb.WriteString(pad(itoa(w/1024)+"K", 10))
	}
	sb.WriteString("\n")
	for _, s := range specs {
		sb.WriteString(pad(s.name, 18))
		for _, w := range windows {
			sb.WriteString(pad(commas(agentWithWindow(w).budget(s.spec)), 10))
		}
		sb.WriteString("  " + s.spec.Bounds + "\n")
	}
	t.Log(sb.String())

	// The order has to hold at every window. A cap on ONE step's result must
	// never exceed the cap on what a whole prompt carries, or the smaller one
	// stops meaning anything.
	for _, w := range windows {
		a := agentWithWindow(w)
		if a.budget(toolResultBudget) > a.budget(evidenceBudget) {
			t.Errorf("at %dK the dispatch cap (%d) exceeds the evidence cap (%d) — "+
				"a result would be cut to fit a prompt it is then cut again for",
				w/1024, a.budget(toolResultBudget), a.budget(evidenceBudget))
		}
		if a.budget(evidenceBudget) > a.budget(toolIndexBudget) {
			t.Errorf("at %dK one step's result (%d) is allowed more than the whole tool "+
				"index (%d)", w/1024, a.budget(evidenceBudget), a.budget(toolIndexBudget))
		}
	}
}

// Base is the floor and the answer for a deployment with no catalog. Most
// supply none, and every one of these numbers must be unchanged for them.
func TestBudgets_InertWithoutACatalog(t *testing.T) {
	for _, s := range []budgetSpec{toolResultBudget, evidenceBudget, payloadBudget, toolIndexBudget} {
		if got := (&Agent{}).budget(s); got != s.Base {
			t.Errorf("%s: no catalog gave %d, want the base %d", s.Bounds, got, s.Base)
		}
		if got := (*Agent)(nil).budget(s); got != s.Base {
			t.Errorf("%s: a nil agent gave %d", s.Bounds, got)
		}
	}
}

// A small window gets the base, not a share of almost nothing. A very large one
// stops at the ceiling — a model reading more does not answer better past a
// point, and one step filling a prompt is worse than several keeping the part
// that mattered.
func TestBudgets_FloorAndCeilingBothHold(t *testing.T) {
	for _, s := range []budgetSpec{toolResultBudget, evidenceBudget, payloadBudget, toolIndexBudget} {
		if got := agentWithWindow(4096).budget(s); got != s.Base {
			t.Errorf("%s: a 4K window gave %d, want the base %d", s.Bounds, got, s.Base)
		}
		if got := agentWithWindow(4 << 20).budget(s); got != s.Ceiling {
			t.Errorf("%s: a huge window gave %d, want the ceiling %d", s.Bounds, got, s.Ceiling)
		}
		if s.Ceiling <= s.Base {
			t.Errorf("%s: ceiling %d is not above base %d, so it can never grow",
				s.Bounds, s.Ceiling, s.Base)
		}
	}
}

// The evidence cut keeps both ends and says which cap made it — a reader of a
// prompt has to tell this cap from the four others, and its size is no longer
// the same on every deployment.
func TestTruncateEvidenceTo_KeepsBothEndsAndNamesTheCap(t *testing.T) {
	body := strings.Repeat("a", 5000) + "MIDDLE" + strings.Repeat("z", 5000)

	got := Text.TruncateEvidenceTo(body, 3000)

	if len(got) > 3000+120 {
		t.Errorf("cut to %d chars for a budget of 3000", len(got))
	}
	if !strings.HasPrefix(got, "aaa") || !strings.HasSuffix(got, "zzz") {
		t.Error("both ends must survive; a result is often JSON and a head-only cut breaks it")
	}
	if strings.Contains(got, "MIDDLE") {
		t.Error("the middle was supposed to be the part removed")
	}
	if !strings.Contains(got, "3000-char evidence cap") {
		t.Error("the marker does not name the cap that cut")
	}
	if short := Text.TruncateEvidenceTo("brief", 3000); short != "brief" {
		t.Errorf("something inside the budget was cut anyway: %q", short)
	}
	if len(Text.TruncateEvidenceTo(strings.Repeat("x", toolapi.EvidenceBudget*2), 0)) > toolapi.EvidenceBudget+120 {
		t.Error("a zero budget must mean the compiled default, not no budget")
	}
}

func pad(s string, n int) string {
	for len(s) < n {
		s += " "
	}
	return s
}

func itoa(n int) string { return commas(n) }

func commas(n int) string {
	s := ""
	for n >= 1000 {
		s = "," + pad3(n%1000) + s
		n /= 1000
	}
	return itoaPlain(n) + s
}

func pad3(n int) string {
	s := itoaPlain(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

func itoaPlain(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
