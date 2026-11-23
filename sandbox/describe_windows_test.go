//go:build windows

package sandbox

import (
	"strings"
	"testing"
)

// On Windows a step below control runs at low integrity, which sees and
// signals no program above it. The sentence a refused command carries says
// so, instead of what the table alone allows.
func TestDescribeOnWindowsSaysWhatLowIntegrityRefuses(t *testing.T) {
	d := Describe(100)
	if strings.Contains(d, "may read any file") == false {
		t.Errorf("reading files is no longer said: %q", d)
	}
	if !strings.Contains(d, "may not") || !strings.Contains(d, "see system and other accounts' programs") {
		t.Errorf("not said that it cannot see system programs: %q", d)
	}
	if strings.Contains(d, "may trace") || strings.Contains(d, "its programs may read any file, see") {
		t.Errorf("says it may see or trace programs: %q", d)
	}
}
