package agent

import (
	"strings"
	"testing"
)

// The concurrency and submit accessors are one-line delegations to the kernel.
// What is worth pinning is that they exist on Agent at all: a host with a
// dashboard adjusts concurrency while the process runs, and if these are
// removed as "redundant with Kernel()" that host breaks with no other signal.
func TestAgentExposesTheKernelControls(t *testing.T) {
	src := readSource(t, "agent.go")
	for _, sig := range []string{
		"func (a *Agent) InFlight() bool",
		"func (a *Agent) SetConcurrency(n int)",
		"func (a *Agent) Concurrency() int",
		"func (a *Agent) SubmitSync(ctx context.Context, t Trigger) (*SyncResult, error)",
	} {
		if !strings.Contains(src, sig) {
			t.Errorf("missing %s — a host adjusting concurrency live has no way to do it", sig)
		}
	}
}
