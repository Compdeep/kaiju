package workspace

import (
	"strings"
	"testing"
)

func TestSafeJoin(t *testing.T) {
	ws := "/home/user/kaiju"
	tests := []struct {
		rel     string
		wantErr string // substring; "" = success
	}{
		{"project/myapp/server.js", ""},
		{"media/video.mp4", ""},
		{"canvas/chart.png", ""},
		{"blueprints/plan.md", ""},
		{"project/../cmd/kaiju/main.go", "not in allowed zones"},
		{"cmd/kaiju/main.go", "not in allowed zones"},
		{"internal/agent/compute.go", "not in allowed zones"},
		{".kaiju/sessions/x", "not in allowed zones"},
		{"compute.py", "not in allowed zones"},
		{"/etc/passwd", "absolute paths"},
		{"../../../etc/passwd", "escapes workspace"},
		{"project", ""},
		{"", "not in allowed zones"},
	}
	for _, tc := range tests {
		t.Run(tc.rel, func(t *testing.T) {
			got, err := SafeJoin(ws, tc.rel)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error for %q: %v", tc.rel, err)
				}
				if !strings.HasPrefix(got, ws) {
					t.Fatalf("path %q not under workspace: got %q", tc.rel, got)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error for %q, got path %q", tc.rel, got)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}
