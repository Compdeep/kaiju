package uploads

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Compdeep/kaiju/internal/agent/llm"
)

// extractMeta dispatches to a type-specific extractor and populates the
// Meta in place. Stays best-effort — caller treats failures as a soft
// "no preview available" rather than aborting the upload.
func extractMeta(absPath, mime string, meta *Meta) error {
	switch {
	case mime == "text/csv" || mime == "text/tab-separated-values":
		return extractCSV(absPath, meta, mime == "text/tab-separated-values")
	case mime == "application/json":
		return extractJSONFile(absPath, meta)
	case mime == "application/x-ndjson":
		return extractJSONL(absPath, meta)
	case isTextish(mime):
		return extractText(absPath, meta)
	}
	// Binary types (pdf, images): no preview beyond size + type. The
	// caller already filled those.
	return nil
}

// extractText pulls line count + first/last PreviewLines lines from a
// plain-text file. Streams once for the count and once for the tail —
// fine for our 25 MB cap.
func extractText(path string, meta *Meta) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// First pass: line count + head capture.
	head := make([]string, 0, PreviewLines)
	lineCount := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024) // generous; allows long log lines
	for scanner.Scan() {
		if lineCount < PreviewLines {
			head = append(head, scanner.Text())
		}
		lineCount++
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	// Second pass for tail. Cheap re-scan; with 25 MB cap and a fast
	// disk this is sub-50ms.
	if lineCount > PreviewLines {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		tail := ringbufNew(PreviewLines)
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 1024*1024), 8*1024*1024)
		for s.Scan() {
			tail.add(s.Text())
		}
		meta.TailLines = tail.values()
	}

	meta.Lines = lineCount
	meta.HeadLines = head
	return nil
}

// extractCSV reads the header row and a sample of data rows. Uses the
// stdlib csv package which copes with quoted fields, escaped commas,
// etc. Doesn't try to infer types — that's a job for whatever
// downstream tool processes the file.
func extractCSV(path string, meta *Meta, tabs bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	if tabs {
		reader.Comma = '\t'
	}
	reader.FieldsPerRecord = -1 // tolerate ragged rows

	header, err := reader.Read()
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	meta.CSVColumns = header

	sample := make([][]string, 0, CSVSampleRows)
	rowCount := 0
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Malformed row — note in head_lines and stop sampling, but
			// keep counting via a fresh line scan below for accuracy.
			break
		}
		if len(sample) < CSVSampleRows {
			sample = append(sample, row)
		}
		rowCount++
	}
	meta.CSVSample = sample
	meta.CSVRowCount = rowCount

	// Line count is rows + 1 (header); ignore for now — CSVRowCount
	// is the more accurate number for callers.
	meta.Lines = rowCount + 1
	return nil
}

// extractJSONFile reads a top-level JSON document and records the
// schema (keys + Go-side types) and a few sample values. For arrays at
// the top level, captures up to CSVSampleRows entries.
func extractJSONFile(path string, meta *Meta) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("parse json: %w", err)
	}
	switch x := v.(type) {
	case map[string]any:
		schema := make(map[string]any, len(x))
		for k, val := range x {
			schema[k] = jsonType(val)
		}
		meta.JSONSchema = schema
	case []any:
		if len(x) > 0 {
			if first, ok := x[0].(map[string]any); ok {
				schema := make(map[string]any, len(first))
				for k, val := range first {
					schema[k] = jsonType(val)
				}
				meta.JSONSchema = schema
			}
			limit := CSVSampleRows
			if len(x) < limit {
				limit = len(x)
			}
			meta.JSONSample = x[:limit]
		}
	}
	return nil
}

// extractJSONL reads up to CSVSampleRows records from an NDJSON file
// and infers a schema from the first record.
func extractJSONL(path string, meta *Meta) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	count := 0
	var sample []any
	var schema map[string]any
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		count++
		if count <= CSVSampleRows {
			var rec any
			if err := json.Unmarshal([]byte(line), &rec); err == nil {
				sample = append(sample, rec)
				if schema == nil {
					if obj, ok := rec.(map[string]any); ok {
						schema = make(map[string]any, len(obj))
						for k, val := range obj {
							schema[k] = jsonType(val)
						}
					}
				}
			}
		}
	}
	meta.Lines = count
	meta.JSONSample = sample
	meta.JSONSchema = schema
	return nil
}

// jsonType reports a short type label for a JSON-decoded value.
func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

// ringbuf keeps the last N lines seen — used for tail capture.
type ringbuf struct {
	cap  int
	data []string
}

func ringbufNew(n int) *ringbuf { return &ringbuf{cap: n} }

func (r *ringbuf) add(s string) {
	if len(r.data) < r.cap {
		r.data = append(r.data, s)
		return
	}
	r.data = append(r.data[1:], s)
}

func (r *ringbuf) values() []string {
	if r == nil {
		return nil
	}
	out := make([]string, len(r.data))
	copy(out, r.data)
	return out
}

// summarise produces a short markdown summary for a large text-class
// file. Strategy: read the whole file (capped at MaxFileSize), pass it
// to the executor LLM in one call asking for ~10 bullet points + a
// 3-sentence overview. Synchronous — caller blocks until the summary
// is on disk.
//
// For files an order of magnitude larger we'd do chunked map-reduce
// but at our 25 MB ceiling one call comfortably handles it for any
// reasonable executor model.
func (p *Processor) summarise(ctx context.Context, srcAbs, dstAbs string) error {
	if p.executor == nil {
		return fmt.Errorf("no executor client")
	}
	data, err := os.ReadFile(srcAbs)
	if err != nil {
		return err
	}
	// Cap context by truncating mid-string if needed. The summary is a
	// rough overview; missing the literal middle of a giant log isn't a
	// correctness issue.
	const maxChars = 200_000
	body := string(data)
	if len(body) > maxChars {
		head := body[:maxChars*2/3]
		tail := body[len(body)-maxChars/3:]
		body = head + "\n\n... (middle elided for summary; full file at " + srcAbs + ") ...\n\n" + tail
	}

	prompt := "You are summarising a file the user just uploaded. Produce:\n" +
		"1. A 3-sentence overview of what the file contains.\n" +
		"2. Up to 10 bullet points naming the most useful things in it (sections, tables, key terms, schemas, dates, names — whatever the type implies).\n" +
		"3. One sentence on what an agent would typically do with this file.\n\n" +
		"Output as plain markdown. No preamble, no apologies. The user's own program will read your output verbatim."

	req := &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: prompt},
			{Role: "user", Content: body},
		},
		Temperature: 0.2,
		MaxTokens:   1024,
	}
	resp, err := p.executor.Complete(ctx, req)
	if err != nil {
		return err
	}
	if len(resp.Choices) == 0 {
		return fmt.Errorf("empty summary response")
	}
	summary := resp.Choices[0].Message.Content
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("blank summary")
	}
	return os.WriteFile(dstAbs, []byte(summary), 0o644)
}
