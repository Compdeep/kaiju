//go:build linux

package sandbox

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

/*
 * ordinaryMayReadFile: what an account that owns nothing and is in no group
 * may read, which is what the level-0 account (enbarr-exe) is. The opened file
 * must let others read it (o+r; a folder also o+x to list it), and every
 * folder on its real path must let others through (o+x). The file is the one
 * opened (fstat of the fd); its path is the kernel's for that fd.
 */
func ordinaryMayReadFile(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	need := fs.FileMode(0o004)
	if fi.IsDir() {
		need |= 0o001
	}
	if fi.Mode().Perm()&need != need {
		return fmt.Errorf("its permissions (%v) do not let other accounts read it", fi.Mode().Perm())
	}
	real, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(f.Fd())))
	if err != nil {
		return fmt.Errorf("where it is: %w", err)
	}
	for dir := filepath.Dir(real); ; dir = filepath.Dir(dir) {
		di, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if di.Mode().Perm()&0o001 == 0 {
			return fmt.Errorf("%s does not let other accounts through", dir)
		}
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	return nil
}
