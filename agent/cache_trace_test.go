package agent

import (
	"strings"
	"testing"
)

// Each call's log says how much of its prompt the provider served from its
// cache, so whether caching works can be read off any run.
func TestACallsLogSaysHowMuchWasCached(t *testing.T) {
	for _, c := range []struct {
		in, cached int
		want       string
	}{
		{1000, 250, "tokens_cached: 250 (25%)\n"},
		{1000, 0, "tokens_cached: 0 (0%)\n"},
	} {
		meta := LLMTrace{TokensIn: c.in, TokensOut: 10, TokensCached: c.cached}.formatMeta()
		if !strings.Contains(meta, c.want) {
			t.Errorf("in=%d cached=%d: want %q in\n%s", c.in, c.cached, c.want, meta)
		}
	}
}
