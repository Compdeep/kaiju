//go:build linux

package cli

import "golang.org/x/sys/unix"

const (
	tcGetAttr = unix.TCGETS
	tcSetAttr = unix.TCSETS
)
