package agent

import (
	"os"
	"testing"

	"github.com/Compdeep/kaiju/sandbox"
)

// Tools run by these tests start their programs through toolapi.Command, which
// re-starts this test binary to lock them, so the binary has to handle that first.
func TestMain(m *testing.M) {
	sandbox.Enter()
	sandbox.Enable(sandbox.Options{Logf: func(string, ...any) {}})
	os.Exit(m.Run())
}
