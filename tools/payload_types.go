package tools

import (
	"strconv"
	"strings"
)

// The payloads of the tools that answered in prose alone.
//
// Each of these put its result into a sentence: "wrote 412 bytes to /tmp/x",
// "extracted 9 files to /tmp/y", "3 entries:". A run reading that has the answer
// and cannot name it, so wiring one step's result into the next step's parameter
// meant quoting a sentence forward and hoping the number was read back out of it.
//
// The structs here are what those tools now return alongside the same text as
// before. Every OutputSchema is derived from the struct, so the declaration a
// planner reads and the payload a run receives come from one definition.

// fileReadData is what file_read returns beside the lines it read.
type fileReadData struct {
	Path       string `json:"path" desc:"the file that was read"`
	LinesShown int    `json:"lines_shown" desc:"lines in the text of this result"`
	LinesTotal int    `json:"lines_total" desc:"lines in the file"`
	Truncated  bool   `json:"truncated" desc:"true when the file has more lines than were shown"`
	FromEnd    bool   `json:"from_end" desc:"true when the lines shown are the last of the file rather than the first"`
	Binary     bool   `json:"binary,omitempty" desc:"true when the file is not text and was described rather than read"`
	BinaryKind string `json:"binary_kind,omitempty" desc:"what kind of binary it is — ELF, PE/COFF, gzip, zip, PNG, PDF, Mach-O"`
	Bytes      int64  `json:"bytes,omitempty" desc:"the file's size, reported when it was not read"`
}

// fileWriteData is what file_write returns.
type fileWriteData struct {
	Path     string `json:"path" desc:"the file that was written"`
	Bytes    int    `json:"bytes" desc:"bytes written"`
	Appended bool   `json:"appended" desc:"true when the bytes went on the end of an existing file, false when the file was replaced"`
}

// gitData is what git returns beside the command's output.
type gitData struct {
	Action    string `json:"action" desc:"the git subcommand that was run"`
	Lines     int    `json:"lines" desc:"lines of output, after any truncation"`
	Truncated bool   `json:"truncated" desc:"the output was longer than this tool returns and the rest was cut, so what came back is not the whole answer"`
}

// panelPushData is what panel_push returns.
type panelPushData struct {
	Plugin string `json:"plugin" desc:"the panel that was written to"`
	Bytes  int    `json:"bytes" desc:"bytes pushed"`
}

// archiveData is what archive returns for any of its actions.
type archiveData struct {
	Action  string   `json:"action" desc:"list, extract or create"`
	Path    string   `json:"path" desc:"the archive"`
	Dest    string   `json:"dest,omitempty" desc:"action=extract: where the files were written"`
	Count   int      `json:"count" desc:"entries listed, files extracted, or files put in the archive"`
	Entries []string `json:"entries,omitempty" desc:"action=list: the names inside the archive"`
	// What an extraction left behind. Absent from the other two actions.
	Skipped  int  `json:"skipped,omitempty" desc:"action=extract: entries that could not be written, so they are not on disk however high the count is"`
	Refused  int  `json:"refused,omitempty" desc:"action=extract: entries whose path pointed outside the destination and were not written. Not noise: a path like that in an archive is itself worth knowing about"`
	Complete bool `json:"complete,omitempty" desc:"action=extract: false when anything was skipped or refused, which means the destination does not hold everything the archive named"`
}

// officeData is what office_extract returns beside the text of the document.
type officeData struct {
	Kind      string `json:"kind" desc:"the document family, such as docx or pdf"`
	File      string `json:"file" desc:"the file's name, without its directory"`
	Chars     int    `json:"chars" desc:"characters of text found, before any cut"`
	Truncated bool   `json:"truncated" desc:"the text was cut at the tool's character limit"`
}

// processRow is one process from a listing.
type processRow struct {
	User    string  `json:"user,omitempty" desc:"the account it runs as"`
	PID     int     `json:"pid" desc:"the process identifier"`
	CPU     float64 `json:"cpu_percent" desc:"percentage of a processor in use"`
	Memory  float64 `json:"mem_percent" desc:"percentage of memory in use"`
	Command string  `json:"command" desc:"the command line, whole rather than cut at the terminal width"`
}

// processListData is what process_list returns beside its table.
type processListData struct {
	Count     int          `json:"count" desc:"processes in this result"`
	Limit     int          `json:"limit" desc:"the most this call would return"`
	Filter    string       `json:"filter,omitempty" desc:"the substring each line had to contain"`
	AtLimit   bool         `json:"at_limit" desc:"true when the limit was reached, so there are more processes than are shown"`
	Processes []processRow `json:"processes,omitempty" desc:"one per process. Empty where the listing command's columns are not the ones parsed here, in which case the text is still complete"`
}

/*
 * parsePsRows reads the columns of ps auxww into rows.
 *
 * The layout is USER, PID, %CPU, %MEM, VSZ, RSS, TTY, STAT, START, TIME, COMMAND,
 * and the command is everything from the eleventh field on, since it contains
 * spaces and is last.
 *
 * param: lines - the lines already selected for the result, header first.
 * return: one row per line that has ps's layout. A line that does not is skipped,
 *         so a platform printing something else yields no rows rather than wrong
 *         ones, and the text of the result is unaffected either way.
 */
func parsePsRows(lines []string) []processRow {
	var rows []processRow
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue // the header names the columns
		}
		fields := strings.Fields(line)
		if len(fields) < 11 {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		cpu, err := strconv.ParseFloat(fields[2], 64)
		if err != nil {
			continue
		}
		mem, err := strconv.ParseFloat(fields[3], 64)
		if err != nil {
			continue
		}
		rows = append(rows, processRow{
			User:    fields[0],
			PID:     pid,
			CPU:     cpu,
			Memory:  mem,
			Command: strings.Join(fields[10:], " "),
		})
	}
	return rows
}
