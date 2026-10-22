package agent

import "testing"

// The ceiling caps the clearance on every read, whatever it is set to, and
// follows the ceiling when it changes.
func TestTheClearanceNeverPassesTheCeiling(t *testing.T) {
	ceiling := 100
	c := &localClearance{value: 200, ceiling: func() int { return ceiling }}
	if got := c.Clearance(); got != 100 {
		t.Fatalf("want 100, got %d", got)
	}
	c.Set(50)
	if got := c.Clearance(); got != 50 {
		t.Fatalf("a value under the ceiling: want 50, got %d", got)
	}
	c.Set(300)
	ceiling = 200
	if got := c.Clearance(); got != 200 {
		t.Fatalf("after the ceiling rose: want 200, got %d", got)
	}
	if got := (&localClearance{value: 150}).Clearance(); got != 150 {
		t.Fatalf("no ceiling: want 150, got %d", got)
	}
}
