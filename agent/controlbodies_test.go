package agent

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The three control nodes — Holmes, the micro-planner and the observer — stored
// their output as a plain string, so the trace showed the first line of a JSON
// blob ({"reasoning": "...) where every other node showed a sentence. These
// tests cover what each one now says instead, and that the scheduler still
// hands the typed body over.

func TestHolmesBodySummaryPrefersTheRootCause(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "a concluded investigation reports its root cause",
			raw:  `{"conclude":true,"hypothesis":"maybe disk","reasoning":"long prose","rca":{"root_cause":"log rotation stopped"}}`,
			want: "RCA: log rotation stopped",
		},
		{
			name: "mid-investigation reports the working theory",
			raw:  `{"conclude":false,"hypothesis":"disk filled by unrotated logs","reasoning":"long prose"}`,
			want: "hypothesis: disk filled by unrotated logs",
		},
		{
			name: "with no theory yet it reports the reasoning",
			raw:  `{"conclude":false,"reasoning":"checking the mount points first"}`,
			want: "checking the mount points first",
		},
		{
			name: "unparseable output falls back to the first line",
			raw:  "not json at all\nsecond line",
			want: "not json at all",
		},
		{
			name: "concluding with an empty root cause falls through to the theory",
			raw:  `{"conclude":true,"hypothesis":"disk","rca":{"root_cause":""}}`,
			want: "hypothesis: disk",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseHolmesBody(c.raw).Summary(); got != c.want {
				t.Errorf("Summary() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestMicroPlannerBodySummary(t *testing.T) {
	if got := parseMicroPlannerBody(`{"summary":"restart the collector","nodes":[{"tool":"x"}]}`).Summary(); got != "fix: restart the collector" {
		t.Errorf("Summary() = %q, want the diagnosis", got)
	}
	if got := parseMicroPlannerBody(`{"nodes":[{"tool":"x"},{"tool":"y"}]}`).Summary(); got != "2 fix step(s)" {
		t.Errorf("Summary() = %q, want the step count when there is no diagnosis", got)
	}
	if got := parseMicroPlannerBody("garbage").Summary(); got != "garbage" {
		t.Errorf("Summary() = %q, want the raw first line", got)
	}
}

func TestObserverBodySummary(t *testing.T) {
	if got := parseObserverBody(`{"action":"cancel","reason":"the host went offline"}`).Summary(); got != "cancel: the host went offline" {
		t.Errorf("Summary() = %q, want the decision and why", got)
	}
	if got := parseObserverBody(`{"action":"continue"}`).Summary(); got != "continue" {
		t.Errorf("Summary() = %q, want the bare decision when no reason is given", got)
	}
	if got := parseObserverBody("garbage").Summary(); got != "garbage" {
		t.Errorf("Summary() = %q, want the raw first line", got)
	}
}

// A long field is cut rather than filling the trace line with an essay.
func TestControlBodySummariesAreTruncated(t *testing.T) {
	long := strings.Repeat("a", 400)
	for name, got := range map[string]string{
		"holmes":       parseHolmesBody(`{"hypothesis":"` + long + `"}`).Summary(),
		"microplanner": parseMicroPlannerBody(`{"summary":"` + long + `"}`).Summary(),
		"observer":     parseObserverBody(`{"action":"inject","reason":"` + long + `"}`).Summary(),
	} {
		if len(got) > 200 {
			t.Errorf("%s summary is %d chars; it should be cut near 150", name, len(got))
		}
	}
}

// Everything downstream still reads the raw JSON — the scheduler re-parses it to
// graft nodes, the reflector reads the debug history off it, and the frontend
// calls JSON.parse on node.result. The typed body must hand back exactly what
// was stored, or all three break at once.
func TestControlBodiesHandBackTheRawJSONUnchanged(t *testing.T) {
	const raw = `{"action":"inject","reason":"needs more evidence","nodes":[{"tool":"read_file"}]}`
	for name, b := range map[string]NodeBody{
		"holmes":       parseHolmesBody(raw),
		"microplanner": parseMicroPlannerBody(raw),
		"observer":     parseObserverBody(raw),
	} {
		if got := b.Evidence(); got != raw {
			t.Errorf("%s Evidence() = %q, want the raw JSON", name, got)
		}
		if v, ok := b.Field("reason"); !ok || v != "needs more evidence" {
			t.Errorf(`%s Field("reason") = %v, %v`, name, v, ok)
		}
	}
}

// The parsed struct is reachable, which is the point of a typed body: a consumer
// can read a field instead of unmarshalling the string a second time.
func TestControlBodiesExposeTheParsedOutput(t *testing.T) {
	if got := parseHolmesBody(`{"conclude":true}`).Out.Conclude; !got {
		t.Error("HolmesBody.Out.Conclude did not survive the parse")
	}
	if got := parseMicroPlannerBody(`{"nodes":[{"tool":"x"}]}`).Out.Nodes; len(got) != 1 {
		t.Errorf("MicroPlannerBody.Out.Nodes = %d steps, want 1", len(got))
	}
	if got := parseObserverBody(`{"cancel":["tag-a"]}`).Out.Cancel; len(got) != 1 {
		t.Errorf("ObserverBody.Out.Cancel = %v, want one tag", got)
	}
}

// TestControlNodesStoreTheirTypedBody guards the wiring. Every test above passes
// on a scheduler that went back to SetResult, because SetResult still stores a
// body — the untyped one, whose Summary is the first line of the JSON. That is
// the exact defect these bodies were written to remove, and nothing else fails
// when it returns.
//
// Matched loosely on whitespace so gofmt realignment is not a false alarm.
func TestControlNodesStoreTheirTypedBody(t *testing.T) {
	for _, c := range []struct {
		file, want, node string
	}{
		{"scheduler.go", `SetBody\(comp\.NodeID,\s*parseHolmesBody\(`, "holmes"},
		{"scheduler.go", `SetBody\(comp\.NodeID,\s*parseMicroPlannerBody\(`, "micro-planner"},
		{"observer.go", `SetBody\(obsID,\s*parseObserverBody\(`, "observer"},
	} {
		src, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatalf("read %s: %v", c.file, err)
		}
		if !regexp.MustCompile(c.want).Match(src) {
			t.Errorf("the %s node no longer stores a typed body; its trace line is back to raw JSON", c.node)
		}
	}
}
