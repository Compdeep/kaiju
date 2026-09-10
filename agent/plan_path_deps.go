package agent

import (
	"log"
	"path"
	"regexp"
	"slices"
	"strings"
)

// A path written into a step's params, which another step may have produced.
//
// Deliberately narrow: a slash or a dot-extension, no spaces, no scheme. A bare
// word is not a path, and treating one as a path would order steps that merely
// mention the same noun.
var pathLikeRe = regexp.MustCompile(`[A-Za-z0-9_.\-/]*[/][A-Za-z0-9_.\-/]+|[A-Za-z0-9_\-]+\.[A-Za-z0-9]{1,6}\b`)

// Extensions that name a document rather than a path fragment. A token with a
// dot and nothing else to recommend it — "v4.2", "3.5" — is not a file.
var notAFileExt = map[string]bool{
	"0": true, "1": true, "2": true, "3": true, "4": true,
	"5": true, "6": true, "7": true, "8": true, "9": true,
}

/*
 * linkPathDeps orders a step after one that names the same file.
 * desc: A dependency the planner does not have to declare, because it can be
 *       seen.
 *
 *       depends_on was required on every step until the schema had to satisfy a
 *       strict decoder, which has no way to express an optional field — so it
 *       was moved out of required and its description became "Rarely needed".
 *       The planner stopped emitting it: one live run produced three plans and
 *       thirteen steps with the field absent from every one.
 *
 *       That run's ordering was plain to see. A bash step wrote
 *       extracted/pitch.txt; two file_read steps opened that exact path. Nothing
 *       connected them, so all three ran in one batch and the reads went looking
 *       for files the extraction had not written yet. A whole round wasted, and
 *       a replan spent rediscovering it.
 *
 *       The reference form is already repaired this way — a step writing
 *       ${step.find_docs.url} without depends_on has it added. This is the same
 *       repair for the case where the connection is a shared literal instead:
 *       one step names a file, an earlier step names the same file.
 *
 *       Ordering two steps that did not need it costs parallelism. Not ordering
 *       two that did costs the round. So this errs toward ordering.
 * param: steps - the plan, modified in place.
 */
func linkPathDeps(steps []PlanStep) {
	if len(steps) < 2 {
		return
	}
	paths := make([]map[string]bool, len(steps))
	for i := range steps {
		paths[i] = pathsIn(steps[i].Params)
	}

	for i := range steps {
		if len(paths[i]) == 0 {
			continue
		}
		for j := 0; j < i; j++ {
			if len(paths[j]) == 0 || slices.Contains(steps[i].DependsOn, j) {
				continue
			}
			shared := ""
			for p := range paths[i] {
				if paths[j][p] {
					shared = p
					break
				}
			}
			if shared == "" {
				continue
			}
			steps[i].DependsOn = append(steps[i].DependsOn, j)
			log.Printf("[dag] step %d (%s) names %q, which step %d (%s) also names — ordering it after",
				i, steps[i].Tag, shared, j, steps[j].Tag)
		}
	}
}

/*
 * pathsIn collects the file paths a step's params mention.
 * desc: Walks the params the way the reference repair does, so a path inside a
 *       shell command is found as well as one in a path field — the run this
 *       came from wrote its filenames inside a bash command on one side and in a
 *       path parameter on the other.
 *
 *       Normalised with path.Clean so "extracted/pitch.txt" and
 *       "./extracted/pitch.txt" are the same file, which they are.
 * param: params - the step's parameters.
 * return: the distinct paths named, cleaned.
 */
func pathsIn(params map[string]any) map[string]bool {
	out := map[string]bool{}
	walkParams(params, func(s string) (any, bool) {
		for _, m := range pathLikeRe.FindAllString(s, -1) {
			if p := cleanPathToken(m); p != "" {
				out[p] = true
			}
		}
		return s, false
	})
	return out
}

// cleanPathToken normalises a candidate, or returns "" when it is not a file.
func cleanPathToken(tok string) string {
	tok = strings.Trim(tok, "./")
	if tok == "" || len(tok) < 3 {
		return ""
	}
	// A dotted token with a numeric tail is a version, not a file: "v4.2".
	if ext := strings.TrimPrefix(path.Ext(tok), "."); ext != "" && notAFileExt[ext] {
		return ""
	}
	// Something with no slash and no extension is a bare word.
	if !strings.Contains(tok, "/") && path.Ext(tok) == "" {
		return ""
	}
	return path.Clean(tok)
}
