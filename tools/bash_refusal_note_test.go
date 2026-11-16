package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A command that failed on its own, such as ls of a file that does not exist,
// says why in its own words, and not what the step's level allows: the level
// had nothing to do with it, and the sentence read as a refusal.
func TestBash_AnOrdinaryFailureSaysWhyNotTheLevel(t *testing.T) {
	ctx := toolapi.WithLevel(context.Background(), toolapi.ImpactObserve)
	got, _ := NewBash("").ExecuteTyped(ctx, map[string]any{"command": "ls /no/such/file"})
	if strings.Contains(got.Detail, "runs at level") {
		t.Errorf("the level was named for a failure it did not cause: %s", got.Detail)
	}
	if !strings.Contains(got.Detail, "No such file") {
		t.Errorf("the command's own reason is missing: %s", got.Detail)
	}
}

// A command the level refused still says so, so it is not retried as though
// it might work the next time.
func TestBash_ARefusalNamesTheLevel(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the lock is Linux's")
	}
	dir := t.TempDir()
	ctx := toolapi.WithLevel(context.Background(), toolapi.ImpactObserve)
	got, _ := NewBash("").ExecuteTyped(ctx, map[string]any{"command": "touch " + filepath.Join(dir, "f")})
	if _, err := os.Stat(filepath.Join(dir, "f")); err == nil {
		t.Skip("this kernel did not lock the command")
	}
	if !strings.Contains(got.Detail, "runs at level") {
		t.Errorf("a refused command does not name the level: %s", got.Detail)
	}
}

// A command that failed with nothing on stderr, because it sent its errors to
// /dev/null, says that the cause is not known, and names the level as one
// possible cause. Said with nothing, "exit 1" and an empty output read to every
// stage as "nothing there": run 19:40 2026-11-12 reported google_sudoers as
// empty and no SSH keys on the machine, from reads that had been refused.
func TestBash_AFailureWithNoErrorMessageSaysTheCauseIsUnknown(t *testing.T) {
	ctx := toolapi.WithLevel(context.Background(), toolapi.ImpactObserve)
	got, _ := NewBash("").ExecuteTyped(ctx, map[string]any{"command": "cat /no/such/file 2>/dev/null"})
	for _, want := range []string{"printed no error message", "/dev/null", "cause is not known", "runs at level"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("the failure should say %q: %s", want, got.Detail)
		}
	}
}

// A program that is not installed: the shell's exit 127 says so even when its
// message was discarded.
func TestBash_ExitCode127SaysAProgramWasNotFound(t *testing.T) {
	ctx := toolapi.WithLevel(context.Background(), toolapi.ImpactObserve)
	got, _ := NewBash("").ExecuteTyped(ctx, map[string]any{"command": "no_such_program_xyz 2>/dev/null"})
	if !strings.Contains(got.Detail, "was not found (exit 127)") {
		t.Errorf("exit 127 should say a program was not found: %s", got.Detail)
	}
}

// skyfield's own failure, as run fe1e1df3 (2026-11-16) printed it: the
// ValueError, then advice whose last line is example code.
const skyfieldFailure = `Loading ephemeris from media/de421.bsp
Traceback (most recent call last):
  File "/workspace/project/fe1e1df3/compute_ssb.py", line 26, in <module>
    main()
  File "/usr/lib/python3.11/site-packages/skyfield/timelib.py", line 1204, in _datetime_to_utc_tuple
    raise ValueError(_naive_complaint)
ValueError: cannot interpret a datetime that lacks a timezone

You must either specify that your datetime is in UTC:

    from skyfield.api import utc
    d = datetime(..., tzinfo=utc)  # to build a new datetime
    d = d.replace(tzinfo=utc)      # to fix an existing datetime

Or use a timezone object like those provided by the third-party ` + "`pytz`" + ` library:

    from pytz import timezone
    eastern = timezone('US/Eastern')
    d = eastern.localize(datetime(2014, 1, 16, 1, 32, 9))
`

// writeFailing returns a command that prints skyfieldFailure, on stderr or
// stdout, and exits 1.
func writeFailing(t *testing.T, toStderr bool) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(out, []byte(skyfieldFailure), 0o644); err != nil {
		t.Fatal(err)
	}
	redirect := ""
	if toStderr {
		redirect = " >&2"
	}
	return "cat " + out + redirect + "; exit 1"
}

// The engine runs a compute script as "( … ) 2>&1": stderr is empty and the
// traceback is in stdout. The reason is the error line, not "printed no error
// message" and not the last line of the advice after it.
func TestBash_MergedStderrStillSaysWhy(t *testing.T) {
	ctx := toolapi.WithLevel(context.Background(), toolapi.ImpactObserve)
	got, _ := NewBash("").ExecuteTyped(ctx, map[string]any{"command": "( " + writeFailing(t, true) + " ) 2>&1"})
	if !strings.Contains(got.Detail, "ValueError: cannot interpret a datetime that lacks a timezone") {
		t.Errorf("the error line is not the reason: %s", got.Detail)
	}
	for _, wrong := range []string{"printed no error message", "eastern.localize", "may have been refused"} {
		if strings.Contains(got.Detail, wrong) {
			t.Errorf("the reason says %q: %s", wrong, got.Detail)
		}
	}
}

// The same failure on stderr, unmerged: the error line again, not the last line.
func TestBash_StderrReasonIsTheErrorLine(t *testing.T) {
	ctx := toolapi.WithLevel(context.Background(), toolapi.ImpactObserve)
	got, _ := NewBash("").ExecuteTyped(ctx, map[string]any{"command": writeFailing(t, true)})
	if !strings.Contains(got.Detail, "ValueError: cannot interpret a datetime") || strings.Contains(got.Detail, "eastern.localize") {
		t.Errorf("the reason is not the error line: %s", got.Detail)
	}
}

// A program that prints its error on stdout without any merge still has it
// found, and said to be from stdout.
func TestBash_ErrorOnStdoutIsFound(t *testing.T) {
	ctx := toolapi.WithLevel(context.Background(), toolapi.ImpactObserve)
	got, _ := NewBash("").ExecuteTyped(ctx, map[string]any{"command": writeFailing(t, false)})
	if !strings.Contains(got.Detail, "ValueError: cannot interpret a datetime") || !strings.Contains(got.Detail, "stdout") {
		t.Errorf("the error on stdout was not given as the reason: %s", got.Detail)
	}
}

// The error survives in the result's stdout when a long text follows it: the
// stream was kept as its first 200 and last 600 bytes.
func TestBash_FailureKeepsTheErrorUnderALongTail(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	body := strings.Repeat("loading line\n", 50) + "KeyError: 'sun'\n" + strings.Repeat("advice line that goes on\n", 60)
	if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := toolapi.WithLevel(context.Background(), toolapi.ImpactObserve)
	got, _ := NewBash("").ExecuteTyped(ctx, map[string]any{"command": "( cat " + out + "; exit 1 ) 2>&1"})
	if !strings.Contains(string(got.Data), "KeyError: 'sun'") {
		t.Errorf("the error was cut out of the failure's stdout: %.400s", got.Data)
	}
	if !strings.Contains(got.Detail, "KeyError: 'sun'") {
		t.Errorf("the reason is missing: %s", got.Detail)
	}
}

// Output that states no error: not "printed no error message" over text the
// reader can see.
func TestBash_StdoutWithNoErrorSaysSo(t *testing.T) {
	ctx := toolapi.WithLevel(context.Background(), toolapi.ImpactObserve)
	got, _ := NewBash("").ExecuteTyped(ctx, map[string]any{"command": `echo "42 rows"; exit 3`})
	if strings.Contains(got.Detail, "42 rows") {
		t.Errorf("ordinary output was given as the reason: %s", got.Detail)
	}
	if !strings.Contains(got.Detail, "printed nothing on stderr") {
		t.Errorf("the detail should say stderr was empty and stdout states no error: %s", got.Detail)
	}
}
