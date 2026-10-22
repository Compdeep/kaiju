//go:build linux && (amd64 || arm64)

package sandbox

import (
	"encoding/binary"
	"fmt"
	"testing"

	"golang.org/x/sys/unix"
)

// run evaluates the filter on one system call, as the kernel would, and
// returns its verdict. Only the instructions buildFilter emits are handled.
func runFilter(t *testing.T, prog []unix.SockFilter, arch uint32, nr uintptr, args ...uint64) uint32 {
	t.Helper()
	var data [64]byte
	binary.LittleEndian.PutUint32(data[0:], uint32(nr))
	binary.LittleEndian.PutUint32(data[4:], arch)
	for i, a := range args {
		binary.LittleEndian.PutUint64(data[16+8*i:], a)
	}
	var acc uint32
	for pc := 0; pc < len(prog); pc++ {
		in := prog[pc]
		switch in.Code {
		case ldW:
			acc = binary.LittleEndian.Uint32(data[in.K:])
		case jeq:
			if acc == in.K {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case jge:
			if acc >= in.K {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case jset:
			if acc&in.K != 0 {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case ret:
			return in.K
		default:
			t.Fatalf("instruction %#x at %d is not one the filter uses", in.Code, pc)
		}
	}
	t.Fatal("the filter ran off its end")
	return 0
}

const (
	allow  = unix.SECCOMP_RET_ALLOW
	eperm  = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	enosys = unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)
)

type call struct {
	name string
	nr   uintptr
	args []uint64
	want uint32
}

func check(t *testing.T, label string, prog []unix.SockFilter, cases []call) {
	t.Helper()
	for _, c := range cases {
		if got := runFilter(t, prog, table.arch, c.nr, c.args...); got != c.want {
			t.Errorf("%s %s: got %#x, want %#x", label, c.name, got, c.want)
		}
	}
}

func build(t *testing.T, allow map[Effect]bool, fileWrites bool) []unix.SockFilter {
	t.Helper()
	prog, err := buildFilter(table, 4000, allow, fileWrites)
	if err != nil {
		t.Fatal(err)
	}
	return prog
}

// readOnlyAllow is what a step at 100 gets from the default table, with
// SignalPrograms refused so its rules show.
func readOnlyAllow() map[Effect]bool {
	a := DefaultEffects().allowedAt(100)
	a[SignalPrograms] = false
	return a
}

func TestTheFilterDecidesEachRule(t *testing.T) {
	const self, other = 4000, 4001
	neg := func(v int) uint64 { return uint64(uint32(-int32(v))) }
	for _, fileWrites := range []bool{false, true} {
		prog := build(t, readOnlyAllow(), fileWrites)
		cases := []call{
			{"read", unix.SYS_READ, nil, allow},
			// syslog: only the actions that read
			{"syslog read all", unix.SYS_SYSLOG, []uint64{3}, allow},
			{"syslog read", unix.SYS_SYSLOG, []uint64{2}, allow},
			{"syslog size", unix.SYS_SYSLOG, []uint64{10}, allow},
			{"syslog read and clear", unix.SYS_SYSLOG, []uint64{4}, eperm},
			{"syslog clear (dmesg -C)", unix.SYS_SYSLOG, []uint64{5}, eperm},
			{"syslog console off", unix.SYS_SYSLOG, []uint64{6}, eperm},
			{"syslog console level", unix.SYS_SYSLOG, []uint64{8}, eperm},
			// limits, scheduling, placement: only the command itself
			{"prlimit itself (0)", unix.SYS_PRLIMIT64, []uint64{0}, allow},
			{"prlimit itself (pid)", unix.SYS_PRLIMIT64, []uint64{self}, allow},
			{"prlimit another", unix.SYS_PRLIMIT64, []uint64{other}, eperm},
			{"affinity another", unix.SYS_SCHED_SETAFFINITY, []uint64{other}, eperm},
			{"scheduler another", unix.SYS_SCHED_SETSCHEDULER, []uint64{other}, eperm},
			{"sched attr itself", unix.SYS_SCHED_SETATTR, []uint64{0}, allow},
			{"migrate another", unix.SYS_MIGRATE_PAGES, []uint64{other}, eperm},
			{"priority itself", unix.SYS_SETPRIORITY, []uint64{unix.PRIO_PROCESS, 0}, allow},
			{"priority another", unix.SYS_SETPRIORITY, []uint64{unix.PRIO_PROCESS, other}, eperm},
			{"priority of a whole user", unix.SYS_SETPRIORITY, []uint64{unix.PRIO_USER, 0}, eperm},
			{"io priority of a whole user", unix.SYS_IOPRIO_SET, []uint64{ioprioWhoUser, 0}, eperm},
			{"io priority itself", unix.SYS_IOPRIO_SET, []uint64{1, 0}, allow},
			{"process_madvise", unix.SYS_PROCESS_MADVISE, []uint64{0}, eperm},
			// signals: its own group only
			{"kill own group (0)", unix.SYS_KILL, []uint64{0}, allow},
			{"kill itself", unix.SYS_KILL, []uint64{self}, allow},
			{"kill own group (-pid)", unix.SYS_KILL, []uint64{neg(self)}, allow},
			{"kill another", unix.SYS_KILL, []uint64{other}, eperm},
			{"kill every process", unix.SYS_KILL, []uint64{0xffffffff}, eperm},
			{"tkill", unix.SYS_TKILL, []uint64{self}, eperm},
			{"pidfd_send_signal", unix.SYS_PIDFD_SEND_SIGNAL, nil, eperm},
			// programs: read, stop and go on; never change
			{"ptrace attach", unix.SYS_PTRACE, []uint64{unix.PTRACE_ATTACH}, allow},
			{"ptrace seize", unix.SYS_PTRACE, []uint64{unix.PTRACE_SEIZE, other, 0, 0}, allow},
			{"ptrace peek", unix.SYS_PTRACE, []uint64{unix.PTRACE_PEEKDATA}, allow},
			{"ptrace syscall", unix.SYS_PTRACE, []uint64{unix.PTRACE_SYSCALL}, allow},
			{"ptrace get regset", unix.SYS_PTRACE, []uint64{unix.PTRACE_GETREGSET}, allow},
			{"ptrace get syscall info", unix.SYS_PTRACE, []uint64{0x420e}, allow},
			{"ptrace options", unix.SYS_PTRACE, []uint64{unix.PTRACE_SETOPTIONS, other, 0, unix.PTRACE_O_TRACESYSGOOD}, allow},
			{"ptrace options exitkill", unix.SYS_PTRACE, []uint64{unix.PTRACE_SETOPTIONS, other, 0, unix.PTRACE_O_EXITKILL}, eperm},
			{"ptrace seize exitkill", unix.SYS_PTRACE, []uint64{unix.PTRACE_SEIZE, other, 0, unix.PTRACE_O_EXITKILL | unix.PTRACE_O_TRACESYSGOOD}, eperm},
			{"ptrace poke text", unix.SYS_PTRACE, []uint64{unix.PTRACE_POKETEXT}, eperm},
			{"ptrace poke data", unix.SYS_PTRACE, []uint64{unix.PTRACE_POKEDATA}, eperm},
			{"ptrace poke user", unix.SYS_PTRACE, []uint64{unix.PTRACE_POKEUSR}, eperm},
			{"ptrace set regset", unix.SYS_PTRACE, []uint64{unix.PTRACE_SETREGSET}, eperm},
			{"ptrace set siginfo", unix.SYS_PTRACE, []uint64{unix.PTRACE_SETSIGINFO}, eperm},
			{"ptrace kill", unix.SYS_PTRACE, []uint64{unix.PTRACE_KILL}, eperm},
			{"ptrace unknown", unix.SYS_PTRACE, []uint64{0x4fff}, eperm},
			{"process_vm_readv", unix.SYS_PROCESS_VM_READV, nil, allow},
			{"process_vm_writev", unix.SYS_PROCESS_VM_WRITEV, nil, eperm},
			// the network
			{"socket inet", unix.SYS_SOCKET, []uint64{unix.AF_INET}, allow},
			// files
			{"chmod", unix.SYS_FCHMODAT, nil, eperm},
			{"mount", unix.SYS_MOUNT, nil, eperm},
		}
		if fileWrites {
			cases = append(cases,
				call{"open to read", unix.SYS_OPENAT, []uint64{0, 0, unix.O_RDONLY}, allow},
				call{"open to write", unix.SYS_OPENAT, []uint64{0, 0, unix.O_WRONLY}, eperm},
				call{"openat2", unix.SYS_OPENAT2, nil, enosys},
				call{"unlink", unix.SYS_UNLINKAT, nil, eperm},
			)
		} else {
			cases = append(cases,
				call{"open to write (Landlock holds it)", unix.SYS_OPENAT, []uint64{0, 0, unix.O_WRONLY}, allow},
			)
		}
		check(t, fmt.Sprintf("fileWrites=%v", fileWrites), prog, cases)
		// Another processor's calls, whose numbers mean something else, are
		// all refused.
		if got := runFilter(t, prog, unix.AUDIT_ARCH_I386, unix.SYS_READ); got != eperm {
			t.Errorf("a 32-bit call was not refused: %#x", got)
		}
	}
}

// Each effect moves its own calls between allowed and refused.
func TestTheFilterFollowsTheEffects(t *testing.T) {
	const other = 4001
	a := readOnlyAllow()
	a[ReadPrograms], a[Network], a[ReadKernelLog] = false, false, false
	check(t, "refused", build(t, a, true), []call{
		{"ptrace attach", unix.SYS_PTRACE, []uint64{unix.PTRACE_ATTACH}, eperm},
		{"process_vm_readv", unix.SYS_PROCESS_VM_READV, nil, eperm},
		{"socket inet", unix.SYS_SOCKET, []uint64{unix.AF_INET}, eperm},
		{"socket inet6", unix.SYS_SOCKET, []uint64{unix.AF_INET6}, eperm},
		{"socket packet", unix.SYS_SOCKET, []uint64{unix.AF_PACKET}, eperm},
		{"socket unix", unix.SYS_SOCKET, []uint64{unix.AF_UNIX}, allow},
		{"socket netlink", unix.SYS_SOCKET, []uint64{unix.AF_NETLINK}, allow},
		{"syslog read", unix.SYS_SYSLOG, []uint64{3}, eperm},
	})

	a = readOnlyAllow()
	a[SignalPrograms], a[ChangeFiles] = true, true
	check(t, "allowed", build(t, a, false), []call{
		{"kill another", unix.SYS_KILL, []uint64{other}, allow},
		{"tkill", unix.SYS_TKILL, nil, allow},
		{"prlimit another", unix.SYS_PRLIMIT64, []uint64{other}, allow},
		{"chmod", unix.SYS_FCHMODAT, nil, allow},
		{"truncate", unix.SYS_TRUNCATE, nil, allow},
		{"open to write", unix.SYS_OPENAT, []uint64{0, 0, unix.O_WRONLY}, allow},
		// still refused whatever the effects, below root
		{"mount", unix.SYS_MOUNT, nil, eperm},
		{"io_uring", unix.SYS_IO_URING_SETUP, nil, eperm},
		{"process_vm_writev", unix.SYS_PROCESS_VM_WRITEV, nil, eperm},
	})
}

// Every combination of effects builds: no jump in the filter is out of reach.
func TestEveryCombinationBuilds(t *testing.T) {
	for mask := 0; mask < 1<<len(lockEffects); mask++ {
		a := map[Effect]bool{}
		for i, e := range lockEffects {
			a[e] = mask&(1<<i) != 0
		}
		for _, fw := range []bool{false, true} {
			if _, err := buildFilter(table, 4000, a, fw); err != nil {
				t.Fatalf("%v fileWrites=%v: %v", a, fw, err)
			}
		}
	}
}
