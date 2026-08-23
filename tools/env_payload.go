package tools

import (
	"strconv"
	"strings"
)

// The payloads of env_list, disk_usage and clipboard.
//
// All three answered in text alone. A run asking how full a disk is got a table of
// df output and had to read a percentage out of prose; a later step could name no
// field of it, so the whole table was quoted forward instead. Same for the count of
// variables matched and the size of what came off the clipboard.
//
// The structs here are what those three now return, and each tool's OutputSchema is
// derived from the struct rather than written out beside it. Parsing is on this side
// of the platform split: df and du print columns, the PowerShell equivalents print a
// table with different headings, so the fields are filled where the columns are known
// and left empty otherwise. The text is unchanged either way, so nothing that reads
// it loses anything on a platform whose columns are not parsed here.

// envListData is what env_list returns beside its listing.
type envListData struct {
	Count     int               `json:"count" desc:"variables returned, after the name filter"`
	Filter    string            `json:"filter,omitempty" desc:"the substring matched against names, when one was given"`
	Masked    int               `json:"masked" desc:"values replaced with **** because the name looks sensitive"`
	Variables map[string]string `json:"variables" desc:"name to value, masked unless show_sensitive was set"`
}

// filesystemRow is one line of df: one mounted filesystem.
type filesystemRow struct {
	Filesystem string `json:"filesystem" desc:"the device or source"`
	Size       string `json:"size" desc:"total size as df prints it, such as 99G"`
	Used       string `json:"used" desc:"space in use, as df prints it"`
	Available  string `json:"available" desc:"space free, as df prints it"`
	UsePercent int    `json:"use_percent" desc:"percentage in use, as a number rather than a string ending in %"`
	MountedOn  string `json:"mounted_on" desc:"where it is mounted"`
}

// diskEntry is one line of du: a directory and its size.
type diskEntry struct {
	Size string `json:"size" desc:"size as du prints it, such as 23M"`
	Path string `json:"path" desc:"the directory measured"`
}

// diskUsageData is what disk_usage returns beside its report.
type diskUsageData struct {
	Path        string          `json:"path,omitempty" desc:"the directory asked about; absent when reporting mounted filesystems"`
	Filesystems []filesystemRow `json:"filesystems,omitempty" desc:"one per mounted filesystem"`
	Entries     []diskEntry     `json:"entries,omitempty" desc:"one per subdirectory measured"`
	Unreadable  []string        `json:"unreadable,omitempty" desc:"directories that could not be read, so their contents are not counted in any size above"`
	Complete    bool            `json:"complete" desc:"false when a directory could not be read or the scan ran out of time, which means every size is a lower bound"`
	Truncated   bool            `json:"truncated" desc:"the text was cut at 4KB; the fields here were not"`
}

// clipboardData is what clipboard returns for either action.
type clipboardData struct {
	Action    string `json:"action" desc:"read or write"`
	Bytes     int    `json:"bytes" desc:"length of the content read or written, before any cut"`
	Truncated bool   `json:"truncated" desc:"action=read: the text was cut at 8KB"`
}

/*
 * parseDfTable reads df's columns into rows.
 *
 * Only the layout df prints is understood: Filesystem, Size, Used, Avail, Use%,
 * Mounted on. A mount point containing spaces is kept whole, since it is the last
 * column and everything after the fifth field belongs to it.
 *
 * param: out - df's output, header line included.
 * return: one row per filesystem. A line that does not have six fields is skipped
 *         rather than guessed at, so a platform printing something else yields no
 *         rows instead of wrong ones.
 */
func parseDfTable(out string) []filesystemRow {
	var rows []filesystemRow
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue // the header names the columns, it is not a filesystem
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		percent, err := strconv.Atoi(strings.TrimSuffix(fields[4], "%"))
		if err != nil {
			continue // not df's layout, so this line is not read
		}
		rows = append(rows, filesystemRow{
			Filesystem: fields[0],
			Size:       fields[1],
			Used:       fields[2],
			Available:  fields[3],
			UsePercent: percent,
			MountedOn:  strings.Join(fields[5:], " "),
		})
	}
	return rows
}

/*
 * parseDuLines reads du's output into sizes and the directories it could not read.
 *
 * du writes one measured directory per line as size, a tab, then the path, and one
 * line per directory it was refused. Both arrive together because the caller
 * captures the two streams as one, which is why they are separated here rather
 * than by the caller.
 *
 * param: out - du's output, both streams.
 * return: the directories measured, and the ones that could not be read.
 */
func parseDuLines(out string) (entries []diskEntry, unreadable []string) {
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}

		// du: cannot read directory '/tmp/x': Permission denied
		if strings.HasPrefix(line, "du:") {
			if path, ok := quotedPath(line); ok {
				unreadable = append(unreadable, path)
			}
			continue
		}

		// A heading this tool prints itself, not a line of du output.
		if !strings.Contains(line, "\t") {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		size, path := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if size == "" || path == "" {
			continue
		}
		entries = append(entries, diskEntry{Size: size, Path: path})
	}
	return entries, unreadable
}

// quotedPath takes the path out of a du error line, which quotes it with
// apostrophes. A line with no quoted path yields false rather than a fragment of
// the message.
func quotedPath(line string) (string, bool) {
	open := strings.Index(line, "'")
	if open < 0 {
		return "", false
	}
	close := strings.Index(line[open+1:], "'")
	if close < 0 {
		return "", false
	}
	return line[open+1 : open+1+close], true
}
