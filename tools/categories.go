package tools

/*
 * What kind of work each tool does, in the vocabulary preflight speaks.
 *
 * preflightCategories is network, filesystem, compute, process, info, and
 * scopeToWork narrows the planner's tool index to the kinds a run was
 * classified as needing. It can only do that for tools that say what kind they
 * are: a tool declaring nothing is kept, and when NOTHING declares, the whole
 * narrowing returns the list untouched. So an application with a large registry
 * and no categories shows every tool on every call, which is the state this
 * file ends.
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

func (t *PluginList) Categories() []string   { return []string{"info"} }
func (t *PluginEnable) Categories() []string { return []string{"info"} }
func (t *PluginOption) Categories() []string { return []string{"info"} }
func (t *PanelPush) Categories() []string    { return []string{"info"} }
