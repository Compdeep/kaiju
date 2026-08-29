package agent

import (
	"errors"
	"strings"
	"testing"
)

// A second attempt is only worth making if it is told something the first did
// not know. The retry sent one fixed sentence whatever had broken — a plan that
// put a ${step.N.field} reference where the params string belongs was answered
// with advice about a different mistake, and failed the same way twice.
func TestPlanRetryAdviceNamesTheActualMistake(t *testing.T) {
	paramsErr := errors.New(`params must be a JSON object written as a string, ` +
		`like "{\"path\": \"/tmp/x\"}" — this one does not parse: invalid character '$' looking for beginning of value`)

	withRef := planRetryAdvice(paramsErr, `{"steps":[{"tool":"inspect_process","params":${step.2.pid}}]}`)
	if !strings.Contains(withRef, "INSIDE") || !strings.Contains(withRef, "${step.2.pid}") {
		t.Errorf("a misplaced reference was not told where a reference goes:\n%s", withRef)
	}
	if strings.Contains(withRef, "goal, mode and query") {
		t.Errorf("answered a reference mistake with advice about step-level fields:\n%s", withRef)
	}

	// Same parse failure with no reference in it: still about params, but there
	// is no reference to explain, so it must not invent one.
	noRef := planRetryAdvice(paramsErr, `{"steps":[{"tool":"bash","params":"not json"}]}`)
	if strings.Contains(noRef, "${step") {
		t.Errorf("advice mentioned references where the plan used none:\n%s", noRef)
	}
	if !strings.Contains(noRef, "STRING") {
		t.Errorf("advice does not say params is a string:\n%s", noRef)
	}

	// The failure the original sentence was written for still gets it.
	fields := planRetryAdvice(errors.New(`json: unknown field "goal"`), `{"steps":[{"goal":"x"}]}`)
	if !strings.Contains(fields, "goal, mode and query") {
		t.Errorf("step-level fields lost their advice:\n%s", fields)
	}

	// Anything else still says what happened rather than nothing.
	other := planRetryAdvice(errors.New("something else entirely"), "{}")
	if !strings.Contains(other, "something else entirely") {
		t.Errorf("an unrecognised failure did not carry its own error:\n%s", other)
	}
}
