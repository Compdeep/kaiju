package tools

/*
 * What kind of work each tool does: network, filesystem, compute, process or
 * info.
 *
 * Preflight names the kinds a run needs and the planner is told, in the
 * Required Tool Categories section of its prompt. Nothing narrows on these any
 * more: scopeToWork, which dropped tools of other kinds from the planner's
 * index, went on 2026-10-18 because it changed the index from run to run, and
 * an index that differs between runs is one the model host cannot cache. The
 * declarations stay: applications make them through toolapi.Categorised, and a
 * reader of a registry still wants to know.
 *
 * Declared generously. A tool named by either preflight or the ranking
 * survives, and the cost of a second category is that a tool is offered when it
 * was not needed — the cost of a missing one is that a plan cannot reach a tool
 * it needed. Those are not the same size, so anything genuinely spanning two
 * kinds claims both.
 *
 * The shell claims compute and no more, but it is pinned to the front of the
 * index by shellFirst whatever the ranking or this file say, because it is the
 * one tool that covers what no other does.
 */

func (t *Bash) Categories() []string    { return []string{"compute"} }
func (t *Git) Categories() []string     { return []string{"compute", "filesystem"} }
func (t *Service) Categories() []string { return []string{"compute", "process"} }

func (t *FileRead) Categories() []string      { return []string{"filesystem"} }
func (t *FileWrite) Categories() []string     { return []string{"filesystem"} }
func (t *FileList) Categories() []string      { return []string{"filesystem"} }
func (t *DiskUsage) Categories() []string     { return []string{"filesystem"} }
func (t *Archive) Categories() []string       { return []string{"filesystem"} }
func (t *OfficeExtract) Categories() []string { return []string{"filesystem"} }

func (t *ProcessList) Categories() []string { return []string{"process"} }
func (t *ProcessKill) Categories() []string { return []string{"process"} }

// Reaching the network is what these are for, whatever they do with what comes
// back.
func (t *WebSearch) Categories() []string   { return []string{"network"} }
func (t *WebFetch) Categories() []string    { return []string{"network"} }
func (t *WebResearch) Categories() []string { return []string{"network"} }

// NetInfo reads this machine's interfaces and can also check reachability, so
// it answers a question about the network and a question about the host.
func (t *NetInfo) Categories() []string { return []string{"network", "info"} }

func (t *Sysinfo) Categories() []string       { return []string{"info"} }
func (t *EnvList) Categories() []string       { return []string{"info"} }
func (t *Clipboard) Categories() []string     { return []string{"info"} }
func (t *MemoryStore) Categories() []string   { return []string{"info"} }
func (t *MemoryRecall) Categories() []string  { return []string{"info"} }
func (t *MemorySearch) Categories() []string  { return []string{"info"} }
func (t *MessageSearch) Categories() []string { return []string{"info"} }

func (t *PluginList) Categories() []string    { return []string{"info"} }
func (t *PluginInstall) Categories() []string { return []string{"info"} }
func (t *PanelPush) Categories() []string     { return []string{"info"} }
