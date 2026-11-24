package toolapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/Compdeep/kaiju/sandbox"
)

// Refusal is a file a step may not read or write there: the result says why,
// it is not a fault.
type Refusal struct{ Why string }

func (r *Refusal) Error() string { return r.Why }

// Refused reports whether err is a Refusal, and why.
func Refused(err error) (string, bool) {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Why, true
	}
	return "", false
}

/*
 * LocalPath refuses a path that reaches the network or a device. On Windows a
 * UNC path (\\host\share) or a device path (\\?\, \\.\) makes the machine's
 * account authenticate to whatever host it names, before any permission is
 * checked; elsewhere every path is local.
 */
func LocalPath(path string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	if p := strings.ReplaceAll(path, "/", `\`); strings.HasPrefix(p, `\\`) {
		return &Refusal{Why: fmt.Sprintf("%s names a network share or a device; only a local path is read", path)}
	}
	return nil
}

/*
 * OpenForStep opens path for a tool that reads it in its own code, for the
 * step ctx belongs to: a local path only (LocalPath), and, once open, only
 * what the step's level may read (sandbox.MayReadFile), checked on the open
 * file. A refusal is a *Refusal; any other error is the open's own.
 */
func OpenForStep(ctx context.Context, path string) (*os.File, error) {
	if err := LocalPath(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if ctx == nil || IsEngine(ctx) {
		return f, nil
	}
	if err := sandbox.MayReadFile(level(ctx), f); err != nil {
		f.Close()
		return nil, &Refusal{Why: err.Error()}
	}
	return f, nil
}
