package agent

import (
	"reflect"
	"testing"
)

// The planner's tools go in one order whatever the ranking returned: the list
// sits near the start of the prompt, and a new order is a new prompt that the
// provider's cache cannot serve. The shell stays first.
func TestPlannerToolOrderIsStable(t *testing.T) {
	a := []string{shellToolName, "web_search", "compute", "web_fetch", "debug"}
	b := []string{shellToolName, "debug", "web_fetch", "compute", "web_search"}
	ga, gb := shellFirst(sortedCopy(a)), shellFirst(sortedCopy(b))
	if !reflect.DeepEqual(ga, gb) {
		t.Fatalf("one set, two orders: %v and %v", ga, gb)
	}
	if ga[0] != shellToolName {
		t.Errorf("the shell is not first: %v", ga)
	}
	if a[1] != "web_search" {
		t.Errorf("the ranker's slice was reordered: %v", a)
	}
}
