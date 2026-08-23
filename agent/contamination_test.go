package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// Contamination: an application's vocabulary inside the engine.
//
// This package is a framework. An application embedding it has machines with
// roles, work with a discipline, tools with names of its own — and none of that
// belongs here, not in the code, not in a comment, not in a test fixture. A
// framework that knows one application's words has quietly become that
// application's library.
//
// It got in through comments explaining where a change came from, and through
// test data borrowing a real deployment's machine names. Both read as harmless
// and both are how a name becomes load-bearing: the next person to touch the
// file assumes the word means something here.
//
// If a word below is genuinely this engine's, the fix is to say so and take it
// off the list, not to work around the check.
//
// The list was six words that appear nowhere in this engine, so the check
// passed on every file and was cited as evidence the boundary held. The words
// below are the ones a sweep of the embedding application's 305 struct
// declarations and their vocabulary actually found here. See
// kaiju-leakage.md for the count behind each.
var foreignWords = []string{
	// The application itself, and its machine roles.
	"enbarr", "omamori",
	"queen", "pawn", "knight",
	"security_triage",

	// What it watches. Present in this engine today; see kaiju-leakage.md.
	"alert", "alerts",
	"threat", "threats",
	"incident", "incidents",
	"triage",
	"severity",
	"campaign", "campaigns",
	"finding", "findings",
	"suppression", "suppressions",
	"telemetry",
	"technique", "techniques",

	// Words it has not put here yet. Listed so the first one to arrive fails
	// this test rather than being noticed a hundred commits later, which is
	// what happened to every word above.
	"malware", "ransomware", "rootkit", "spyware", "trojan",
	"forensic", "forensics",
	"quarantine", "containment", "remediate",
	"ioc", "iocs", "mitre", "attck", "cve", "cvss",
	"exfil", "exfiltration", "honeypot", "siem", "edr", "xdr", "soc",
	"sever", "severed",

	// Its swarm. A peer-to-peer fleet of machines is one product's shape and
	// this engine reaches other machines through an opaque target string.
	"fleet", "fleets",
	"peer", "peers",
	"swarm", "mesh", "relay", "beacon", "gossip", "murmur",
	"roster",
	"enroll", "enrolled", "enrolment", "enrollment",
	"evict", "eviction",
	"revoke", "revocation",
	"cosign",
}

// Words checked one at a time and deliberately left off, because this engine
// uses them for its own purposes and a check that fires on correct code is a
// check somebody switches off:
//
//	outcome       219 occurrences. Answer.Outcome and Conclusion.Outcome are
//	              this engine's, documented as the application's conclusion
//	              carried through. It belongs with Severity and Category in the
//	              decision recorded as L6, not on this list.
//	endpoint      70. An LLM endpoint.
//	investigation rca.go's own word for a root-cause run.
//	finding is ON the list, but note that nine of its occurrences are ordinary
//	              English — "that is a finding, not a blank success". Those are
//	              expected failures and the fix is to reword them.
//	sensor        twice, both listing what an opaque target might be.
//	remediation   6. edge_grounding's own word for the steps it returns.
//	dedup         42. This engine dedupes its own things.
//	invite        3, the ordinary verb: "invites a plan that cannot be written".
//	ban           too short and too common inside other words to be worth it.

// componentsOf splits a line into the words inside its identifiers, so a name
// that buries an application's word still fails.
//
// Whole-word matching was the rule before this, for a good reason — "spawn"
// contains "pawn" and a check that cannot tell the difference gets switched
// off. It also missed "no_alert", "fleetSection" and "triggerID", which are the
// same leak wearing camel case. Splitting on non-letters and on a lower-to-
// upper transition gets both: "spawn" stays one word, "fleetSection" becomes
// two, "no_alert" becomes two.
func componentsOf(line string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = nil
		}
	}
	runes := []rune(line)
	for i, r := range runes {
		if !unicode.IsLetter(r) {
			flush()
			continue
		}
		// A capital after a small letter starts a new word: fleetSection.
		if i > 0 && unicode.IsUpper(r) && unicode.IsLower(runes[i-1]) {
			flush()
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

func TestNoContaminationFromTheEmbeddingApplication(t *testing.T) {
	forbidden := make(map[string]bool, len(foreignWords))
	for _, w := range foreignWords {
		forbidden[strings.ToLower(w)] = true
	}

	// The whole module, not one package, and no directory exempt. The walk used
	// to start at the agent package, so tools/, internal/, pkg/ and cmd/ were
	// never read — and that is where AuditEntry.TriggerID sat.
	//
	// tests/ was exempt after that, on the reasoning that an evaluation harness
	// is not the engine. It is still this repository's code: tests/eval/holmes
	// carries alertID and extractAlertID, and reads /tmp/kaiju-prompts/<alert
	// _id>.log. A word an engineer meets while reading a harness is a word this
	// engine appears to use, whichever directory it sits in.
	root := moduleRoot(t)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.Contains(path, "contamination_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			seen := map[string]bool{}
			var found []string
			for _, w := range componentsOf(line) {
				if !forbidden[w] || seen[w] {
					continue
				}
				seen[w] = true
				found = append(found, w)
			}

			reason, exempt := exemptionOn(line)
			switch {
			case exempt && reason == "":
				t.Errorf("%s:%d carries %s with no reason after it. A reason is the "+
					"whole mechanism: without one this is a way to switch the check "+
					"off quietly", path, i+1, exemptMarker)
			case exempt && len(found) == 0:
				t.Errorf("%s:%d is exempt and names none of the words. Either the line "+
					"changed and the exemption should go, or it was never needed:\n    %s",
					path, i+1, strings.TrimSpace(line))
			case exempt:
				continue // named for a reason that is written down
			}

			for _, w := range found {
				t.Errorf("%s:%d names %q, which is an application's word and not this "+
					"engine's:\n    %s", path, i+1, w, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// A line that has to name one of these words.
//
// Some do. A test that proves the engine never says "alert" has to write the
// word to check for its absence. An operating system's own error text is not
// ours to rename. An attack string in a URL filter's test is the attack. The
// words are right in those places and the check is right everywhere else, so the
// exemption is per line and it is written on the line, where a reader meets it —
// not in a list at the top of this file that drifts as lines move.
//
//	Messages: []llm.Message{{Role: "system", Content: "…"}}, // foreign-word-ok: model-facing text
//
// Two things fail rather than pass: a marker with no reason after it, and a
// marker on a line that names none of the words. The first is the check being
// switched off quietly; the second is an exemption outliving what it excused.
const exemptMarker = "// foreign-word-ok:"

// exemptionOn returns the reason written on a line, and whether the line carries
// a marker at all.
func exemptionOn(line string) (string, bool) {
	i := strings.Index(line, exemptMarker)
	if i < 0 {
		return "", false
	}
	return strings.TrimSpace(line[i+len(exemptMarker):]), true
}

// moduleRoot walks up from the package directory to the directory holding
// go.mod, so the check covers the module however it is invoked.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolving the package directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package directory — the walk has no root")
		}
		dir = parent
	}
}
