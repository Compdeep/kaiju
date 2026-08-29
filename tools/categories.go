package tools

import "github.com/Compdeep/kaiju/agent/toolapi"

// What kind of work each of these tools does.
//
// The mapping already existed — hardcoded as prose inside the planner's prompt,
// where only a model could act on it and nothing checked it against the
// registry. It named ten tools while the engine registered twenty-seven, and a
// tool renamed or removed would have gone on being advertised. Here it sits on
// the tool, so a tool that stops existing stops declaring, and the engine can
// use it to decide what a planner is shown.
//
// One purpose only: narrowing a registry too large to show whole. Not
// authorisation, not routing. See toolapi.Categorised.
//
// A tool absent from this file declares nothing, which means "cannot say" and
// never "matches nothing" — it stays visible either way.

const (
	catNetwork    = toolapi.CategoryNetwork
	catFilesystem = toolapi.CategoryFilesystem
	catCompute    = toolapi.CategoryCompute
	catProcess    = toolapi.CategoryProcess
	catInfo       = toolapi.CategoryInfo
)

func (t *WebFetch) Categories() []string    { return []string{catNetwork} }
func (t *WebSearch) Categories() []string   { return []string{catNetwork} }
func (t *WebResearch) Categories() []string { return []string{catNetwork} }

func (t *FileRead) Categories() []string  { return []string{catFilesystem} }
func (t *FileWrite) Categories() []string { return []string{catFilesystem} }
func (t *FileList) Categories() []string  { return []string{catFilesystem} }
func (t *Archive) Categories() []string   { return []string{catFilesystem} }

// The shell is every kind at once, which is why shellFirst pins it ahead of the
// ranking and why no narrowing may remove it. Declaring all five says the same
// thing to any caller that reads categories rather than knowing about shells.
func (t *Bash) Categories() []string {
	return []string{catCompute, catProcess, catFilesystem, catInfo, catNetwork}
}

func (t *ProcessList) Categories() []string { return []string{catProcess} }
func (t *ProcessKill) Categories() []string { return []string{catProcess} }
func (t *Service) Categories() []string     { return []string{catProcess} }

func (t *Sysinfo) Categories() []string   { return []string{catInfo} }
func (t *EnvList) Categories() []string   { return []string{catInfo} }
func (t *DiskUsage) Categories() []string { return []string{catInfo} }
func (t *NetInfo) Categories() []string   { return []string{catInfo, catNetwork} }

// Git reaches a remote as readily as it reads a working tree, so it is both.
func (t *Git) Categories() []string { return []string{catFilesystem, catNetwork} }

// Extraction reads a file and produces data from it.
func (t *OfficeExtract) Categories() []string { return []string{catFilesystem, catCompute} }
