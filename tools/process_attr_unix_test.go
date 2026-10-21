//go:build !windows

package tools

import "syscall"

func markSysProcAttr(a *syscall.SysProcAttr)        { a.Noctty = true }
func sysProcAttrMarked(a *syscall.SysProcAttr) bool { return a.Noctty }
