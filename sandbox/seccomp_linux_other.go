//go:build linux && !amd64 && !arm64

package sandbox

// No system call table for this processor, so no lock: a command runs with
// every effect its account has, and Report lists every effect as unenforced.
var table *syscallTable
