// Package pipeline is the pre-integration full-pipeline eval. It invokes
// kaiju end-to-end against a fixture project with a KNOWN multi-file bug
// and asserts the debug cycle fanned out and fixed ALL affected files.
//
// Not part of `go test ./...`. Invoked via the unified dispatcher at
// tests/eval/cmd.
package pipeline

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const query = `Get the backend at project/kaiju_saas/backend running so /health returns 200 on port 4000.`

// Options mirrors the shared eval CLI options relevant to this suite.
type Options struct {
	KaijuBin   string
	ConfigPath string
	Timeout    time.Duration
	Keep       bool
}

// Run executes the pipeline scenario and returns 0 on pass, non-zero on fail.
func Run(opts Options) int {
	if opts.KaijuBin == "" {
		opts.KaijuBin = "./kaiju"
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = "kaiju.json"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 6 * time.Minute
	}

	absBin, err := filepath.Abs(opts.KaijuBin)
	if err != nil {
		log.Printf("[pipeline] resolve kaiju binary: %v", err)
		return 1
	}
	if _, err := os.Stat(absBin); err != nil {
		log.Printf("[pipeline] kaiju binary not found at %s", absBin)
		return 1
	}

	fixtureRoot, err := filepath.Abs("tests/eval/pipeline/fixture")
	if err != nil {
		log.Printf("[pipeline] resolve fixture: %v", err)
		return 1
	}
	if _, err := os.Stat(fixtureRoot); err != nil {
		log.Printf("[pipeline] fixture root not found: %v", err)
		return 1
	}

	workDir, err := os.MkdirTemp("", "pipeline-eval-*")
	if err != nil {
		log.Printf("[pipeline] mkdirtemp: %v", err)
		return 1
	}
	if opts.Keep {
		log.Printf("[pipeline] keep: workdir %s", workDir)
	} else {
		defer os.RemoveAll(workDir)
	}

	if err := copyTree(fixtureRoot, workDir); err != nil {
		log.Printf("[pipeline] copy fixture: %v", err)
		return 1
	}
	log.Printf("[pipeline] workdir: %s", workDir)

	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()

	kaijuConfig, err := filepath.Abs(opts.ConfigPath)
	if err != nil {
		log.Printf("[pipeline] resolve kaiju config: %v", err)
		return 1
	}
	if _, err := os.Stat(kaijuConfig); err != nil {
		log.Printf("[pipeline] kaiju config not found at %s", kaijuConfig)
		return 1
	}

	cmd := exec.CommandContext(ctx, absBin, "run", query)
	cmd.Dir = workDir
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PYTHONPATH=") || strings.HasPrefix(kv, "KAIJU_CONFIG=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "KAIJU_CONFIG="+kaijuConfig)
	cmd.Env = env

	log.Printf("[pipeline] invoking: %s run <query>  (in %s)", absBin, workDir)
	start := time.Now()
	stdout, err := cmd.CombinedOutput()
	dur := time.Since(start)
	if err != nil {
		log.Printf("[pipeline] kaiju exit: %v (after %s)", err, dur.Round(time.Second))
	} else {
		log.Printf("[pipeline] kaiju finished in %s", dur.Round(time.Second))
	}
	if len(stdout) > 0 {
		tail := string(stdout)
		if len(tail) > 2000 {
			tail = "…" + tail[len(tail)-2000:]
		}
		fmt.Println("─── kaiju output (tail) ───")
		fmt.Println(tail)
		fmt.Println("─── end output ───")
	}

	return assertFanOut(workDir)
}

func assertFanOut(workDir string) int {
	routers := []string{
		"project/kaiju_saas/backend/src/routes/auth.js",
		"project/kaiju_saas/backend/src/routes/users.js",
		"project/kaiju_saas/backend/src/routes/payments.js",
	}
	serverPath := "project/kaiju_saas/backend/src/server.js"

	serverBody, err := readFile(workDir, serverPath)
	if err != nil {
		fmt.Printf("FAIL: server.js missing — %v\n", err)
		return 1
	}
	if !strings.Contains(serverBody, "authRouter") ||
		!strings.Contains(serverBody, "usersRouter") ||
		!strings.Contains(serverBody, "paymentsRouter") {
		fmt.Println("FAIL: server.js lost one of the three router bindings")
		return 1
	}

	defaultCount := 0
	namedCount := 0
	for _, p := range routers {
		body, err := readFile(workDir, p)
		if err != nil {
			fmt.Printf("FAIL: %s — %v\n", p, err)
			return 1
		}
		hasDefault := strings.Contains(body, "export default router")
		hasNamed := strings.Contains(body, "export { router }")
		switch {
		case hasDefault && !hasNamed:
			defaultCount++
		case hasNamed && !hasDefault:
			namedCount++
		default:
			fmt.Printf("  ✗ %s — ambiguous / broken exports (default=%v named=%v)\n", p, hasDefault, hasNamed)
		}
	}
	fmt.Printf("  routers: %d converted to default, %d still named\n", defaultCount, namedCount)

	allDefault := defaultCount == 3
	allNamed := namedCount == 3 &&
		strings.Contains(serverBody, "{ router as authRouter }") &&
		strings.Contains(serverBody, "{ router as usersRouter }") &&
		strings.Contains(serverBody, "{ router as paymentsRouter }")
	if allDefault {
		fmt.Println("PASS: all three routers converted to default exports — debugger fanned out.")
		return 0
	}
	if allNamed {
		fmt.Println("PASS: server.js converted to named imports for all three routers — debugger fanned out.")
		return 0
	}
	fmt.Println("FAIL: the fix was NOT fanned out across all three routers — likely only one was touched per debug cycle, classic loop.")
	return 1
}

func readFile(root, rel string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return "", err
	}
	return string(data), nil
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
