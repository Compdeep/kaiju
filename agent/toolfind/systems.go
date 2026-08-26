package toolfind

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Describing a registry too large to list.
//
// A model shown forty tools of a thousand, with nothing to say the other nine
// hundred and sixty exist, plans with the forty and then reports the work
// impossible. It is not wrong to do so: nothing told it otherwise. So the tools
// that did not fit are represented by where they came from — one line per
// source system, with how many it holds and a sample of what they do.
//
// The cost does not grow with the registry. Fifty source systems is about three
// thousand characters whether they hold ten tools each or two hundred.

// systemSampleVerbs is how many distinctive name endings are shown per system.
const systemSampleVerbs = 6

// describeSystems renders one line per source, ordered by how many tools each
// holds so the largest are read first.
//
// Returns "" when every tool came from the same place. The point of these lines
// is to say what exists beyond the tools that fit, and one line covering the
// whole registry says only that it is the whole registry — which the caller
// already states as a count. A single-source deployment gets nothing rather
// than a line worth nothing.
func describeSystems(reg *toolapi.Registry, names []string) string {
	bySource := map[string][]string{}
	for _, n := range names {
		if src := reg.GetSource(n); src != "" {
			bySource[src] = append(bySource[src], n)
		}
	}
	if len(bySource) < 2 {
		return ""
	}

	sources := make([]string, 0, len(bySource))
	for s := range bySource {
		sources = append(sources, s)
	}
	sort.Slice(sources, func(i, j int) bool {
		if len(bySource[sources[i]]) != len(bySource[sources[j]]) {
			return len(bySource[sources[i]]) > len(bySource[sources[j]])
		}
		return sources[i] < sources[j]
	})

	var sb strings.Builder
	for _, src := range sources {
		tools := bySource[src]
		sb.WriteString(fmt.Sprintf("%s — %d tool", src, len(tools)))
		if len(tools) != 1 {
			sb.WriteString("s")
		}
		if sample := sampleVerbs(src, tools); sample != "" {
			sb.WriteString(": ")
			sb.WriteString(sample)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// sampleVerbs picks a few tool names from a source, with the source's own
// prefix removed where it has one — "jira_create_issue" in the jira line reads
// as "create issue", and the line says what the system does rather than
// repeating its name six times.
func sampleVerbs(source string, tools []string) string {
	seen := map[string]bool{}
	var out []string
	sorted := append([]string(nil), tools...)
	sort.Strings(sorted)
	for _, t := range sorted {
		v := strings.TrimPrefix(t, source+"_")
		v = strings.ReplaceAll(v, "_", " ")
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
		if len(out) == systemSampleVerbs {
			break
		}
	}
	if len(tools) > len(out) {
		return strings.Join(out, ", ") + ", …"
	}
	return strings.Join(out, ", ")
}

// paramText is a tool's parameter names and their descriptions, as one line of
// searchable text. A tool is often asked for by the thing it takes — "by issue
// key", "with a cron expression" — and that word appears nowhere but here.
func paramText(schema json.RawMessage) string {
	var s struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if len(schema) == 0 || json.Unmarshal(schema, &s) != nil {
		return ""
	}
	keys := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(strings.ReplaceAll(k, "_", " "))
		if d := s.Properties[k].Description; d != "" {
			sb.WriteString(" ")
			sb.WriteString(d)
		}
		sb.WriteString(" ")
	}
	return sb.String()
}
