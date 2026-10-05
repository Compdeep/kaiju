package agent

import (
	"strings"
	"testing"
)

// Where deep mode works, and what it shows the coder about where it is.
//
// The architect was told to choose a root under project/<name>/ and that every
// path MUST use it, so deep mode put work inside kaiju's own state directory even
// when the request named a real repository. Shallow never did: workspace.Resolve
// returns an absolute path unchanged, which is how every edit_file on a real
// project has been working.

func cliAgent() *Agent {
	return &Agent{cfg: Config{PathConfig: PathConfig{Workspace: "/home/me/proj", CLIMode: true}}}
}

// CLI mode: the workspace IS the directory kaiju was started in, so a relative
// path is already where the user meant. PathConfig has claimed "no project/
// prefix" about CLI mode since it was written, and nothing implemented it —
// CLIMode was read in format.go and at bootstrap and nowhere else.
func TestProjectPrefix_CLIModeAddsNothing(t *testing.T) {
	a := cliAgent()
	for _, g := range []*Graph{nil, {ProjectRoot: "project/webapp"}, {ProjectRoot: "/elsewhere"}} {
		if got := a.projectPrefix(g, []string{"main.go"}); got != "" {
			t.Errorf("CLI mode prefixed %q; the workspace is already the cwd", got)
		}
	}
}

// An absolute root names a real place, so there is nothing to prepend. Without
// this the two concatenate into project/<session>//home/sites/uinloop/docs.
func TestProjectPrefix_AnAbsoluteRootIsNotPrefixed(t *testing.T) {
	a := webAgent()
	got := a.projectPrefix(&Graph{ProjectRoot: "/home/sites/uinloop/docs"}, nil)
	if got != "" {
		t.Errorf("prefix = %q, want none — the root is already a path", got)
	}
}

// Web mode with nothing absolute still needs somewhere for a relative path, and
// that is the case project/<name>/ was invented for.
func TestProjectPrefix_WebModeStillPlacesRelativeWork(t *testing.T) {
	got := webAgent().projectPrefix(&Graph{ProjectRoot: "project/webapp"}, nil)
	if !strings.HasPrefix(got, "project/") {
		t.Errorf("prefix = %q, want it under project/", got)
	}
}

// The tree the coders are shown comes from the project, not from kaiju's state
// directory. Scanning the workspace handed every deep coder SOUL.md, cookies.txt,
// de421.bsp and several dozen session UUIDs under the heading "Project Structure".
func TestProjectScanRoot_ComesFromTheWork(t *testing.T) {
	a := webAgent()

	cases := []struct {
		name  string
		root  string
		graph *Graph
		tasks []computeWorkItem
		want  string
	}{
		{
			name: "the architect's own absolute root",
			root: "/home/sites/uinloop/docs",
			want: "/home/sites/uinloop/docs",
		},
		{
			name:  "a root already on the graph",
			graph: &Graph{ProjectRoot: "/home/sites/uinloop"},
			want:  "/home/sites/uinloop",
		},
		{
			name:  "one task file gives its own directory",
			tasks: []computeWorkItem{{TaskFiles: flexStringArray{"/home/sites/uinloop/docs/src/backdrop.py"}}},
			want:  "/home/sites/uinloop/docs/src",
		},
		{
			name: "two task files give what holds both",
			tasks: []computeWorkItem{
				{TaskFiles: flexStringArray{"/home/sites/uinloop/docs/src/backdrop.py"}},
				{TaskFiles: flexStringArray{"/home/sites/uinloop/platform/web/index.html"}},
			},
			want: "/home/sites/uinloop",
		},
		{
			name:  "nothing absolute falls back to the workspace",
			tasks: []computeWorkItem{{TaskFiles: flexStringArray{"project/app/main.go"}}},
			want:  "/ws",
		},
		{
			name: "no tasks at all falls back",
			want: "/ws",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := a.projectScanRoot(c.graph, c.root, c.tasks); got != c.want {
				t.Errorf("scan root = %q, want %q", got, c.want)
			}
		})
	}
}

// Paths sharing nothing above / must not scan the filesystem root.
func TestProjectScanRoot_NothingInCommonDoesNotScanSlash(t *testing.T) {
	a := webAgent()
	got := a.projectScanRoot(nil, "", []computeWorkItem{
		{TaskFiles: flexStringArray{"/home/a/x.go"}},
		{TaskFiles: flexStringArray{"/srv/b/y.go"}},
	})
	if got == "/" {
		t.Fatal("the scan root is /, which would walk the whole filesystem")
	}
	if got != "/ws" {
		t.Errorf("scan root = %q, want the workspace fallback", got)
	}
}

// The architect is told to work where the work is, and only to invent a directory
// when there is nothing to work on.
func TestArchitectIsToldToWorkWhereTheWorkIs(t *testing.T) {
	src := readSource(t, "prompts.go")
	i := strings.Index(src, "## Paths")
	if i < 0 {
		t.Fatal("the Paths section is gone")
	}
	block := src[i : i+1200]

	if !strings.Contains(block, "real absolute path") {
		t.Errorf("the architect is not told to use real paths:\n%s", block[:500])
	}
	if !strings.Contains(block, "nothing to work on yet") {
		t.Error("the project/<name>/ case is not stated as the exception it is")
	}
	if strings.Contains(block, "All files, setup commands, task_files, execute commands, service workdirs, and validators MUST use this root") {
		t.Error("the old instruction mandating project/<name>/ for everything is still there")
	}
}
