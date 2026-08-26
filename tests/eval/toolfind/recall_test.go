package toolfind_live

import (
	"os"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolfind"
)

// lexicalFloorAt40 is what words alone reach on this corpus over a thousand
// tools, measured rather than chosen: 0.42 at forty, and 0.40 as early as five.
//
// Nearly flat, and that is the point. The objectives are written the way people
// ask for work, so a tool either shares a distinctive word with the question and
// comes first, or shares nothing and sits past position two hundred. There is
// no middle, and no cut-off that helps. That is the gap the embedding half
// closes — the same corpus reaches 0.80 at forty with vectors, measured in
// live_test.go.
//
// Asserted as a floor so a change that breaks word ranking is caught in the
// free suite rather than in the paid one.
const lexicalFloorAt40 = 0.40

// TOOLFIND_RECALL=1 prints where each objective placed, which is how a
// regression is diagnosed and how the floor above was arrived at.
func TestRecall_WordsAloneOnAThousandTools(t *testing.T) {
	reg := EnterpriseRegistry(t, 1000)
	if got := len(reg.List()); got < 950 {
		t.Fatalf("registry is %d tools, wanted about 1000", got)
	}
	ix, err := toolfind.Open(t.TempDir(), reg, nil)
	if err != nil {
		t.Fatal(err)
	}

	at := []int{5, 10, 20, 40}
	got := Recall(t, ix, at, os.Getenv("TOOLFIND_RECALL") != "")
	for _, k := range at {
		t.Logf("recall@%-2d = %.2f  (%d tools, words only)", k, got[k], len(reg.List()))
	}
	if got[40] < lexicalFloorAt40 {
		t.Errorf("recall@40 fell to %.2f, floor is %.2f", got[40], lexicalFloorAt40)
	}
}
