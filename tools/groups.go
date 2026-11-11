package tools

import "github.com/Compdeep/kaiju/agent/toolapi"

/*
 * The groups each tool belongs to (see toolapi.Groups).
 *
 * Preflight picks the groups a request needs and the planner is shown only
 * their tools. A tool goes in every group whose requests reach for it: a
 * missing group hides a tool the plan needed, a spare one costs a line of
 * index. The shell names none, so it is shown on every run.
 */

func (*WebSearch) Groups() []string   { return []string{toolapi.GroupWeb} }
func (*WebFetch) Groups() []string    { return []string{toolapi.GroupWeb} }
func (*WebResearch) Groups() []string { return []string{toolapi.GroupWeb} }

func (*BrowserPair) Groups() []string       { return []string{toolapi.GroupBrowser} }
func (*BrowserStatus) Groups() []string     { return []string{toolapi.GroupBrowser} }
func (*BrowserTabs) Groups() []string       { return []string{toolapi.GroupBrowser} }
func (*BrowserTabAction) Groups() []string  { return []string{toolapi.GroupBrowser} }
func (*BrowserNavigate) Groups() []string   { return []string{toolapi.GroupBrowser} }
func (*BrowserRead) Groups() []string       { return []string{toolapi.GroupBrowser} }
func (*BrowserScreenshot) Groups() []string { return []string{toolapi.GroupBrowser} }
func (*BrowserClick) Groups() []string      { return []string{toolapi.GroupBrowser} }
func (*BrowserType) Groups() []string       { return []string{toolapi.GroupBrowser} }
func (*BrowserKey) Groups() []string        { return []string{toolapi.GroupBrowser} }
func (*BrowserSelect) Groups() []string     { return []string{toolapi.GroupBrowser} }
func (*BrowserScroll) Groups() []string     { return []string{toolapi.GroupBrowser} }
func (*BrowserWait) Groups() []string       { return []string{toolapi.GroupBrowser} }

func (*FileRead) Groups() []string {
	return []string{toolapi.GroupFiles, toolapi.GroupDocuments, toolapi.GroupCode, toolapi.GroupData}
}
func (*FileList) Groups() []string {
	return []string{toolapi.GroupFiles, toolapi.GroupDocuments, toolapi.GroupCode}
}
func (*FileWrite) Groups() []string {
	return []string{toolapi.GroupFiles, toolapi.GroupCode, toolapi.GroupData}
}
func (*Archive) Groups() []string       { return []string{toolapi.GroupFiles} }
func (*OfficeExtract) Groups() []string { return []string{toolapi.GroupDocuments} }
func (*Git) Groups() []string           { return []string{toolapi.GroupCode} }

func (*ProcessList) Groups() []string { return []string{toolapi.GroupOSProcess, toolapi.GroupOSInfo} }
func (*ProcessKill) Groups() []string { return []string{toolapi.GroupOSProcess} }
func (*Service) Groups() []string     { return []string{toolapi.GroupOSProcess, toolapi.GroupCode} }

func (*Sysinfo) Groups() []string   { return []string{toolapi.GroupOSInfo} }
func (*DiskUsage) Groups() []string { return []string{toolapi.GroupOSInfo} }
func (*EnvList) Groups() []string   { return []string{toolapi.GroupOSInfo} }
func (*NetInfo) Groups() []string   { return []string{toolapi.GroupOSInfo} }
func (*Clipboard) Groups() []string { return []string{toolapi.GroupOSInfo} }

func (*MemoryStore) Groups() []string   { return []string{toolapi.GroupMemory} }
func (*MemoryRecall) Groups() []string  { return []string{toolapi.GroupMemory} }
func (*MemorySearch) Groups() []string  { return []string{toolapi.GroupMemory} }
func (*MessageSearch) Groups() []string { return []string{toolapi.GroupMemory} }

func (*PluginList) Groups() []string    { return []string{toolapi.GroupKaiju} }
func (*PluginInstall) Groups() []string { return []string{toolapi.GroupKaiju} }
func (*PanelPush) Groups() []string     { return []string{toolapi.GroupKaiju, toolapi.GroupData} }
