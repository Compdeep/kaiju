package agent

import (
	"context"
	"fmt"
	"log"
)

// Asking the application whether a call is authorised.
//
// Config.Clearance is an application's own authorisation, asked per tool call
// after this package's gate has already allowed it. It typically reaches
// something outside the process — a directory, an approval service — so it is
// the handler most likely to be given a bad answer to work from, and the one
// whose failure must not be read as a yes.

/*
 * checkClearance asks the application's authorisation check, if any.
 * desc: Nil approves, as an unconfigured check does. A panic REFUSES: a check
 *       that crashed has not approved anything, and the call it was asked about
 *       is the next thing to run. That is the same direction validateTarget and
 *       allowTool go, and the opposite of admit — the difference is whether the
 *       thing being decided is about to change something.
 * param: ctx - cancelled with the run.
 * param: tool - the tool about to run.
 * param: params - the call's parameters, as the checker may read them.
 * param: username - the principal, empty when the run has no scope.
 * return: nil when the call may proceed.
 */
func (a *Agent) checkClearance(ctx context.Context, tool string, params map[string]any, username string) (err error) {
	if a == nil || a.clearanceCheck == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[agent] the clearance check panicked, refusing %s: %v", tool, r)
			err = fmt.Errorf("clearance for %s could not be checked", tool)
		}
	}()
	return a.clearanceCheck.Check(ctx, tool, params, username)
}
