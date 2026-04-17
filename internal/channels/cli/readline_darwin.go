//go:build darwin

package cli

import "golang.org/x/sys/unix"

const (
	tcGetAttr = unix.TIOCGETA
	tcSetAttr = unix.TIOCSETA
)
