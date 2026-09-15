package agent

import "testing"

// The conversation is the root, and the architect names a directory inside it.
//
// It used to be the other way round: the architect's name came first and the
// conversation's directory was the fallback for runs that never planned deeply.
// So an architect returning "project/webapp" put that at the top, beside the
// per-conversation directories and belonging to none of them, and two
// conversations that produced the same name wrote to the same files — the
// failure the per-conversation directory exists to stop, one level up from where
// it was stopped.
func TestProjectPrefix_TheConversationIsTheRoot(t *testing.T) {
	cases := []struct {
		name      string
		graph     *Graph
		taskFiles []string
		want      string
	}{
		{
			name:  "the architect names a directory inside the conversation's",
			graph: &Graph{SessionID: "s-1", ProjectRoot: "project/webapp"},
			want:  "project/s-1/webapp/",
		},
		{
			name:  "a trailing slash on the named root changes nothing",
			graph: &Graph{SessionID: "s-1", ProjectRoot: "project/webapp/"},
			want:  "project/s-1/webapp/",
		},
		{
			name:  "a bare name from the architect means the same directory",
			graph: &Graph{SessionID: "s-1", ProjectRoot: "webapp"},
			want:  "project/s-1/webapp/",
		},
		{
			// Once it has been shown paths under this layout the architect
			// starts writing them back whole. Splicing the base on again would
			// repeat the conversation's directory inside itself.
			name:  "a root that already carries the conversation is not doubled",
			graph: &Graph{SessionID: "s-1", ProjectRoot: "project/s-1/webapp"},
			want:  "project/s-1/webapp/",
		},
		{
			name:  "a root naming nothing beyond the conversation is the conversation",
			graph: &Graph{SessionID: "s-1", ProjectRoot: "project/s-1"},
			want:  "project/s-1/",
		},
		{
			name:      "task_files name the project beneath the conversation",
			graph:     &Graph{SessionID: "s-1"},
			taskFiles: []string{"project/s-1/kaiju_webapp/main.go", "project/s-1/kaiju_webapp/go.mod"},
			want:      "project/s-1/kaiju_webapp/",
		},
		{
			name:      "task_files that do not agree on one project name none",
			graph:     &Graph{SessionID: "s-1"},
			taskFiles: []string{"project/s-1/one/main.go", "project/s-1/two/go.mod"},
			want:      "project/s-1/",
		},
		{
			name:      "task_files written under the old layout name no project here",
			graph:     &Graph{SessionID: "s-1"},
			taskFiles: []string{"project/kaiju_webapp/main.go"},
			want:      "project/s-1/",
		},
		{
			name:  "with nothing named, the conversation's directory is the root",
			graph: &Graph{SessionID: "089ce07b-c601-4a1b-8d23-f7d125f47ba5"},
			want:  "project/089ce07b-c601-4a1b-8d23-f7d125f47ba5/",
		},
		{
			// There is no conversation to scope to, so this is the one case that
			// still shares. A named root applies as it always did.
			name:  "no session leaves the shared directory as it was",
			graph: &Graph{},
			want:  "project/",
		},
		{
			name:  "no session, but a named root still names it",
			graph: &Graph{ProjectRoot: "project/webapp"},
			want:  "project/webapp/",
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
