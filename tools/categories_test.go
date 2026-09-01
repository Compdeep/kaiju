package tools

import (
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The vocabulary preflight emits. A category outside it is dropped before
// scopeToWork sees it, so a tool declaring one is never narrowed in — it
// survives on the ranking's first thirty or not at all, and nothing says so.
var validCategories = map[string]bool{
	"network": true, "filesystem": true, "compute": true, "process": true, "info": true,
}

func TestBuiltinCategoriesAreOnesPreflightEmits(t *testing.T) {
	for _, tool := range categorised() {
		cats := toolapi.ToolCategories(tool)
		if len(cats) == 0 {
			t.Errorf("%s declares no category", tool.Name())
			continue
		}
		for _, c := range cats {
			if !validCategories[c] {
				t.Errorf("%s declares %q, which preflight never emits", tool.Name(), c)
			}
		}
	}
}

// The kinds have to land where a reader would expect, or the narrowing removes
// the wrong things. One assertion per kind, on the tool least ambiguously in it.
func TestKindsLandWhereTheyBelong(t *testing.T) {
	for name, want := range map[string]string{
		"bash":       "compute",
		"file_read":  "filesystem",
		"web_search": "network",
		"sysinfo":    "info",
	} {
		tool := find(name)
		if tool == nil {
			t.Errorf("%s is not categorised", name)
			continue
		}
		found := false
		for _, c := range toolapi.ToolCategories(tool) {
			if c == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not declare %q", name, want)
		}
	}
}

func find(name string) toolapi.Tool {
	for _, tool := range categorised() {
		if tool.Name() == name {
			return tool
		}
	}
	return nil
}

// One of each tool this file declares a category for. Nil dependencies: Name()
// and Categories() read none, and nothing here executes.
func categorised() []toolapi.Tool {
	return []toolapi.Tool{
		&Bash{}, &Git{}, &Service{},
		&FileRead{}, &FileWrite{}, &FileList{}, &DiskUsage{}, &Archive{}, &OfficeExtract{},
		&ProcessList{}, &ProcessKill{},
		&WebSearch{}, &WebFetch{}, &WebResearch{}, &NetInfo{},
		&Sysinfo{}, &EnvList{}, &Clipboard{},
		&MemoryStore{}, &MemoryRecall{}, &MemorySearch{}, &MessageSearch{},
		&PluginList{}, &PluginEnable{}, &PluginOption{}, &PanelPush{},
	}
}
