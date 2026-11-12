package main

import (
	"testing"

	"github.com/Compdeep/kaiju/sandbox"
)

// Level 100 is operate: "write files, modify state, run code". A step at 100
// that edits a repository outside the workspace must be able to; a step at 0
// only reads; root stays at 200.
func TestKaijuEffectsLetOperateChangeFiles(t *testing.T) {
	e := kaijuEffects()
	for _, c := range []struct {
		eff   sandbox.Effect
		level int
		want  bool
	}{
		{sandbox.ChangeFiles, 0, false},
		{sandbox.ChangeFiles, 100, true},
		{sandbox.Root, 100, false},
		{sandbox.Root, 200, true},
		{sandbox.ReadPrograms, 200, false},
	} {
		if got := e.Allows(c.eff, c.level); got != c.want {
			t.Errorf("%s at %d: allowed=%v, want %v", c.eff, c.level, got, c.want)
		}
	}
}
