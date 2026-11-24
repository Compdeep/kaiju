//go:build linux

package toolapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// createNoFollow opens name inside dir, which realDir checked against real
// (the writable folders, resolved): the folder is
// opened and checked again by its descriptor, and the file is made in that
// folder without following a symlink at its name, so nothing can be put in
// place of either between the check and the write.
func createNoFollow(real []string, dir, name string, flag int, perm os.FileMode) (*os.File, error) {
	dfd, err := unix.Open(dir, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	defer unix.Close(dfd)
	where, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(dfd))
	if err != nil {
		return nil, fmt.Errorf("where %s is: %w", dir, err)
	}
	if !insideAny(real, where) {
		return nil, outside(dir, where)
	}
	fd, err := unix.Openat(dfd, name, flag|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm.Perm()))
	if err == unix.ELOOP {
		return nil, &Refusal{Why: fmt.Sprintf("%s is a symlink; a tool does not write through one below level %d",
			filepath.Join(dir, name), ImpactControl)}
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(dir, name), Err: err}
	}
	return os.NewFile(uintptr(fd), filepath.Join(where, name)), nil
}
