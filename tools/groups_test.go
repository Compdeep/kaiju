package tools

import (
	"slices"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Every group a tool names is one preflight can pick, or the tool is never
// shown when its group is picked.
func TestGroups_EveryToolNamesKnownGroups(t *testing.T) {
	known := map[string]bool{}
	for _, g := range toolapi.Groups {
		known[g.Name] = true
	}
	for _, tool := range grouped() {
		groups := toolapi.ToolGroups(tool)
		if len(groups) == 0 {
			t.Errorf("%s names no group", tool.Name())
		}
		for _, g := range groups {
			if !known[g] {
				t.Errorf("%s names %q, which preflight cannot pick", tool.Name(), g)
			}
		}
	}
}

// The shell names no group, so it is shown on every run.
func TestGroups_ShellIsInEveryRun(t *testing.T) {
	if g := toolapi.ToolGroups(&Bash{}); g != nil {
		t.Errorf("bash names %v; it must name none", g)
	}
}

// One placement per group, on the tool a request in it cannot do without.
func TestGroups_ToolsLandWhereRequestsLookForThem(t *testing.T) {
	for _, c := range []struct {
		tool  toolapi.Tool
		group string
	}{
		{&WebSearch{}, toolapi.GroupWeb},
		{&BrowserClick{}, toolapi.GroupBrowser},
		{&Archive{}, toolapi.GroupFiles},
		{&OfficeExtract{}, toolapi.GroupDocuments},
		{&Git{}, toolapi.GroupCode},
		{&PanelPush{}, toolapi.GroupData},
		{&ProcessKill{}, toolapi.GroupOSProcess},
		{&Service{}, toolapi.GroupOSProcess},
		{&DiskUsage{}, toolapi.GroupOSInfo},
		{&MessageSearch{}, toolapi.GroupMemory},
		{&PluginInstall{}, toolapi.GroupKaiju},
	} {
		if !slices.Contains(toolapi.ToolGroups(c.tool), c.group) {
			t.Errorf("%s is not in %s", c.tool.Name(), c.group)
		}
	}
}

// One of each tool this package groups. Nil dependencies: Name() and Groups()
// read none, and nothing here executes.
func grouped() []toolapi.Tool {
	return []toolapi.Tool{
		&WebSearch{}, &WebFetch{}, &WebResearch{},
		&BrowserPair{}, &BrowserStatus{}, &BrowserTabs{}, &BrowserTabAction{}, &BrowserNavigate{},
		&BrowserRead{}, &BrowserScreenshot{}, &BrowserClick{}, &BrowserType{}, &BrowserKey{},
		&BrowserSelect{}, &BrowserScroll{}, &BrowserWait{},
		&FileRead{}, &FileWrite{}, &FileList{}, &Archive{}, &OfficeExtract{}, &Git{},
		&ProcessList{}, &ProcessKill{}, &Service{},
		&Sysinfo{}, &DiskUsage{}, &EnvList{}, &NetInfo{}, &Clipboard{},
		&MemoryStore{}, &MemoryRecall{}, &MemorySearch{}, &MessageSearch{},
		&PluginList{}, &PluginInstall{}, &PanelPush{},
	}
}
