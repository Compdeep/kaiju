package toolapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

/*
 * Writes made in a tool's own code.
 *
 * A program a tool starts is held by the operating system's lock. A file a
 * tool writes itself is not: it is written by this program's account, so
 * the only check is the tool's. A check on the path's text is not one: a
 * symlink inside the workspace (made by a bash step, or by a clone) sends the
 * write wherever it points. These functions find where the write really lands
 * before writing, and refuse below control unless that is inside the step's
 * writable folders (Writable: the workspace). At control, or for the engine's
 * own work, anywhere. A call with no level is held at level 0, as Command
 * holds it.
 *
 * Every write a tool makes itself goes through here: tools/writes_test.go
 * fails on a tool that calls os.Create, os.WriteFile, os.OpenFile, os.Mkdir,
 * os.MkdirAll or os.Rename.
 */

// stepRoots is where the step ctx belongs to may write: nil for anywhere.
func stepRoots(ctx context.Context) []string {
	if ctx != nil && IsEngine(ctx) || level(ctx) >= ImpactControl {
		return nil
	}
	if ctx == nil {
		return []string{}
	}
	return append([]string{}, Writable(ctx)...)
}

// CreateForStep opens path to write (flag as os.OpenFile's) for the step ctx
// belongs to, making its folder first. A refusal is a *Refusal.
func CreateForStep(ctx context.Context, path string, flag int, perm os.FileMode) (*os.File, error) {
	return createWithin(stepRoots(ctx), path, flag, perm)
}

// WriteFileForStep is os.WriteFile for the step ctx belongs to.
func WriteFileForStep(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	_, err := writeWithin(stepRoots(ctx), path, data, perm)
	return err
}

// MkdirAllForStep is os.MkdirAll for the step ctx belongs to.
func MkdirAllForStep(ctx context.Context, dir string, perm os.FileMode) error {
	_, err := realDir(resolveRoots(stepRoots(ctx)), dir, perm)
	return err
}

// WriteFileIn writes a tool's own file, which must land inside root whatever
// the step's level: a tool's record or saved output in its folder.
func WriteFileIn(root, path string, data []byte, perm os.FileMode) error {
	_, err := writeWithin([]string{root}, path, data, perm)
	return err
}

// CreateIn is CreateForStep for a tool's own file inside root.
func CreateIn(root, path string, flag int, perm os.FileMode) (*os.File, error) {
	return createWithin([]string{root}, path, flag, perm)
}

// ReplaceFileIn writes a tool's own file inside root through a temporary file
// and a rename, so a reader never sees half of it.
func ReplaceFileIn(root, path string, data []byte, perm os.FileMode) error {
	tmp, err := writeWithin([]string{root}, path+".tmp", data, perm)
	if err != nil {
		return err
	}
	// The rename replaces whatever is at the name, a symlink included, and
	// does not follow it.
	return os.Rename(tmp, filepath.Join(filepath.Dir(tmp), filepath.Base(path)))
}

// writeWithin writes data at path and returns where it really landed.
func writeWithin(roots []string, path string, data []byte, perm os.FileMode) (string, error) {
	f, err := createWithin(roots, path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return "", err
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return f.Name(), werr
}

func createWithin(roots []string, path string, flag int, perm os.FileMode) (*os.File, error) {
	real := resolveRoots(roots)
	dir, err := realDir(real, filepath.Dir(path), 0o755)
	if err != nil {
		return nil, err
	}
	if real == nil {
		return os.OpenFile(filepath.Join(dir, filepath.Base(path)), flag, perm)
	}
	return createNoFollow(real, dir, filepath.Base(path), flag, perm)
}

/*
 * realDir makes dir and returns where it really is, refusing a folder outside
 * real, the writable folders already resolved (nil: anywhere). The folder
 * that already exists is resolved as the system resolves it, symlinks
 * followed, and checked before anything is made in it; what is made is
 * checked again.
 */
func realDir(real []string, dir string, perm os.FileMode) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if real == nil {
		return abs, os.MkdirAll(abs, perm)
	}
	if err := LocalPath(abs); err != nil {
		return "", err
	}
	if len(real) == 0 {
		return "", &Refusal{Why: fmt.Sprintf("%s: below level %d a tool writes only in the workspace, and this step has none", dir, ImpactControl)}
	}
	existing := abs
	for {
		if _, err := os.Stat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if !insideAny(real, resolved) {
		return "", outside(dir, resolved)
	}
	rest, err := filepath.Rel(existing, abs)
	if err != nil {
		return "", err
	}
	target := filepath.Join(resolved, rest)
	if err := os.MkdirAll(target, perm); err != nil {
		return "", err
	}
	final, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	if !insideAny(real, final) {
		return "", outside(dir, final)
	}
	return final, nil
}

// resolveRoots resolves each writable folder, making it if it is missing; nil
// (anywhere) stays nil.
func resolveRoots(roots []string) []string {
	if roots == nil {
		return nil
	}
	out := []string{}
	for _, r := range roots {
		if r == "" || os.MkdirAll(r, 0o755) != nil {
			continue
		}
		if real, err := filepath.EvalSymlinks(r); err == nil {
			out = append(out, real)
		}
	}
	return out
}

func insideAny(roots []string, p string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func outside(asked, real string) error {
	return &Refusal{Why: fmt.Sprintf("%s is really %s, outside the workspace: below level %d a tool writes only there",
		asked, real, ImpactControl)}
}
