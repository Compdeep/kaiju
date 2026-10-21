//go:build windows

package tools

import "syscall"

func markSysProcAttr(a *syscall.SysProcAttr)        { a.Token = 1 }
func sysProcAttrMarked(a *syscall.SysProcAttr) bool { return a.Token == 1 }
