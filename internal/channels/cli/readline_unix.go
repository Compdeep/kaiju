//go:build linux || darwin

package cli

import "golang.org/x/sys/unix"

// enableOPOST puts output post-processing back after term.MakeRaw cleared it,
// so a bare \n still moves the cursor to column zero while we own raw mode.
func enableOPOST(fd int) error {
	t, err := unix.IoctlGetTermios(fd, tcGetAttr)
	if err != nil {
		return err
	}
	t.Oflag |= unix.OPOST | unix.ONLCR
	return unix.IoctlSetTermios(fd, tcSetAttr, t)
}
