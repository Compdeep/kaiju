package llm

import "strings"

// What a caller wants of a model's thinking.
//
// Three separate questions travel together and are easy to confuse: whether to
// think at all, how hard, and how much of the reply the thinking may use. They
// are separate because a model may answer one and ignore the others — the
// catalog records which, per model, from measurement.

// Want is whether this call should reason.
type Want int

const (
	// WantAuto says nothing and takes the model's own default. The zero value,
	// and deliberately not WantOff: reading silence as "do not think" lets a
	// caller that never considered the question change how every answer it
	// touches is written.
	WantAuto Want = iota
	// WantOff refuses reasoning, where the model allows that.
	WantOff
	// WantOn asks for it even from a model that ships with it off.
	WantOn
)

// Effort is how hard to think, on an ordered ladder.
//
// The words are the providers' and not ours — minimal, low, medium, high,
// xhigh, max — and no model takes all six. Ordered because the ladder has to
// mean something on the models that ignore the parameter entirely: the deadline
// moves with the ordinal whether or not the provider honours the word.
type Effort int

const (
	EffortUnset Effort = iota
	EffortMinimal
	EffortLow
	EffortMedium
	EffortHigh
	EffortXHigh
	EffortMax
)

var effortNames = [...]string{"", "minimal", "low", "medium", "high", "xhigh", "max"}

// String is the provider's word for this effort, and "" for unset.
func (e Effort) String() string {
	if e < 0 || int(e) >= len(effortNames) {
		return ""
	}
	return effortNames[e]
}

/*
 * ParseEffort reads an effort from configuration or a request.
 * desc: Case-insensitive, and an unrecognised word is EffortUnset rather than
 *       an error: a mistyped setting should leave the model alone, not refuse
 *       the call.
 * param: s - the word.
 * return: the effort, and whether it was recognised.
 */
func ParseEffort(s string) (Effort, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for i, name := range effortNames {
		if name != "" && name == s {
			return Effort(i), true
		}
	}
	return EffortUnset, false
}

// Efforts is the whole ladder, weakest first — for a picker that has to offer
// them in an order rather than in whatever order a catalog listed them.
func Efforts() []Effort {
	return []Effort{EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}
}

// Reasoning is what a caller wants of the model's thinking on one request.
//
// The zero value asks for nothing and is what a caller that has no opinion
// leaves behind.
type Reasoning struct {
	Want   Want
	Effort Effort
	// Budget is thinking tokens. Zero lets the client divide the reply
	// allowance, which is the usual case — thinking and answering come out of
	// one number on the wire, and a caller rarely knows how to split it.
	Budget int
}

// On reports whether this asks for thinking. Absent is not a no.
func (r *Reasoning) On() bool { return r != nil && r.Want == WantOn }

// Off reports whether this refuses thinking.
func (r *Reasoning) Off() bool { return r != nil && r.Want == WantOff }
