package tools

import (
	"context"
	"testing"

	agenttools "github.com/Compdeep/kaiju/agent/tools"
	"github.com/Compdeep/kaiju/internal/config"
)

// Every tool this package registers returns its result as a ToolMessage. The
// dispatcher checks for this interface and nothing else, so a rename that
// leaves one unsatisfied is silent — it goes back to the string path, its
// output is capped at 4096 characters and byte-spliced, and its outcome stops
// reaching the coverage statement.
//
// The list is written out rather than discovered so that adding a tool and
// forgetting to type it fails here.
func allTools(t *testing.T) map[string]agenttools.Tool {
	t.Helper()
	ws := t.TempDir()
	return map[string]agenttools.Tool{
		"archive":        NewArchive(),
		"bash":           NewBash(""),
		"clipboard":      NewClipboard(),
		"disk_usage":     NewDiskUsage(),
		"env_list":       NewEnvList(),
		"file_list":      NewFileList(ws),
		"file_read":      NewFileRead(ws),
		"file_write":     NewFileWrite(ws),
		"git":            NewGit(),
		"memory_recall":  NewMemoryRecall(nil),
		"memory_search":  NewMemorySearch(nil),
		"memory_store":   NewMemoryStore(nil),
		"net_info":       NewNetInfo(),
		"office_extract": NewOfficeExtract(ws),
		"panel_push":     NewPanelPush(),
		"plugin_list":    NewPluginList(),
		"plugin_option":  NewPluginOption(&config.Config{}),
		"process_kill":   NewProcessKill(),
		"process_list":   NewProcessList(),
		"service":        NewService(ws),
		"sysinfo":        NewSysinfo(ws),
		"web_fetch":      NewWebFetch(),
		"web_search":     NewWebSearch(),
		// The variants main actually registers, which take configuration the
		// plain constructors do not. Missing these was how the first version of
		// this guard passed while three registered tools went unchecked.
		"plugin_enable": NewPluginEnable(agenttools.NewRegistry(), &config.Config{}, NewService(ws)),
		"web_research":  NewWebResearch(SearchConfig{}, nil),
	}
}

func TestEveryToolIsTyped(t *testing.T) {
	for name, tool := range allTools(t) {
		if _, ok := tool.(agenttools.TypedExecutor); !ok {
			t.Errorf("%s does not implement TypedExecutor — the dispatcher will take the string path", name)
		}
	}
}

// And every tool still satisfies the plain interface, which is what keeps
// callers outside the DAG working and is the reason Tool was left unchanged.
func TestEveryToolStillSatisfiesTool(t *testing.T) {
	for name, tool := range allTools(t) {
		if tool.Name() != name {
			t.Errorf("%s reports its name as %q", name, tool.Name())
		}
		if tool.Parameters() == nil {
			t.Errorf("%s declares no parameter schema", name)
		}
	}
}

// The adapter must not invent a result when the tool failed: an error carries
// no output. Checked on a tool that fails predictably.
func TestAdapterReturnsNothingOnError(t *testing.T) {
	out, err := NewFileRead(t.TempDir()).Execute(context.Background(), map[string]any{"path": "absent"})
	if err == nil {
		t.Fatal("want the read error")
	}
	if out != "" {
		t.Fatalf("an error must carry no result, got %q", out)
	}
}
