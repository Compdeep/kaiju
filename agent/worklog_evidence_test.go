package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The real listing from the run that broke, rebuilt to the same shape and size.
func realListing() string {
	type entry struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Size int    `json:"size"`
	}
	names := []string{
		"01-s1.html", "02-sobj.html", "03-s2.html", "04-srew.html", "05-sval.html",
		"06-sentry.html", "07-sjudge.html", "08-s6.html", "09-s7.html", "10-s8.html",
		"11-s9.html", "12-s4.html", "13-s10.html", "14-s11.html",
	}
	var es []entry
	for i, n := range names {
		es = append(es, entry{n, "file", 5000 + i})
	}
	b, _ := json.Marshal(map[string]any{"entries": es})
	return "file_list: " + string(b)
}

// A listing keeps its COUNT and says how many are unshown.
//
// At 100 characters the same input survived as two filenames and a third cut
// mid-word. The planner held the pattern NN-slug.html, two real examples and the
// slugs from the conversation, so it continued the numbering and invented nine
// paths. The count and the "and N more" are what stop that.
func TestWorklogEvidenceKeepsAListsCount(t *testing.T) {
	in := realListing()
	got := worklogEvidence(in)
	t.Logf("in  (%d chars): %.90s…", len(in), in)
	t.Logf("out (%d chars): %s", len(got), got)

	if !strings.HasPrefix(got, "14 entries") {
		t.Errorf("the total is not stated first: %q", got)
	}
	if !strings.Contains(got, "and 8 more") {
		t.Errorf("the unshown entries are not counted, so a reader cannot tell this is partial: %q", got)
	}
	if !strings.Contains(got, "01-s1.html") || !strings.Contains(got, "06-sentry.html") {
		t.Errorf("the shown names are missing: %q", got)
	}
	// It must be no larger than a plain cap would have been.
	if len(got) > worklogEvidenceChars {
		t.Errorf("the summary is %d chars, over the %d cap it replaces", len(got), worklogEvidenceChars)
	}
	// And it must not carry the byte-cut marker, which is the thing that was read
	// as "clipped text" rather than "entries omitted".
	if strings.Contains(got, "[cut ") {
		t.Errorf("a list summary still carries the byte-cut marker: %q", got)
	}
}

// A result that is not a list falls back to the character cap, now 300.
func TestWorklogEvidenceCapsOrdinaryResults(t *testing.T) {
	long := strings.Repeat("x", 900)
	got := worklogEvidence(long)
	if len(got) <= worklogEvidenceChars {
		t.Errorf("a 900-char result came back %d chars with no marker", len(got))
	}
	if !strings.Contains(got, "[cut 300/900]") {
		t.Errorf("the cut is not named with the whole size: %.60q", got)
	}
	short := "all of it"
	if got := worklogEvidence(short); got != short {
		t.Errorf("a short result was changed: %q", got)
	}
}

// Things that look like a list and are not.
func TestListedNamesRefusesWhatIsNotAList(t *testing.T) {
	for name, in := range map[string]string{
		"plain prose":   "read 14 files and found four errors",
		"broken json":   `file_list: {"entries":[{"name":"a.html"`,
		"empty entries": `{"entries":[]}`,
		"other shape":   `{"stdout":"a b c"}`,
		"json array":    `[{"name":"a"}]`,
	} {
		if names, total := listedNames(in); total != 0 {
			t.Errorf("%s was read as a list of %d: %v", name, total, names)
		}
	}
}

// Six names, then a count — a listing of thousands must not fill the worklog.
func TestWorklogEvidenceBoundsALongList(t *testing.T) {
	var es []map[string]string
	for i := 0; i < 500; i++ {
		es = append(es, map[string]string{"name": fmt.Sprintf("file-%03d.html", i)})
	}
	b, _ := json.Marshal(map[string]any{"entries": es})
	got := worklogEvidence("file_list: " + string(b))
	if !strings.HasPrefix(got, "500 entries") || !strings.Contains(got, "and 494 more") {
		t.Errorf("a 500-entry listing was not summarised: %q", got)
	}
	if len(got) > worklogEvidenceChars {
		t.Errorf("a 500-entry listing produced %d chars", len(got))
	}
}
