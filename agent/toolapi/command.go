package toolapi

import (
	"context"
	"os/exec"

	"github.com/Compdeep/kaiju/sandbox"
)

// How a tool starts a program.
//
// Every program a tool starts goes through Command (or CommandDetached), never
// exec.Command, so that one place decides whether it is locked. A step allowed
// less than ImpactControl runs its programs locked: they can read and cannot
// change anything (package sandbox). The level is the most the step may do —
// the lower of the run's intent and the clearance — not the tool's
// own rating, which for bash comes from matching the command's text and can be
// wrong. A context that carries no level is locked, so a path that forgets to
// set one fails safe.

type levelKey struct{}
type writableKey struct{}

/*
 * WithLevel returns a context carrying the most the step it runs may do.
 * param: level - an impact rank (ImpactObserve, ImpactAffect, ImpactControl).
 */
func WithLevel(ctx context.Context, level int) context.Context {
	return context.WithValue(ctx, levelKey{}, level)
}

/*
 * LevelOf returns the level WithLevel set, and false when none was.
 */
func LevelOf(ctx context.Context) (int, bool) {
	l, ok := ctx.Value(levelKey{}).(int)
	return l, ok
}

/*
 * WithWritable returns a context naming folders a locked program may still
 * write under, such as the engine's workspace, where generated code is written
 * and run.
 */
func WithWritable(ctx context.Context, dirs ...string) context.Context {
	return context.WithValue(ctx, writableKey{}, append(Writable(ctx), dirs...))
}

// Writable returns the folders WithWritable named.
func Writable(ctx context.Context) []string {
	d, _ := ctx.Value(writableKey{}).([]string)
	return d
}

/*
 * Locked reports whether programs started under ctx are locked.
 */
func Locked(ctx context.Context) bool {
	l, ok := LevelOf(ctx)
	return !ok || l < ImpactControl
}

/*
 * Command is exec.CommandContext for a tool: locked unless ctx allows control.
 * desc: Used exactly like a command from exec.CommandContext. When a locked
 *       command cannot be locked, its Err is set and Start returns the reason
 *       without running anything.
 */
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	return sandbox.Command(ctx, Locked(ctx), Writable(ctx), name, args...)
}

/*
 * CommandDetached is Command for a program that must outlive the step, such
 * as a server a tool leaves running. ctx gives only the level; it does not end
 * the program.
 */
func CommandDetached(ctx context.Context, name string, args ...string) *exec.Cmd {
	return sandbox.Detached(Locked(ctx), Writable(ctx), name, args...)
}
