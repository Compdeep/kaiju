package memory

import "testing"

// The two shapes, taken from real stored traces.
//
// An agent run carries tool nodes naming what ran; a chat turn carries a chat node
// and no tools. These are the traces of two consecutive messages in one live
// session: the second answered "Confirmed in source and generated outputs" about
// the files the first had written.
func TestProvenanceOfTellsAnAgentRunFromAChatTurn(t *testing.T) {
	agentRun := `[
	  {"id":"n0","type":"preflight","state":"done","tag":"reading the request"},
	  {"id":"n1","type":"executive","state":"done","tag":"plan"},
	  {"id":"n2","type":"tool","state":"done","tag":"read_sections_py","tool":"file_read"},
	  {"id":"n3","type":"tool","state":"done","tag":"edit_architecture_intro","tool":"edit_file"},
	  {"id":"n4","type":"tool","state":"done","tag":"rebuild_docs","tool":"bash"},
	  {"id":"n5","type":"tool","state":"done","tag":"verify_intro_update","tool":"bash"},
	  {"id":"n6","type":"reflection","state":"done","tag":"reflect"}
	]`
	if got := provenanceOf(agentRun); got != "file_read, edit_file, bash" {
		t.Errorf("provenanceOf(agent run) = %q, want the tools in first-seen order, deduplicated", got)
	}

	chatTurn := `[
	  {"id":"n0","type":"preflight","state":"done","tag":"reading the request"},
	  {"id":"n1","type":"chat","state":"done","tag":"chat"}
	]`
	if got := provenanceOf(chatTurn); got != "" {
		t.Errorf("provenanceOf(chat turn) = %q, want empty: it ran nothing", got)
	}
}

// Absence is not a claim.
//
// Most stored history predates tracing, and a chat-lane, vision or CLI reply never
// writes one, so "" has to mean "nothing is known" everywhere it is read. Reporting
// it as "ran nothing" would describe three quarters of the record as fabricated.
func TestProvenanceOfSaysNothingWhenNothingIsKnown(t *testing.T) {
	for name, trace := range map[string]string{
		"no trace":    "",
		"whitespace":  "   \n",
		"not json":    "{broken",
		"wrong shape": `{"nodes":[]}`,
		"empty array": "[]",
	} {
		if got := provenanceOf(trace); got != "" {
			t.Errorf("provenanceOf(%s) = %q, want empty", name, got)
		}
	}
}

// compute is the coder: it writes files, so it is work even though what it ran is
// not a named tool.
func TestProvenanceOfCountsCompute(t *testing.T) {
	trace := `[{"id":"n0","type":"compute","state":"done","tag":"write_the_patch"}]`
	if got := provenanceOf(trace); got != "compute" {
		t.Errorf("provenanceOf(compute) = %q, want compute", got)
	}
}
