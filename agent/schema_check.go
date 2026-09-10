package agent

import (
	"log"
	"sort"

	"github.com/Compdeep/kaiju/agent/llm"
)

// Which stages the provider can actually be asked to enforce.
//
// Every reasoning stage declares one schema and pins the model to it, and the
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
		// The tools the registry holds, because the plan schema describes a step
		// per tool and the shape depends on which. Called with no list it built
		// the open fallback — a document production never sends — so the check
		// reported on a shape that does not exist and left the one that does
		// unexamined.
		//
		// A run is shown a NARROWER list than this, so what the check reads is
		// the widest form of the document. A narrower one is a subset of these
		// branches: if every tool here can be carried, so can any selection.
		"plan":         a.executivePlanSchema(a.registryToolNames()),
		"route":        routeSchema(),
		"preflight":    preflightSchema(),
		"reflector":    reflectorSchema(),
		"observer":     observerSchema(),
		"group_review": groupReviewSchema(),
		"holmes":       holmesSchema(),
		"debugger":     debuggerSchema(),
		"curator":      curatorSchema(),
		"architect":    architectSchema(),
		// The coder's shape depends on whether the node may edit an existing
		// file. Both are checked: a schema that is only wrong in one of them is
		// wrong on the runs that take that branch.
		"coder(write)": coderSchema(false),
		"coder(edit)":  coderSchema(true),
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
		log.Printf("[schema] %s cannot be carried by strict, so it stays on tool calling:", s.Stage)
		for _, p := range s.Problems {
			log.Printf("[schema]   %s", p)
		}
	}
	if unenforceable > 0 {
		// Said as a count rather than a warning per stage: these are not faults
		// to go and fix. A params object holds whatever the tool a sibling field
		// names requires, and strict has no way to write "keys I cannot list in
		// advance" — so the stage runs on the wire that can express it.
		//
		// This message used to say each was "a 400 the client reads as a missing
		// capability", which was true when the request went out claiming strict
		// anyway. asSchemaRequest now asks the checker before sending, so the
		// round trip and the model-wide fallback it triggered no longer happen.
		log.Printf("[schema] %d stage(s) run on tool calling because strict cannot express "+
			"their shape. Nothing is sent claiming enforcement it would not get.", unenforceable)
	}
	return unenforceable
}

// registryToolNames is every tool this build has, for the boot check.
//
// Not what any run is shown — relevantTools narrows that per run — but the
// superset, so the check reads the widest form of the plan schema. Empty when
// no registry has been built, which leaves executivePlanSchema on its open
// fallback and the check reporting on that.
func (a *Agent) registryToolNames() []string {
	if a.registry == nil {
		return nil
	}
	return a.registry.List()
}
