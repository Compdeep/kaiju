//go:build linux && !amd64 && !arm64

package sandbox

// No system call table for this processor, so no lock: a locked command is
// refused (or runs unlocked under Options.AllowUnlocked).
var table *syscallTable
