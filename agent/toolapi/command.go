package toolapi

import (
	"context"
	"os/exec"

	"github.com/Compdeep/kaiju/sandbox"
)

// How a tool starts a program.
//
// Every program a tool starts goes through Command (or CommandDetached), never
// exec.Command, so that one place decides what it may do: the effects the
// step's level allows (package sandbox). The level is the most the step may do
// — the lower of the run's intent and the clearance — not the tool's own
// rating, which for bash comes from matching the command's text and can be
// wrong. A context that carries no level is level 0, so a path that forgets to
// set one gets the least.

type levelKey struct{}
type writableKey struct{}
type engineKey struct{}

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

// level is the level programs started under ctx run at: 0 when none was set.
func level(ctx context.Context) int {
	l, _ := LevelOf(ctx)
	return l
}

/*
 * ForEngine returns a context for programs the engine starts itself, from its
 * own command rather than a step's, such as a plugin's host: they run as this
 * program, with neither a lock nor root.
 */
func ForEngine(ctx context.Context) context.Context {
	return context.WithValue(ctx, engineKey{}, true)
}

// IsEngine reports whether ctx is ForEngine's.
func IsEngine(ctx context.Context) bool {
	e, _ := ctx.Value(engineKey{}).(bool)
	return e
}

/*
 * WithWritable returns a context naming folders a program may still write
 * under when its step may not change files, such as the engine's workspace,
 * where generated code is written and run.
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
 * Limits says what programs started under ctx may and may not do, for a tool
 * to add to the output of a command that failed.
 * return: one sentence, or "" when they may do everything.
 */
func Limits(ctx context.Context) string {
	if IsEngine(ctx) {
		return ""
	}
	return sandbox.Describe(level(ctx))
}

/*
 * Command is exec.CommandContext for a tool, with the effects ctx's level
 * allows.
 * desc: Used exactly like a command from exec.CommandContext. When a lock it
 *       needs cannot be applied, its Err is set and Start returns the reason
 *       without running anything.
 */
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	if IsEngine(ctx) {
		return exec.CommandContext(ctx, name, args...)
	}
	return sandbox.Command(ctx, level(ctx), Writable(ctx), name, args...)
}

/*
 * CommandDetached is Command for a program that must outlive the step, such
 * as a server a tool leaves running. ctx gives only the level; it does not end
 * the program.
 */
func CommandDetached(ctx context.Context, name string, args ...string) *exec.Cmd {
	if IsEngine(ctx) {
		return exec.Command(name, args...)
	}
	return sandbox.Detached(level(ctx), Writable(ctx), name, args...)
}
