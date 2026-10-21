//go:build linux && (amd64 || arm64)

package sandbox

import (
	"encoding/binary"
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

func TestTheFilterDecidesEachRule(t *testing.T) {
	const self, other = 4000, 4001
	for _, fileWrites := range []bool{false, true} {
		prog, err := buildFilter(table, self, fileWrites)
		if err != nil {
			t.Fatal(err)
		}
		a := table.arch
		neg := func(v int) uint64 { return uint64(uint32(-int32(v))) }
		cases := []struct {
			name string
			nr   uintptr
			args []uint64
			want uint32
		}{
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
			{"ptrace", unix.SYS_PTRACE, nil, eperm},
			{"chmod", unix.SYS_FCHMODAT, nil, eperm},
		}
		if fileWrites {
			cases = append(cases,
				struct {
					name string
					nr   uintptr
					args []uint64
					want uint32
				}{"open to read", unix.SYS_OPENAT, []uint64{0, 0, unix.O_RDONLY}, allow},
				struct {
					name string
					nr   uintptr
					args []uint64
					want uint32
				}{"open to write", unix.SYS_OPENAT, []uint64{0, 0, unix.O_WRONLY}, eperm},
				struct {
					name string
					nr   uintptr
					args []uint64
					want uint32
				}{"openat2", unix.SYS_OPENAT2, nil, enosys},
				struct {
					name string
					nr   uintptr
					args []uint64
					want uint32
				}{"unlink", unix.SYS_UNLINKAT, nil, eperm},
			)
		}
		for _, c := range cases {
			if got := runFilter(t, prog, a, c.nr, c.args...); got != c.want {
				t.Errorf("fileWrites=%v %s: got %#x, want %#x", fileWrites, c.name, got, c.want)
			}
		}
		// Another processor's calls, whose numbers mean something else, are
		// all refused.
		if got := runFilter(t, prog, unix.AUDIT_ARCH_I386, unix.SYS_READ); got != eperm {
			t.Errorf("a 32-bit call was not refused: %#x", got)
		}
	}
}
