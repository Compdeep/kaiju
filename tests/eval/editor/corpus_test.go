package editor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The corpus is the only measure of whether editing works, and it is exercised
// by a harness that costs an LLM call per scenario. A scenario that will never
// pass — malformed JSON, a check that names no file, a query with nothing to
// verify it — is not discovered until someone pays for the run, and then looks
// like an editing failure rather than a broken fixture.
//
// These tests read the corpus and nothing else. No model, no network, so they
// run in the ordinary suite and keep the paid one honest.

const corpusRoot = "corpus"

type fixture struct {
	path      string // the file to be edited
	scenarios string // its companion .scenarios.jsonl
}

func corpusFixtures(t *testing.T) []fixture {
	t.Helper()
	var out []fixture
	err := filepath.Walk(corpusRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".scenarios.jsonl") {
			return err
		}
		out = append(out, fixture{
			path:      strings.TrimSuffix(path, ".scenarios.jsonl"),
			scenarios: path,
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk corpus: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("no fixtures found — the corpus is the whole point of this package")
	}
	return out
}

// Every scenario file must parse, and every scenario in it must be runnable.
func TestCorpus_EveryScenarioIsRunnable(t *testing.T) {
	var scenarios, core int
	for _, f := range corpusFixtures(t) {
		if _, err := os.Stat(f.path); err != nil {
			t.Errorf("%s: scenarios exist but the file to edit does not", f.scenarios)
			continue
		}
		raw, err := os.ReadFile(f.scenarios)
		if err != nil {
			t.Errorf("%s: unreadable: %v", f.scenarios, err)
			continue
		}
		for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var sc scenario
			if err := json.Unmarshal([]byte(line), &sc); err != nil {
				t.Errorf("%s line %d: not valid JSON — the harness would skip this silently: %v", f.scenarios, i+1, err)
				continue
			}
			scenarios++
			if sc.Core {
				core++
			}
			if strings.TrimSpace(sc.Query) == "" && sc.Skip == "" {
				t.Errorf("%s line %d: no query, so there is nothing for the editor to do", f.scenarios, i+1)
			}
			// Something has to decide whether the edit was right. A scenario with
			// neither a check nor an expectation passes whatever the editor does.
			if sc.Check == "" && sc.ExpectIn == "" && sc.ExpectNotIn == "" && sc.Skip == "" {
				t.Errorf("%s line %d: nothing verifies the result, so this scenario cannot fail", f.scenarios, i+1)
			}
			if sc.Check != "" && !strings.Contains(sc.Check, "$FILE") {
				t.Errorf("%s line %d: the check never names $FILE, so it does not read the edited copy", f.scenarios, i+1)
			}
		}
	}
	t.Logf("%d fixtures, %d scenarios, %d in the core tier", len(corpusFixtures(t)), scenarios, core)
}

// A check that cannot run is indistinguishable from an edit that was wrong, so
// each one is executed against the UNEDITED fixture. It must fail there: a check
// that already passes before anything is edited proves nothing about the edit.
func TestCorpus_EveryCheckFailsBeforeTheEdit(t *testing.T) {
	if testing.Short() {
		t.Skip("runs shell commands per scenario")
	}
	for _, f := range corpusFixtures(t) {
		raw, err := os.ReadFile(f.scenarios)
		if err != nil {
			continue
		}
		original, err := os.ReadFile(f.path)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var sc scenario
			if json.Unmarshal([]byte(line), &sc) != nil || sc.Check == "" || sc.Skip != "" {
				continue
			}
			work := filepath.Join(t.TempDir(), filepath.Base(f.path))
			if err := os.WriteFile(work, original, 0o644); err != nil {
				t.Fatalf("stage fixture: %v", err)
			}
			if runCheck(t, sc.Check, work) {
				t.Errorf("%s line %d: the check passes on the unedited file, so it would pass however the editor answered\n  query: %s",
					f.scenarios, i+1, trimTo(sc.Query, 90))
			}
		}
	}
}

func runCheck(t *testing.T, check, file string) bool {
	t.Helper()
	cmd := exec.Command("bash", "-c", check)
	cmd.Env = append(os.Environ(), "FILE="+file)
	return cmd.Run() == nil
}

func trimTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
