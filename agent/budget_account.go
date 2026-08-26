package agent

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Counting what the caps actually cut.
//
// Four numbers decide how much of a run reaches a model, and until they were
// collected in budgets.go nobody could read them together. Reading them is not
// the same as knowing whether they bind: a cap that never fires costs nothing
// however small it looks, and one that fires on every step is the reason an
// answer was thin — and neither leaves a trace beyond a marker buried in a
// prompt nobody reads.
//
// So each cut is counted. Then "was this run starved?" is a question with an
// answer, and "did raising the evidence cap help?" can be settled by running
// the same query twice rather than by argument.
//
// Scheduler workers cut on their own goroutines, so the tally is locked: a
// per-run total that only sees one worker is worse than none.

// capCut is one cap's tally for a run.
type capCut struct {
	Hits    int // how many times it cut
	Dropped int // how much was removed, summed, in the cap's own unit
	Largest int // the biggest single cut, so an average cannot hide one huge loss
}

// Most caps measure characters. The tool index measures tools, and reporting
// twelve dropped tools as "12c" would read as twelve characters — a number off
// by three orders of magnitude in the direction that makes a real loss look
// like none.
var capUnits = map[string]func(int) string{
	"tool index": func(n int) string { return fmt.Sprintf("%d tools", n) },
}

func (c capCut) render(cap string) string {
	size := kb
	if u, ok := capUnits[cap]; ok {
		size = u
	}
	return fmt.Sprintf("%s cut %d (%s dropped, largest %s)",
		cap, c.Hits, size(c.Dropped), size(c.Largest))
}

// capAccount is a run's tally. It hangs off the Graph rather than the context
// because a Graph IS one run, and because the gate's sources are handed a graph
// and no context — the cut that matters most, one step's result reaching a
// prompt, happens inside one of them.
//
// Its own lock, not the graph's: a cut is recorded while a source is reading
// nodes, and taking the graph's lock there would deadlock.
type capAccount struct {
	mu   sync.Mutex
	cuts map[string]*capCut
}

/*
 * recordCut notes that a cap removed part of one value.
 * desc: Called by whichever cap did the cutting, with what it was asked to fit
 *       and what it kept. A no-op when the run is not accounting, and when
 *       nothing was actually removed.
 * param: cap - the cap that cut, named as budgets.go names it.
 * param: before - the size it was handed, in characters.
 * param: after - the size it kept.
 */
func (g *Graph) recordCut(cap string, before, after int) {
	dropped := before - after
	if dropped <= 0 || g == nil {
		return
	}
	a := &g.caps
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cuts == nil {
		a.cuts = map[string]*capCut{}
	}
	c := a.cuts[cap]
	if c == nil {
		c = &capCut{}
		a.cuts[cap] = c
	}
	c.Hits++
	c.Dropped += dropped
	if dropped > c.Largest {
		c.Largest = dropped
	}
}

/*
 * CapReport describes what the caps cut during a run, for a log line or a
 * trace. Empty when nothing was cut, which is the answer worth having: it says
 * the run was not starved, rather than saying nothing.
 * return: one line, or "" when no cap fired.
 */
func (g *Graph) CapReport() string {
	if g == nil {
		return ""
	}
	a := &g.caps
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cuts) == 0 {
		return ""
	}
	names := make([]string, 0, len(a.cuts))
	for n := range a.cuts {
		names = append(names, n)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, a.cuts[n].render(n))
	}
	return strings.Join(parts, "; ")
}

// kb renders a character count the way a reader skims it: exact while it is
// small enough to matter, rounded once it is not.
func kb(chars int) string {
	if chars < 1024 {
		return fmt.Sprintf("%dc", chars)
	}
	return fmt.Sprintf("%.0fKB", float64(chars)/1024)
}
