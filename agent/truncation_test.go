package agent

import (
	"strings"
	"testing"
)

// A truncation marker must not be readable as content.
//
// TruncateLog appended "..." — three characters that a model cannot tell apart
// from text which happens to end that way. In one run three stages read this
// function's own marker as evidence that the DATA was incomplete: a 74-byte
// process list reported as output that had been cut, and a 2,772-byte script
// diagnosed by Holmes as "truncated during generation" — Holmes having cut it
// here itself, at 1500, then read the marker back on its next iteration and
// promoted it to a high-confidence root cause. A repair of a working file
// followed.
func TestTruncateLog_SaysItCut(t *testing.T) {
	whole := strings.Repeat("x", 2772)
	got := Text.TruncateLog(whole, 1500)

	if strings.HasSuffix(got, "...") {
		t.Error(`the marker is still a bare "..." — a stage reading it cannot tell a cut from the end of a document`)
	}
	if !strings.Contains(got, "cut") {
		t.Errorf("the marker does not say a cut was made: %q", got[len(got)-40:])
	}
	// The total, so a reader can tell "this is all of it" from "this is the
	// start of it". That distinction is what every one of those mistakes turned
	// on.
	if !strings.Contains(got, "2772") {
		t.Errorf("the marker does not name the whole: %q", got[len(got)-40:])
	}
}

// Short enough to sit on a one-line trace summary. A sentence would be longer
// than the text it marks.
func TestTruncateLog_TheMarkerIsShort(t *testing.T) {
	got := Text.TruncateLog(strings.Repeat("x", 400), 200)
	if over := len(got) - 200; over > 30 {
		t.Errorf("the marker costs %d chars; a trace line cannot carry that", over)
	}
}

// Nothing is added to something that fits. A marker on a whole value would say
// the opposite of the truth.
func TestTruncateLog_UntouchedWhenItFits(t *testing.T) {
	const s = "USER         PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND"
	if got := Text.TruncateLog(s, 100); got != s {
		t.Errorf("a 74-byte value that fits in 100 was changed:\n got %q\nwant %q", got, s)
	}
}

// The case from the run, stated as itself: a complete file, read whole, must
// not come back looking like a file that ended early.
func TestTruncateLog_ACompleteFileDoesNotLookTruncated(t *testing.T) {
	script := "import json\n" + strings.Repeat("    pass\n", 20) + "\nif __name__ == '__main__':\n    main()\n"
	got := Text.TruncateLog(script, 4000)
	if strings.Contains(got, "cut") {
		t.Errorf("a file that fits was marked as cut: %q", got)
	}
	if !strings.HasSuffix(got, "main() ") && !strings.HasSuffix(got, "main()") {
		t.Errorf("the end of the file did not survive: %q", got[len(got)-30:])
	}
}
