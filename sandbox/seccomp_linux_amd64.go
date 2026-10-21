//go:build linux && amd64

package sandbox

import "golang.org/x/sys/unix"

var table = &syscallTable{
	arch: unix.AUDIT_ARCH_X86_64,
	x32:  true,
	always: []uintptr{
		unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV,
		unix.SYS_PIDFD_SEND_SIGNAL, unix.SYS_PIDFD_GETFD, unix.SYS_TKILL,
		unix.SYS_CHMOD, unix.SYS_FCHMOD, unix.SYS_FCHMODAT, unix.SYS_FCHMODAT2,
		unix.SYS_CHOWN, unix.SYS_FCHOWN, unix.SYS_LCHOWN, unix.SYS_FCHOWNAT,
		unix.SYS_SETXATTR, unix.SYS_LSETXATTR, unix.SYS_FSETXATTR,
		unix.SYS_REMOVEXATTR, unix.SYS_LREMOVEXATTR, unix.SYS_FREMOVEXATTR,
		unix.SYS_UTIME, unix.SYS_UTIMES, unix.SYS_UTIMENSAT, unix.SYS_FUTIMESAT,
		unix.SYS_TRUNCATE,
		unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER,
		unix.SYS_OPEN_BY_HANDLE_AT,
		unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT, unix.SYS_CHROOT,
		unix.SYS_MOUNT_SETATTR, unix.SYS_MOVE_MOUNT, unix.SYS_OPEN_TREE,
		unix.SYS_FSOPEN, unix.SYS_FSCONFIG, unix.SYS_FSMOUNT, unix.SYS_FSPICK,
		unix.SYS_SWAPON, unix.SYS_SWAPOFF, unix.SYS_REBOOT,
		unix.SYS_INIT_MODULE, unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE,
		unix.SYS_KEXEC_LOAD, unix.SYS_KEXEC_FILE_LOAD,
		unix.SYS_PROCESS_MADVISE,
	},
	syslog: unix.SYS_SYSLOG,
	selfOnly: []pidCall{
		{unix.SYS_PRLIMIT64, 0},
		{unix.SYS_SCHED_SETAFFINITY, 0}, {unix.SYS_SCHED_SETSCHEDULER, 0},
		{unix.SYS_SCHED_SETPARAM, 0}, {unix.SYS_SCHED_SETATTR, 0},
		{unix.SYS_MIGRATE_PAGES, 0}, {unix.SYS_MOVE_PAGES, 0},
	},
	whoCalls: []whoCall{
		{unix.SYS_SETPRIORITY, 0, 1, unix.PRIO_USER},
		{unix.SYS_IOPRIO_SET, 0, 1, ioprioWhoUser},
	},
	signals: []uintptr{unix.SYS_KILL, unix.SYS_TGKILL, unix.SYS_RT_SIGQUEUEINFO, unix.SYS_RT_TGSIGQUEUEINFO},
	fileWrites: []uintptr{
		unix.SYS_CREAT, unix.SYS_UNLINK, unix.SYS_UNLINKAT,
		unix.SYS_RENAME, unix.SYS_RENAMEAT, unix.SYS_RENAMEAT2,
		unix.SYS_MKDIR, unix.SYS_MKDIRAT, unix.SYS_RMDIR,
		unix.SYS_LINK, unix.SYS_LINKAT, unix.SYS_SYMLINK, unix.SYS_SYMLINKAT,
		unix.SYS_MKNOD, unix.SYS_MKNODAT, unix.SYS_FTRUNCATE, unix.SYS_FALLOCATE,
	},
	opens:   []openCall{{unix.SYS_OPEN, 1}, {unix.SYS_OPENAT, 2}},
	openat2: unix.SYS_OPENAT2,
}
