//go:build !linux

package toolapi

import (
	"fmt"
	"os"
	"path/filepath"
)

// createNoFollow opens name inside dir, which realDir checked, refusing a name
// that is already something other than a file (a symlink or a junction).
func createNoFollow(_ []string, dir, name string, flag int, perm os.FileMode) (*os.File, error) {
	path := filepath.Join(dir, name)
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return nil, &Refusal{Why: fmt.Sprintf("%s is not a plain file; a tool does not write through it below level %d",
			path, ImpactControl)}
	}
	return os.OpenFile(path, flag, perm)
}
