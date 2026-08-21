package agent

import "testing"

// Only the architect sets ProjectRoot, so every run that never planned deeply
// fell through to a bare "project/" shared by every conversation on the machine
// — which is how one run's coder came to edit a script another conversation had
// left there, blind, and fail on content it could not have matched.
func TestProjectPrefix_Precedence(t *testing.T) {
	cases := []struct {
		name      string
		graph     *Graph
		taskFiles []string
		want      string
	}{
		{
			name:  "an architect-named root wins",
			graph: &Graph{SessionID: "s-1", ProjectRoot: "project/webapp"},
			want:  "project/webapp/",
		},
		{
			name:  "a named root already ending in a slash is left alone",
			graph: &Graph{SessionID: "s-1", ProjectRoot: "project/webapp/"},
			want:  "project/webapp/",
		},
		{
			name:      "an explicit path in task_files still wins over the thread",
			graph:     &Graph{SessionID: "s-1"},
			taskFiles: []string{"project/kaiju_webapp/main.go", "project/kaiju_webapp/go.mod"},
			want:      "project/kaiju_webapp/",
		},
		{
			name:  "otherwise the conversation gets its own directory",
			graph: &Graph{SessionID: "089ce07b-c601-4a1b-8d23-f7d125f47ba5"},
			want:  "project/089ce07b-c601-4a1b-8d23-f7d125f47ba5/",
		},
		{
			name:  "no session leaves the shared directory as it was",
			graph: &Graph{},
			want:  "project/",
		},
		{
			name:  "no graph at all still resolves",
			graph: nil,
			want:  "project/",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := projectPrefix(c.graph, c.taskFiles); got != c.want {
				t.Fatalf("projectPrefix = %q, want %q", got, c.want)
			}
		})
	}
}

// A session id is not a path, so anything that could act like one keeps the
// shared directory rather than being spliced into a path.
func TestThreadDir_RejectsAnythingThatIsNotAPlainName(t *testing.T) {
	for _, bad := range []string{"", "   ", "../escape", "a/b", "with space", "semi;colon", ".."} {
		if got := threadDir(bad); got != "" {
			t.Fatalf("threadDir(%q) = %q, want empty", bad, got)
		}
	}
	for _, ok := range []string{"089ce07b-c601-4a1b-8d23-f7d125f47ba5", "s_1", "ABC123"} {
		if got := threadDir(ok); got != ok {
			t.Fatalf("threadDir(%q) = %q, want it unchanged", ok, got)
		}
	}
}
