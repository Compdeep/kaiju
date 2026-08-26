package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

/*
 * Recording why a ${step.N.field} reference was rejected.
 *
 * The rejection message names the field and the producer and nothing else, so
 * reading it after the fact tells you a reference failed but not what it was
 * checked against. The same message is produced whether the field is absent
 * from both schema levels, present but under a different name, or present in a
 * schema the tool did not declare at all — and those need different fixes.
 *
 * So the check writes down what it saw: the reference, the producing tool, the
 * names the envelope level offers, the names the payload offers, and the raw
 * schema it read them from. Enough to tell, without re-deriving it from the
 * source, which of those cases this was.
 *
 * Diagnostic only. It never changes a decision, never fails a run, and writes
 * nothing when the directory is unavailable — a trace that can break the thing
 * it traces is worse than no trace.
 */

const refDebugDir = "/tmp/kaiju-prompts"

// logRejectedReference appends one rejection to refs-rejected.log.
func logRejectedReference(raw, field, producer string, outSchema json.RawMessage) {
	defer func() { _ = recover() }() // a diagnostic must never take the run with it

	f, err := os.OpenFile(filepath.Join(refDebugDir, "refs-rejected.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	var sb strings.Builder
	fmt.Fprintf(&sb, "\n=== %s  %s rejected ===\n", time.Now().UTC().Format(time.RFC3339), raw)
	fmt.Fprintf(&sb, "field wanted   : %s\n", field)
	fmt.Fprintf(&sb, "producer tool  : %s\n", producer)
	fmt.Fprintf(&sb, "envelope offers: %s\n", strings.Join(schemaTopLevelNames(outSchema), ", "))
	if data := envelopeData(outSchema); data != nil {
		fmt.Fprintf(&sb, "payload offers : %s\n", strings.Join(schemaTopLevelNames(data), ", "))
	} else {
		sb.WriteString("payload offers : (no data section in the declared schema)\n")
	}
	fmt.Fprintf(&sb, "resolver would : %s\n", resolverVerdict(field))
	fmt.Fprintf(&sb, "declared schema:\n%s\n", string(outSchema))
	f.WriteString(sb.String())
}

// schemaTopLevelNames lists a schema's property names, sorted.
func schemaTopLevelNames(schema json.RawMessage) []string {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if len(schema) == 0 || json.Unmarshal(schema, &s) != nil || len(s.Properties) == 0 {
		return []string{"(none declared)"}
	}
	out := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// resolverVerdict says what toolMessageBody would do with this field at RUN
// time, which is the comparison worth having: a reference the validator refuses
// and the resolver would have served is a disagreement between two rules, not a
// bad plan.
func resolverVerdict(field string) string {
	switch strings.SplitN(field, ".", 2)[0] {
	case "content", "detail", "status", "type":
		return "RESOLVE IT — envelopeField serves this name at run time; " +
			"validator and resolver disagree"
	case "data":
		return "RESOLVE IT — the resolver strips a leading \"data\" as the payload wrapper"
	}
	return "also fail — no envelope name matches; the field is genuinely absent"
}
