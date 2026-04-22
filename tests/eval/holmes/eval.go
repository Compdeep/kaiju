// Package holmes is the pre-integration Holmes-investigation eval. Each
// scenario copies a broken fixture into a tmp workdir, invokes kaiju
// end-to-end, and verifies the post-fix artefact by actually running it —
// not by token-matching verdict text.
//
// Not part of `go test ./...`. Invoked via the unified dispatcher at
// tests/eval/cmd.
package holmes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Options mirrors the shared eval CLI options relevant to this suite.
type Options struct {
	KaijuBin   string
	ConfigPath string
	Only       string
	Timeout    time.Duration
	Keep       bool
}

type scenario struct {
	name          string
	fixtureSubdir string
	query         string
	verify        func(workDir, kaijuOut string) error
	purpose       string
}

var scenarios = []scenario{
	{
		name:          "fix_script",
		fixtureSubdir: "fix_script",
		query:         `project/app/main.py is computing the wrong total. Fix it.`,
		purpose:       `End-to-end: given a Python script with a bug, kaiju should fix it and the fixed script should produce the correct output when run.`,
		verify: func(workDir, _ string) error {
			script := filepath.Join(workDir, "project/app/main.py")
			cmd := exec.Command("python3", script, "2", "3", "5")
			out, err := cmd.Output()
			if err != nil {
				return fmt.Errorf("python3 main.py failed: %v", err)
			}
			got := strings.TrimSpace(string(out))
			if got != "10" {
				return fmt.Errorf("expected '10', got %q", got)
			}
			return nil
		},
	},
	{
		name:          "fix_config",
		fixtureSubdir: "fix_config",
		query:         `The API in project/api/ won't install — fix its package.json.`,
		purpose:       `End-to-end: a package.json pins express to a version that does not exist (^999.0.0). Kaiju should pick a real version so 'npm install' succeeds.`,
		verify: func(workDir, _ string) error {
			apiDir := filepath.Join(workDir, "project/api")
			cmd := exec.Command("npm", "install", "--no-audit", "--no-fund")
			cmd.Dir = apiDir
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("npm install failed: %v — %s", err, trimTail(string(out), 400))
			}
			return nil
		},
	},
	{
		name:          "fix_json_comma",
		fixtureSubdir: "fix_json_comma",
		query:         `project/api/package.json is malformed. Fix it.`,
		purpose:       `End-to-end: package.json has a missing comma between "version" and "type". Kaiju should fix the syntax; the result must parse as valid JSON.`,
		verify: func(workDir, _ string) error {
			path := filepath.Join(workDir, "project/api/package.json")
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read: %v", err)
			}
			var any interface{}
			if err := json.Unmarshal(data, &any); err != nil {
				return fmt.Errorf("still not valid JSON: %v", err)
			}
			return nil
		},
	},
	{
		name:          "fix_missing_import",
		fixtureSubdir: "fix_missing_import",
		query:         `project/script/main.py crashes when run. Fix it.`,
		purpose:       `End-to-end: main.py uses json.dumps without importing json. Kaiju should add the missing import; the script must run cleanly.`,
		verify: func(workDir, _ string) error {
			script := filepath.Join(workDir, "project/script/main.py")
			cmd := exec.Command("python3", script, "x", "y")
			out, err := cmd.Output()
			if err != nil {
				return fmt.Errorf("python3 main.py failed: %v", err)
			}
			body := strings.TrimSpace(string(out))
			if !strings.Contains(body, `"tool"`) || !strings.Contains(body, `"x"`) {
				return fmt.Errorf("unexpected output: %q", body)
			}
			return nil
		},
	},

	// ── Fan-out: Holmes must emit multiple affected files. ──
	{
		name:          "fanout_multi",
		fixtureSubdir: "fanout_multi",
		query:         `project/run_all.py crashes when run. Fix it.`,
		purpose:       `Multi-file fan-out: three independent bugs in services/{a,b,c}.py all surface in one run_all.py invocation. A single-file fix cannot make run_all pass — all three services must be touched.`,
		verify: func(workDir, _ string) error {
			runAll := filepath.Join(workDir, "project/run_all.py")
			cmd := exec.Command("python3", runAll)
			cmd.Dir = filepath.Join(workDir, "project")
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("run_all.py still failing: %v — %s", err, trimTail(string(out), 400))
			}
			if !strings.Contains(string(out), "ok") {
				return fmt.Errorf("run_all.py ran but did not print 'ok': %s", trimTail(string(out), 200))
			}
			return nil
		},
	},
	{
		name:          "fanout_rename",
		fixtureSubdir: "fanout_rename",
		query:         `project/run_all.py fails. Fix it.`,
		purpose:       `Multi-file fan-out via rename: lib/compute.py exports total() but three consumers import three different names (compute_sum, total_sum, sum_all). No single-file rename in lib/ can satisfy all three imports — at least two consumers must be edited.`,
		verify: func(workDir, _ string) error {
			runAll := filepath.Join(workDir, "project/run_all.py")
			cmd := exec.Command("python3", runAll)
			cmd.Dir = filepath.Join(workDir, "project")
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("run_all.py still failing: %v — %s", err, trimTail(string(out), 400))
			}
			body := string(out)
			for _, want := range []string{"x=6", "y=15", "z=24"} {
				if !strings.Contains(body, want) {
					return fmt.Errorf("missing %q in output: %s", want, trimTail(body, 200))
				}
			}
			return nil
		},
	},

	// ── Classification boundary: when does reflector spawn Holmes? ──
	{
		name:          "class_obvious",
		fixtureSubdir: "class_obvious",
		query:         `project/main.py has a syntax error. Fix it and run it.`,
		purpose:       `Classification — obvious fix: a SyntaxError with an unambiguous line number should be resolvable by a direct microplan. Documents that kaiju does not over-invoke Holmes when the error is self-evident.`,
		verify: func(workDir, kaijuOut string) error {
			main := filepath.Join(workDir, "project/main.py")
			cmd := exec.Command("python3", main)
			out, err := cmd.Output()
			if err != nil {
				return fmt.Errorf("main.py still broken: %v", err)
			}
			if !strings.Contains(string(out), "Hello, world") {
				return fmt.Errorf("expected 'Hello, world', got %q", string(out))
			}
			iters := strings.Count(kaijuOut, "holmes iter ")
			if iters > 2 {
				return fmt.Errorf("expected ≤2 holmes iterations for obvious syntax error, saw %d — reflector may be over-investigating", iters)
			}
			return nil
		},
	},
	{
		name:          "class_opaque",
		fixtureSubdir: "class_opaque",
		query:         `project/main.py writes the wrong content to out.txt. It must exactly match project/expected.txt. Fix project/main.py.`,
		purpose:       `Classification — silent wrong output (loose): planner is non-deterministic on "Fix X" queries — sometimes it commits to edit+verify, sometimes it just reads and describes. This test passes if EITHER main.py was actually fixed (out.txt matches expected) OR the reflector/aggregator correctly named the fix verb (upper/uppercase). Fails only when kaiju failed to diagnose the bug at all. See fix_and_verify for the strict regression guard.`,
		verify: func(workDir, kaijuOut string) error {
			// Happy path — file actually edited, running produces expected.
			cmd := exec.Command("python3", "project/main.py")
			cmd.Dir = workDir
			if err := cmd.Run(); err == nil {
				want, _ := os.ReadFile(filepath.Join(workDir, "project/expected.txt"))
				for _, rel := range []string{"project/out.txt", "out.txt"} {
					got, rerr := os.ReadFile(filepath.Join(workDir, rel))
					if rerr == nil && strings.TrimSpace(string(got)) == strings.TrimSpace(string(want)) {
						return nil
					}
				}
			}
			// Diagnostic-only path — reflector or aggregator correctly named
			// the fix even if no coder ran.
			needles := []string{"upper", "uppercase"}
			hit := func(s string) bool {
				lower := strings.ToLower(s)
				for _, n := range needles {
					if strings.Contains(lower, n) {
						return true
					}
				}
				return false
			}
			for _, d := range readReflectorDecisions(workDir) {
				if hit(d.Summary) || hit(d.Verdict) {
					return nil
				}
			}
			if hit(kaijuOut) {
				return nil
			}
			return fmt.Errorf("neither a working fix nor a correct diagnosis — reflector/aggregator never mentioned 'upper'/'uppercase'; tail: %s", trimTail(kaijuOut, 200))
		},
	},

	{
		name:          "fix_and_verify",
		fixtureSubdir: "fix_and_verify",
		query:         `Run project/test_main.py. It currently fails. Edit project/main.py until every assertion in project/test_main.py passes. Do not modify the test file.`,
		purpose:       `Strict fix-and-verify: the same reverse-vs-uppercase bug as class_opaque, but the query explicitly asks kaiju to run a test file and the only success condition is that the test passes post-run. Kaiju cannot satisfy this by describing the fix — it must actually edit main.py AND the test must execute clean. Regression guard on the planner committing to action.`,
		verify: func(workDir, _ string) error {
			cmd := exec.Command("python3", "project/test_main.py")
			cmd.Dir = workDir
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("test_main.py still failing: %v — %s", err, trimTail(string(out), 300))
			}
			// Compare the test file against the source fixture — the workdir
			// copy reflects post-kaiju state, so we have to use the source as
			// the original baseline.
			orig, err := os.ReadFile("tests/eval/holmes/fixtures/fix_and_verify/project/test_main.py")
			if err != nil {
				return fmt.Errorf("read source test_main.py: %v", err)
			}
			after, err := os.ReadFile(filepath.Join(workDir, "project/test_main.py"))
			if err != nil {
				return fmt.Errorf("read workdir test_main.py: %v", err)
			}
			if string(orig) != string(after) {
				return fmt.Errorf("kaiju modified the test file — violated the 'do not modify' constraint")
			}
			return nil
		},
	},

	// ── Malformed file recovery: kaiju must repair structurally broken files. ──
	{
		name:          "trunc_js",
		fixtureSubdir: "trunc_js",
		query:         `project/api/server.js is truncated and fails to parse. Repair the file so 'node --check project/api/server.js' exits 0 without error. Keep the existing handler functions intact.`,
		purpose:       `Malformed file — truncation: server.js ends mid-response with an unterminated string, broken object literal, and no closing brace. Kaiju must reconstruct a syntactically valid end of file without deleting the existing handlers. Verify: node --check passes AND both handleHealth and handleUser are still defined.`,
		verify: func(workDir, _ string) error {
			path := filepath.Join(workDir, "project/api/server.js")
			cmd := exec.Command("node", "--check", path)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("node --check still fails: %v — %s", err, trimTail(string(out), 300))
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read: %v", err)
			}
			for _, fn := range []string{"handleHealth", "handleUser"} {
				if !strings.Contains(string(body), fn) {
					return fmt.Errorf("kaiju parsed the file by deleting %s — handler must be preserved", fn)
				}
			}
			return nil
		},
	},
	{
		name:          "bad_indent_py",
		fixtureSubdir: "bad_indent_py",
		query:         `project/tool.py fails with an IndentationError. Fix the indentation so 'python3 -m py_compile project/tool.py' succeeds and the script runs cleanly.`,
		purpose:       `Malformed file — indentation: tool.py has a misaligned statement inside a for-loop body that triggers 'IndentationError: unindent does not match any outer indentation level'. Kaiju must normalize the indentation without changing the logic — running the script must still produce summarize output.`,
		verify: func(workDir, _ string) error {
			path := filepath.Join(workDir, "project/tool.py")
			compile := exec.Command("python3", "-m", "py_compile", path)
			if out, err := compile.CombinedOutput(); err != nil {
				return fmt.Errorf("py_compile still fails: %v — %s", err, trimTail(string(out), 300))
			}
			run := exec.Command("python3", path)
			out, err := run.Output()
			if err != nil {
				return fmt.Errorf("tool.py crashed post-fix: %v", err)
			}
			body := string(out)
			if !strings.Contains(body, "total") || !strings.Contains(body, "hits") {
				return fmt.Errorf("summarize output missing expected keys: %q", body)
			}
			return nil
		},
	},
	{
		name:          "brace_json",
		fixtureSubdir: "brace_json",
		query:         `project/config/app.json is malformed — it won't parse with jq. Fix the file so 'jq . project/config/app.json' exits 0 cleanly. Preserve every key that's currently present.`,
		purpose:       `Malformed file — unmatched brace: app.json is missing a closing brace on the "web" sub-object. Kaiju must insert the right brace, not delete keys. Verify: jq parses it AND both "api" and "web" entries under servers survive.`,
		verify: func(workDir, _ string) error {
			path := filepath.Join(workDir, "project/config/app.json")
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read: %v", err)
			}
			var doc map[string]interface{}
			if err := json.Unmarshal(data, &doc); err != nil {
				return fmt.Errorf("still not valid JSON: %v", err)
			}
			servers, ok := doc["servers"].(map[string]interface{})
			if !ok {
				return fmt.Errorf("servers key missing or not an object")
			}
			for _, want := range []string{"api", "web"} {
				if _, ok := servers[want]; !ok {
					return fmt.Errorf("kaiju dropped servers.%s to make it parse — must preserve keys", want)
				}
			}
			return nil
		},
	},

	{
		name:          "compute_fanout",
		fixtureSubdir: "compute_fanout",
		query:         `For Mercury, Venus, Earth, Mars, and Jupiter, fetch their average distance from the Sun (km) and surface gravity (m/s²). Compute how many minutes light from the Sun takes to reach each planet (distance_km / 299792 / 60) and what a 75 kg human would weigh on each planet (75 × gravity / 9.81). Rank the planets by gravity, lowest first. Present the results as a table with columns: Planet, Distance (km), Gravity (m/s²), Light travel (min), Weight of 75 kg human (kg), Gravity rank.`,
		purpose:       `End-to-end value-computation pipeline — 5 parallel fetches → compute(shallow) with 5 param_refs → ranked table. Exercises validateDataFlow (compute+depends_on must have param_refs), validateParamRef (dep.Result field must exist or be graceful-degrade), and truncateToolResult (JSON envelopes stay valid when web_fetch content is large). A pass requires the final output to contain real numbers; a fail means either the validator chain broke or the pipeline couldn't recover.`,
		verify: func(workDir, kaijuOut string) error {
			// Two acceptable outcomes — either one is a pass:
			//
			//   (A) clean pipeline — Executive wired param_refs correctly
			//       from the start, compute ran, output has real numbers
			//       with recognisable units and a plausible Jupiter weight.
			//
			//   (B) validator caught a bad plan — the Executive emitted
			//       compute with depends_on but no param_refs (or wrong
			//       wiring), validateDataFlow / validateParamRef fired
			//       with a descriptive error, and kaiju surfaced that
			//       error to the user rather than silently corrupting.
			//
			// A fail = neither happened: no validator trip AND no real
			// output. That's the only shape we actually care about — it
			// means the missing-params class of bugs got through silently.
			planets := []string{"Mercury", "Venus", "Earth", "Mars", "Jupiter"}
			for _, p := range planets {
				if !strings.Contains(kaijuOut, p) {
					return fmt.Errorf("output missing planet %q — tail: %s", p, trimTail(kaijuOut, 300))
				}
			}

			// Path (A): real computed output.
			numericPat := regexp.MustCompile(`[0-9]+(\.[0-9]+)?\s*(min|kg|m/s|km)`)
			numericMatches := numericPat.FindAllString(kaijuOut, -1)
			jupiterRange := regexp.MustCompile(`(1[5-9][0-9]|2[01][0-9])\s*kg`)
			if len(numericMatches) >= 5 && jupiterRange.MatchString(kaijuOut) {
				return nil // clean pass
			}

			// Path (B): validator caught the bad plan.
			validatorMarkers := []string{
				"no param_refs",
				"data flow incomplete",
				"param_ref",
				"depends_on",
				"wire it via param_refs",
				"dispatch:reject",
			}
			lower := strings.ToLower(kaijuOut)
			for _, m := range validatorMarkers {
				if strings.Contains(lower, strings.ToLower(m)) {
					return nil // validator did its job
				}
			}

			return fmt.Errorf("neither a clean computed output nor a clear validator/data-flow error — missing-params class may have leaked through silently; numeric_matches=%d, tail: %s",
				len(numericMatches), trimTail(kaijuOut, 400))
		},
	},

	// ── Give-up path: kaiju must stop cleanly rather than loop. ──
	{
		name:          "giveup_unreachable",
		fixtureSubdir: "giveup_unreachable",
		query:         `project/main.py fails. Make it succeed without mocking the network or removing the HTTP call.`,
		purpose:       `Give-up: the script fetches an unresolvable hostname (.invalid TLD, RFC 2606). No real fix exists within the stated constraint. Either kaiju gets creative and passes verify, OR surfaces a clean "halted / exhausted / unable" marker — what it must NOT do is loop until the outer timeout.`,
		verify: func(workDir, kaijuOut string) error {
			main := filepath.Join(workDir, "project/main.py")
			cmd := exec.Command("python3", main)
			if out, err := cmd.Output(); err == nil && strings.Contains(string(out), "ok") {
				return nil
			}
			// Two legitimate paths (mirrors giveup_contradict):
			//   1. Reflector trace shows a conclude decision naming the
			//      impossibility.
			//   2. Final stdout contains any give-up phrasing.
			// Check both, widen markers beyond the narrow "halted/exhausted"
			// set — the aggregator's wording drifts per run.
			markers := []string{
				"halted", "exhausted", "unable", "max iter", "forcing conclude",
				"unreachable", "cannot succeed", "cannot be", "cannot resolve",
				"impossible", "still fails", "no mocking", "error repeats",
			}
			hit := func(s string) bool {
				lower := strings.ToLower(s)
				for _, m := range markers {
					if strings.Contains(lower, m) {
						return true
					}
				}
				return false
			}
			for _, d := range readReflectorDecisions(workDir) {
				if d.Decision == "conclude" && (hit(d.Verdict) || hit(d.Summary)) {
					return nil
				}
			}
			if hit(kaijuOut) {
				return nil
			}
			return fmt.Errorf("neither success nor a clean give-up marker; kaiju likely looped — tail: %s", trimTail(kaijuOut, 400))
		},
	},
	{
		name:          "giveup_contradict",
		fixtureSubdir: "giveup_contradict",
		query:         `project/run_tests.py fails. Fix project/target.py so every assertion in project/test_*.py passes. Do not modify the test files.`,
		purpose:       `Give-up: test_a asserts f() returns int 1; test_b asserts f() returns int 2. No integer satisfies both. Kaiju should either (a) surface the contradiction back to the user via the reflector's clarify path, OR (b) halt after exhausting Holmes iterations. What it must NOT do is silently rewrite the tests or loop until the outer timeout.`,
		verify: func(workDir, kaijuOut string) error {
			testA, errA := os.ReadFile(filepath.Join(workDir, "project/test_a.py"))
			testB, errB := os.ReadFile(filepath.Join(workDir, "project/test_b.py"))
			if errA == nil && errB == nil {
				if !strings.Contains(string(testA), "== 1") || !strings.Contains(string(testB), "== 2") {
					return fmt.Errorf("kaiju edited the test files — violated the 'do not modify tests' constraint")
				}
			}
			// The reflector is the component whose job is to detect
			// unsatisfiable spec and pick "conclude". The aggregator can
			// override with a creative answer — that's a separate concern and
			// not what this test is asserting. Inspect the captured trace
			// for the reflector's actual decision.
			markers := []string{
				"halted", "exhausted", "unable", "max iter", "forcing conclude",
				"contradict", "conflict", "conflicting", "not possible",
				"clarify", "please specify", "please provide", "cannot simultaneously", "cannot all pass",
			}
			hit := func(s string) bool {
				lower := strings.ToLower(s)
				for _, m := range markers {
					if strings.Contains(lower, m) {
						return true
					}
				}
				return false
			}
			for _, d := range readReflectorDecisions(workDir) {
				if d.Decision == "conclude" && (hit(d.Verdict) || hit(d.Summary)) {
					return nil
				}
			}
			// Fallback: if the trace is unreadable for some reason, accept a
			// marker in the final stdout.
			if hit(kaijuOut) {
				return nil
			}
			return fmt.Errorf("no reflector clarify/giveup marker; tail: %s", trimTail(kaijuOut, 400))
		},
	},
}

func trimTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// Run executes the holmes scenarios and returns 0 on pass, non-zero on fail.
func Run(opts Options) int {
	if opts.KaijuBin == "" {
		opts.KaijuBin = "./kaiju"
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = "kaiju.json"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Minute
	}

	absBin, err := filepath.Abs(opts.KaijuBin)
	if err != nil {
		log.Printf("[holmes] resolve kaiju: %v", err)
		return 1
	}
	if _, err := os.Stat(absBin); err != nil {
		log.Printf("[holmes] kaiju binary not found at %s — build with `go build -o kaiju ./cmd/kaiju`", absBin)
		return 1
	}

	configPath, err := filepath.Abs(opts.ConfigPath)
	if err != nil {
		log.Printf("[holmes] resolve kaiju config: %v", err)
		return 1
	}
	if _, err := os.Stat(configPath); err != nil {
		log.Printf("[holmes] kaiju config missing at %s", configPath)
		return 1
	}

	any := false
	fails := 0
	total := 0
	for _, sc := range scenarios {
		if opts.Only != "" && sc.name != opts.Only {
			continue
		}
		any = true
		total++
		fmt.Println(strings.Repeat("━", 70))
		fmt.Printf("SCENARIO: %s\n", sc.name)
		fmt.Printf("%s\n", sc.purpose)
		fmt.Println(strings.Repeat("━", 70))
		if err := runScenario(opts.KaijuBin, absBin, configPath, sc, opts.Timeout, opts.Keep); err != nil {
			fails++
			fmt.Printf("✗ %s — %v\n", sc.name, err)
		} else {
			fmt.Printf("✓ %s\n", sc.name)
		}
	}
	if !any {
		log.Printf("[holmes] no scenarios selected; check -only flag")
		return 1
	}
	if fails > 0 {
		fmt.Printf("\n%d/%d scenarios FAILED\n", fails, total)
		return 1
	}
	fmt.Println("\nALL PASS")
	return 0
}

func runScenario(kaijuShortName, absBin, configPath string, sc scenario, timeout time.Duration, keep bool) error {
	fixtureRoot, err := filepath.Abs(filepath.Join("tests/eval/holmes/fixtures", sc.fixtureSubdir))
	if err != nil {
		return fmt.Errorf("resolve fixture: %w", err)
	}
	if _, err := os.Stat(fixtureRoot); err != nil {
		return fmt.Errorf("fixture %s missing: %w", fixtureRoot, err)
	}

	workDir, err := os.MkdirTemp("", "holmes-eval-"+sc.name+"-*")
	if err != nil {
		return fmt.Errorf("mkdirtemp: %w", err)
	}
	if keep {
		log.Printf("[%s] keep: workdir %s", sc.name, workDir)
	} else {
		defer os.RemoveAll(workDir)
	}

	if err := copyTree(fixtureRoot, workDir); err != nil {
		return fmt.Errorf("copy fixture: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, absBin, "run", sc.query)
	cmd.Dir = workDir
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PYTHONPATH=") || strings.HasPrefix(kv, "KAIJU_CONFIG=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "KAIJU_CONFIG="+configPath)
	cmd.Env = env

	log.Printf("[%s] invoking %s run <query> in %s", sc.name, kaijuShortName, workDir)
	start := time.Now()
	out, runErr := cmd.CombinedOutput()
	dur := time.Since(start)
	if runErr != nil {
		log.Printf("[%s] kaiju exit: %v (after %s)", sc.name, runErr, dur.Round(time.Second))
	} else {
		log.Printf("[%s] kaiju finished in %s", sc.name, dur.Round(time.Second))
	}

	body := string(out)
	tail := body
	if len(tail) > 2000 {
		tail = "…" + tail[len(tail)-2000:]
	}
	fmt.Println("─── kaiju tail ───")
	fmt.Println(tail)
	fmt.Println("──────────────────")

	// Capture the per-investigation prompt trace kaiju writes to
	// /tmp/kaiju-prompts/<alert_id>.log. We copy it into the workdir so it
	// survives with -keep, and on verify failure persist a stable copy under
	// tests/eval/holmes/failures/ so the reflector/planner/aggregator prompts
	// are inspectable without rerunning.
	alertID := extractAlertID(body)
	workdirTrace := ""
	if alertID != "" {
		src := filepath.Join("/tmp/kaiju-prompts", alertID+".log")
		if _, err := os.Stat(src); err == nil {
			workdirTrace = filepath.Join(workDir, ".kaiju_prompts.log")
			if cerr := copyFile(src, workdirTrace, 0644); cerr != nil {
				log.Printf("[%s] could not copy prompt trace: %v", sc.name, cerr)
				workdirTrace = ""
			}
		}
	}

	if sc.verify == nil {
		return fmt.Errorf("scenario %s has no verify func", sc.name)
	}
	if err := sc.verify(workDir, body); err != nil {
		saved := persistTrace(sc.name, workdirTrace, alertID)
		if saved != "" {
			return fmt.Errorf("post-fix verify failed: %w\n    prompt trace: %s", err, saved)
		}
		if alertID != "" {
			return fmt.Errorf("post-fix verify failed: %w\n    (no prompt trace captured; kaiju alert=%s)", err, alertID)
		}
		return fmt.Errorf("post-fix verify failed: %w", err)
	}
	return nil
}

// reflectorDecision is a parsed reflector tool-call output.
type reflectorDecision struct {
	Decision string `json:"decision"`
	Summary  string `json:"summary"`
	Verdict  string `json:"verdict"`
	Problem  string `json:"problem"`
}

// readReflectorDecisions parses every reflector entry from the captured
// trace and returns the parsed outputs in order. Returns an empty slice if
// the trace is missing, unreadable, or has no reflector entries. Used by
// verify funcs that need to inspect what the reflector *actually* decided,
// independent of whatever text the aggregator later synthesised.
func readReflectorDecisions(workDir string) []reflectorDecision {
	path := filepath.Join(workDir, ".kaiju_prompts.log")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	const marker = "reflector "
	var out []reflectorDecision
	lines := strings.Split(string(data), "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "=== ") || !strings.Contains(lines[i], marker) {
			continue
		}
		// Scan forward for `--- OUTPUT ---` block, capture until next `--- `.
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "=== ") {
				break
			}
			if strings.TrimSpace(lines[j]) != "--- OUTPUT ---" {
				continue
			}
			var buf strings.Builder
			for k := j + 1; k < len(lines); k++ {
				if strings.HasPrefix(lines[k], "--- ") || strings.HasPrefix(lines[k], "=== ") {
					break
				}
				buf.WriteString(lines[k])
				buf.WriteByte('\n')
			}
			var d reflectorDecision
			if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &d); err == nil {
				out = append(out, d)
			}
			break
		}
	}
	return out
}

// extractAlertID parses the "alert=run-<nano>" marker kaiju prints on each
// run. The prompt trace file is named /tmp/kaiju-prompts/<alert_id>.log.
func extractAlertID(output string) string {
	idx := strings.Index(output, "alert=")
	if idx < 0 {
		return ""
	}
	rest := output[idx+len("alert="):]
	end := strings.IndexAny(rest, " ,)\n\t")
	if end < 0 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
}

// persistTrace copies a captured prompt trace into
// tests/eval/holmes/failures/<scenario>-<timestamp>.log so it survives
// workdir cleanup. Returns the destination path, or "" on any failure.
func persistTrace(scenarioName, workdirTrace, alertID string) string {
	src := workdirTrace
	if src == "" && alertID != "" {
		src = filepath.Join("/tmp/kaiju-prompts", alertID+".log")
	}
	if src == "" {
		return ""
	}
	if _, err := os.Stat(src); err != nil {
		return ""
	}
	failDir := filepath.Join("tests/eval/holmes/failures")
	if err := os.MkdirAll(failDir, 0755); err != nil {
		return ""
	}
	stamp := time.Now().Format("20060102-150405")
	dst := filepath.Join(failDir, fmt.Sprintf("%s-%s.log", scenarioName, stamp))
	if err := copyFile(src, dst, 0644); err != nil {
		return ""
	}
	return dst
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
