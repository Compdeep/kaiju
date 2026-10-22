//go:build linux

package sandbox

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The system calls a locked command may not make, for the processor this is
// built for (seccomp_linux_amd64.go, seccomp_linux_arm64.go).
type syscallTable struct {
	// arch is the AUDIT_ARCH value a call must carry. Any other is refused, so
	// a 32-bit call cannot get round a table written for 64-bit numbers.
	arch uint32
	// x32 refuses calls with the x32 bit set (amd64 only).
	x32 bool
	// always are refused below Root whatever the effects: mounts, modules and
	// the rest no step below Root may make, and calls that would get round
	// the filter (io_uring) or out of a container (open_by_handle_at).
	always []uintptr

	// ptrace, with ReadPrograms allowed, is allowed only for the requests in
	// ptraceReads; process_vm_readv with it. Both are refused without it.
	ptrace, processVMReadv uintptr

	// socket is refused, without Network, for the internet families.
	socket uintptr

	// changes are refused without ChangeFiles: changes to a file that
	// Landlock does not cover — permissions, owner, times, extended
	// attributes — and truncating by name.
	changes []uintptr
	// fileWrites are refused without ChangeFiles when seccomp alone holds the
	// writes.
	fileWrites []uintptr
	// opens are refused, when seccomp alone holds the writes, if the flags in
	// the given argument ask to write, create or truncate.
	opens []openCall
	// openat2 takes its flags in a struct seccomp cannot read. Refused with
	// ENOSYS when seccomp alone holds the writes, so a C library falls back to
	// openat, whose flags can be read.
	openat2 uintptr

	// Without SignalPrograms:
	//
	// signals are allowed only when their first argument is the command
	// itself, its own process group, or 0 (its own group). Linux delivers a
	// signal sent to any thread's id to that thread's process, so refusing the
	// starting program's id alone would leave its other threads' ids open;
	// the command may signal nothing outside its own group instead. A program
	// the command starts can still be stopped through the group.
	signals []uintptr
	// threadSignals name their target by a thread id or a pidfd, neither of
	// which the filter can match to the group: refused.
	threadSignals []uintptr
	// selfOnly change a process's limits, priority or placement, and are
	// allowed only on the command itself: the argument naming the process must
	// be 0 or the command's id. Otherwise a command could starve the program
	// that started it, which runs as the same account.
	selfOnly []pidCall
	// whoCalls name their target with a kind and an id. The kind for every
	// process of a user (which includes the program that started the command)
	// is refused, and the id must be 0 or the command's.
	whoCalls []whoCall

	// syslog is allowed, with ReadKernelLog, only for the actions that read
	// the kernel log, so a command holding CAP_SYSLOG cannot clear it
	// (dmesg -C) or change where it goes; without ReadKernelLog it is refused.
	syslog uintptr
}

type openCall struct {
	nr  uintptr
	arg int
}

type pidCall struct {
	nr  uintptr
	arg int
}

type whoCall struct {
	nr       uintptr
	whichArg int
	whoArg   int
	userKind uint32
}

// syslogReads are the syslog(2) actions that only read: READ, READ_ALL,
// SIZE_UNREAD and SIZE_BUFFER.
var syslogReads = []uint32{2, 3, 9, 10}

// ptraceReads are the ptrace(2) requests that read a program, stop it or let
// it go on: what strace, gdb and ltrace need to follow one. Any request not
// here is refused, so those that change a program — POKETEXT, POKEDATA,
// POKEUSER, SETREGS, SETFPREGS, SETREGSET, SETSIGINFO, SETSIGMASK, KILL, the
// syscall-emulation ones — are refused, and so is any a newer kernel adds.
var ptraceReads = []uint32{
	unix.PTRACE_TRACEME, unix.PTRACE_PEEKTEXT, unix.PTRACE_PEEKDATA, unix.PTRACE_PEEKUSR,
	unix.PTRACE_CONT, unix.PTRACE_SINGLESTEP, unix.PTRACE_ATTACH, unix.PTRACE_DETACH,
	unix.PTRACE_SYSCALL,
	12, // PTRACE_GETREGS (x86)
	14, // PTRACE_GETFPREGS (x86)
	18, // PTRACE_GETFPXREGS (x86)
	25, // PTRACE_GET_THREAD_AREA (x86)
	unix.PTRACE_GETEVENTMSG, unix.PTRACE_GETSIGINFO, unix.PTRACE_GETREGSET,
	unix.PTRACE_INTERRUPT, unix.PTRACE_LISTEN, unix.PTRACE_PEEKSIGINFO,
	0x420a, // PTRACE_GETSIGMASK
	0x420c, // PTRACE_SECCOMP_GET_FILTER
	0x420d, // PTRACE_SECCOMP_GET_METADATA
	0x420e, // PTRACE_GET_SYSCALL_INFO
	0x420f, // PTRACE_GET_RSEQ_CONFIGURATION
	0x4211, // PTRACE_GET_SYSCALL_USER_DISPATCH_CONFIG
}

// ptraceOptions set a tracer's options; allowed unless the options ask for
// the traced program to be killed when the tracer exits (PTRACE_O_EXITKILL),
// which would stop any program by attaching to it and exiting.
var ptraceOptions = []uint32{unix.PTRACE_SETOPTIONS, unix.PTRACE_SEIZE}

// netFamilies are the socket families Network covers.
var netFamilies = []uint32{unix.AF_INET, unix.AF_INET6, unix.AF_PACKET}

// writeFlags are the open flags that change a file.
const writeFlags = unix.O_WRONLY | unix.O_RDWR | unix.O_CREAT | unix.O_TRUNC | unix.O_APPEND

func seccompSupported() bool { return table != nil }

// program is a seccomp filter being assembled, with jumps to named places
// resolved at the end.
type program struct {
	ins    []unix.SockFilter
	jt, jf []string
	labels map[string]int
}

func (p *program) emit(code uint16, k uint32, jt, jf string) {
	p.ins = append(p.ins, unix.SockFilter{Code: code, K: k})
	p.jt = append(p.jt, jt)
	p.jf = append(p.jf, jf)
}

func (p *program) label(name string) { p.labels[name] = len(p.ins) }

func (p *program) resolve() error {
	for i := range p.ins {
		for _, j := range []struct {
			name string
			to   *uint8
		}{{p.jt[i], &p.ins[i].Jt}, {p.jf[i], &p.ins[i].Jf}} {
			if j.name == "" {
				continue
			}
			at, ok := p.labels[j.name]
			if !ok {
				return fmt.Errorf("no place named %q", j.name)
			}
			off := at - i - 1
			if off < 0 || off > 255 {
				return fmt.Errorf("jump to %q out of range", j.name)
			}
			*j.to = uint8(off)
		}
	}
	return nil
}

const (
	ldW  = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
	jeq  = unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K
	jge  = unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K
	jset = unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K
	ret  = unix.BPF_RET | unix.BPF_K

	// Offsets in struct seccomp_data. An argument's low 32 bits come first on
	// both little-endian processors this is built for.
	offNr   = 0
	offArch = 4
)

func offArg(i int) uint32 { return uint32(16 + 8*i) }

/*
 * buildFilter assembles the filter for one locked command.
 * param: self - the command's process id, which is also its group's.
 * param: allow - the effects the command may have.
 * param: fileWrites - refuse writing files here, because Landlock does not.
 */
func buildFilter(t *syscallTable, self int, allow map[Effect]bool, fileWrites bool) ([]unix.SockFilter, error) {
	p := &program{labels: map[string]int{}}
	p.emit(ldW, offArch, "", "")
	p.emit(jeq, t.arch, "", "deny")
	p.emit(ldW, offNr, "", "")
	if t.x32 {
		p.emit(jge, 0x40000000, "deny", "")
	}
	deny := func(nrs ...uintptr) {
		for _, nr := range nrs {
			p.emit(jeq, uint32(nr), "deny", "")
		}
	}
	deny(t.always...)
	pid := uint32(self)

	if allow[ReadPrograms] {
		p.emit(jeq, uint32(t.ptrace), "", "ptrace-done")
		p.emit(ldW, offArg(0), "", "")
		for _, r := range ptraceReads {
			p.emit(jeq, r, "allow", "")
		}
		for _, r := range ptraceOptions {
			p.emit(jeq, r, "ptrace-options", "")
		}
		p.emit(ret, unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM), "", "")
		p.label("ptrace-options")
		p.emit(ldW, offArg(3), "", "")
		p.emit(jset, unix.PTRACE_O_EXITKILL, "deny", "allow")
		p.label("ptrace-done")
	} else {
		deny(t.ptrace, t.processVMReadv)
	}

	if !allow[Network] {
		p.emit(jeq, uint32(t.socket), "", "socket-done")
		p.emit(ldW, offArg(0), "", "")
		for _, f := range netFamilies {
			p.emit(jeq, f, "deny", "")
		}
		p.emit(ret, unix.SECCOMP_RET_ALLOW, "", "")
		p.label("socket-done")
	}

	if !allow[SignalPrograms] {
		deny(t.threadSignals...)
		for i, nr := range t.signals {
			next := fmt.Sprintf("signal-%d", i)
			p.emit(jeq, uint32(nr), "", next)
			p.emit(ldW, offArg(0), "", "")
			p.emit(jeq, 0, "allow", "")
			p.emit(jeq, pid, "allow", "")
			p.emit(jeq, -pid, "allow", "deny")
			p.label(next)
		}
		for i, c := range t.selfOnly {
			next := fmt.Sprintf("self-%d", i)
			p.emit(jeq, uint32(c.nr), "", next)
			p.emit(ldW, offArg(c.arg), "", "")
			p.emit(jeq, 0, "allow", "")
			p.emit(jeq, pid, "allow", "deny")
			p.label(next)
		}
		for i, c := range t.whoCalls {
			next := fmt.Sprintf("who-%d", i)
			p.emit(jeq, uint32(c.nr), "", next)
			p.emit(ldW, offArg(c.whichArg), "", "")
			p.emit(jeq, c.userKind, "deny", "")
			p.emit(ldW, offArg(c.whoArg), "", "")
			p.emit(jeq, 0, "allow", "")
			p.emit(jeq, pid, "allow", "deny")
			p.label(next)
		}
	}

	if t.syslog != 0 {
		if allow[ReadKernelLog] {
			p.emit(jeq, uint32(t.syslog), "", "syslog-done")
			p.emit(ldW, offArg(0), "", "")
			for _, a := range syslogReads {
				p.emit(jeq, a, "allow", "")
			}
			p.emit(ret, unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM), "", "")
			p.label("syslog-done")
		} else {
			deny(t.syslog)
		}
	}

	if !allow[ChangeFiles] {
		deny(t.changes...)
		if fileWrites {
			deny(t.fileWrites...)
			if t.openat2 != 0 {
				p.emit(jeq, uint32(t.openat2), "enosys", "")
			}
			for i, o := range t.opens {
				next := fmt.Sprintf("open-%d", i)
				p.emit(jeq, uint32(o.nr), "", next)
				p.emit(ldW, offArg(o.arg), "", "")
				p.emit(jset, writeFlags, "deny", "allow")
				p.label(next)
			}
		}
	}
	p.label("allow")
	p.emit(ret, unix.SECCOMP_RET_ALLOW, "", "")
	p.label("deny")
	p.emit(ret, unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM), "", "")
	p.label("enosys")
	p.emit(ret, unix.SECCOMP_RET_ERRNO|uint32(unix.ENOSYS), "", "")
	if err := p.resolve(); err != nil {
		return nil, err
	}
	return p.ins, nil
}

// applySeccomp installs the filter on this thread. no_new_privs is already set.
func applySeccomp(self int, allow map[Effect]bool, fileWrites bool) error {
	if table == nil {
		return fmt.Errorf("no system call table for this processor")
	}
	ins, err := buildFilter(table, self, allow, fileWrites)
	if err != nil {
		return err
	}
	prog := unix.SockFprog{Len: uint16(len(ins)), Filter: &ins[0]}
	return unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0)
}

// ioprioWhoUser is IOPRIO_WHO_USER, the ioprio_set(2) kind for every process of
// a user.
const ioprioWhoUser = 3
