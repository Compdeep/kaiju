package skillmd

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// CheckGating verifies a skill can run on this platform.
// Returns a human-readable reason on failure, nil on success.
func CheckGating(meta *SkillMetadata) error {
	if meta == nil {
		return nil
	}

	// OS check
	if len(meta.OS) > 0 {
		found := false
		for _, o := range meta.OS {
			if strings.EqualFold(o, runtime.GOOS) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("skill requires OS %v, running on %s", meta.OS, runtime.GOOS)
		}
	}

	if meta.Requires == nil {
		return nil
	}

	// Binary check
	for _, bin := range meta.Requires.Bins {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("required binary %q not found in PATH", bin)
		}
	}

	// Env check
	for _, env := range meta.Requires.Env {
		if os.Getenv(env) == "" {
			return fmt.Errorf("required environment variable %q not set", env)
		}
	}

	return nil
}
