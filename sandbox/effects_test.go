package sandbox

import (
	"strings"
	"testing"
)

func TestEffectsLevels(t *testing.T) {
	e := Effects{ChangeFiles: 100, Network: Never}
	cases := []struct {
		eff   Effect
		level int
		want  bool
	}{
		{ChangeFiles, 99, false},
		{ChangeFiles, 100, true},
		{Network, 200, false}, // never
		{Root, 199, false},    // not named: its default, 200
		{Root, 200, true},
		{ReadFiles, 0, true},
	}
	for _, c := range cases {
		if got := e.Allows(c.eff, c.level); got != c.want {
			t.Errorf("%s at %d: want %v", c.eff, c.level, c.want)
		}
	}
}

func TestDescribe(t *testing.T) {
	if d := Describe(200); d != "" {
		t.Fatalf("everything is allowed at 200, got %q", d)
	}
	d := Describe(100)
	for _, want := range []string{"level 100", "may read any file", "use the network", "may not change files (needs 200)", "run as root (needs 200)"} {
		if !strings.Contains(d, want) {
			t.Errorf("missing %q in %q", want, d)
		}
	}
}
