package agent

import (
	"log"
	"sort"

	"github.com/Compdeep/kaiju/agent/llm"
)

// Which stages the provider can actually be asked to enforce.
//
// Every reasoning stage declares one tool and pins the model to it, and the
// client rewrites all of them into a schema request with strict set — see
// llm/structured.go. Setting strict does not make a schema enforceable: a
// provider that checks refuses a schema breaking its rules, and a provider that
// does not check accepts the same schema and answers anyway.
//
// Nothing in a reply distinguishes those two. So the schemas are read here,
// once, at startup, and a stage that could never have been enforced says so in
// the log rather than looking like the ones that are.

// StageSchema is one stage's declared shape and what it would be sent as.
type StageSchema struct {
	// Stage is the name the model sees, which is also the name in the run log.
	Stage string
	// Problems is empty when the provider can hold the model to this schema.
	Problems []llm.StrictProblem
	// Converts is false when the call would stay on tool calling rather than
	// becoming a schema request — in which case strict never applied to it and
	// Problems is not a fault.
	Converts bool
}

/*
 * StageSchemas returns every stage that asks the model for one shape, with what
 * a strict provider would make of its schema.
 * desc: One entry per stage, sorted by name. A stage missing from this list is
 *       one that offers the model a choice of tools (the ReAct loop) or none at
 *       all (the aggregator, the chat lanes) — neither is a schema request and
 *       neither belongs here.
 * return: the stages, in name order.
 */
func (a *Agent) StageSchemas() []StageSchema {
	defs := map[string]llm.ToolDef{
		"plan":      a.executiveToolDef(),
		"route":     routeToolDef(),
		"preflight": preflightToolDef(),
		"reflector": reflectorToolDef(),
		"observer":  observerToolDef(),
		"holmes":    holmesToolDef(),
		"debugger":  debuggerToolDef(),
		"curator":   curatorToolDef(),
		"architect": architectToolDef(),
		// The coder's shape depends on whether the node may edit an existing
		// file. Both are checked: a schema that is only wrong in one of them is
		// wrong on the runs that take that branch.
		"coder(write)": coderToolDef(false),
		"coder(edit)":  coderToolDef(true),
	}

	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]StageSchema, 0, len(names))
	for _, name := range names {
		schema := llm.SchemaAsSent(defs[name])
		if schema == nil {
			out = append(out, StageSchema{Stage: name, Converts: false})
			continue
		}
		out = append(out, StageSchema{
			Stage:    name,
			Converts: true,
			Problems: llm.StrictProblems(schema),
		})
	}
	return out
}

/*
 * LogStageSchemas writes one startup line naming the stages that cannot be
 * enforced, and what is wrong with each.
 * desc: Silent when every stage is clean, because a line saying nothing is
 *       wrong is a line an operator learns to skip. Called after the intent
 *       registry loads: the plan schema's intent enum is built from it, and
 *       checking before it is populated reports an empty enum that the real
 *       request would not carry.
 * return: how many stages have a problem, so a caller can act on it.
 */
func (a *Agent) LogStageSchemas() int {
	unenforceable := 0
	for _, s := range a.StageSchemas() {
		if !s.Converts {
			log.Printf("[schema] %s stays on tool calling — its parameters are not an object, so strict never applied", s.Stage)
			continue
		}
		if len(s.Problems) == 0 {
			continue
		}
		unenforceable++
		log.Printf("[schema] %s is sent with strict set, but a provider that enforces strict would refuse it:", s.Stage)
		for _, p := range s.Problems {
			log.Printf("[schema]   %s", p)
		}
	}
	if unenforceable > 0 {
		log.Printf("[schema] %d stage(s) ask for enforcement they are not getting. "+
			"On a provider that checks, each is a 400 the client reads as a missing capability "+
			"and answers by falling back to tool calling — see rejectsSchemas.", unenforceable)
	}
	return unenforceable
}
