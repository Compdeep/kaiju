package tools

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

/*
 * Archive creates or extracts archive files in zip and tar.gz formats.
 * desc: Tool for creating, extracting, and listing archive files with zip-slip protection on extraction.
 */
type Archive struct{}

/*
 * NewArchive creates a new Archive tool instance.
 * desc: Returns a zero-value Archive ready for use.
 * return: pointer to a new Archive
 */
func NewArchive() *Archive { return &Archive{} }

/*
 * Name returns the tool identifier.
 * desc: Returns "archive" as the tool name.
 * return: the string "archive"
 */
func (a *Archive) Name() string { return "archive" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that this tool creates or extracts zip and tar.gz archives.
 * return: description string
 */
func (a *Archive) Description() string {
	return "Create or extract archive files. Supports zip and tar.gz formats."
}

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing the archive operation result message.
 * return: JSON schema as raw bytes
 */
func (a *Archive) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOfWithNote(archiveData{},
		"Fields depend on action: entries is filled by list, dest by extract."))
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactAffect since archive operations modify the filesystem.
 * param: params - unused parameters
 * return: ImpactAffect (1)
 */
func (a *Archive) Impact(params map[string]any) int {
	return toolapi.ImpactAffect
}

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines action (create/extract/list), archive_path, files, dest, and format parameters.
 * return: JSON schema as raw bytes
 */
func (a *Archive) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["create", "extract", "list"], "description": "Action to perform"},
			"archive_path": {"type": "string", "description": "Path to the archive file"},
			"files": {"type": "array", "items": {"type": "string"}, "description": "Files/directories to archive (for create)"},
			"dest": {"type": "string", "description": "Destination directory (for extract, default: current dir)"},
			"format": {"type": "string", "enum": ["zip", "tar.gz"], "description": "Archive format (default: inferred from extension)"}
		},
		"required": ["action", "archive_path"],
		"additionalProperties": false
	}`)
}

/*
 * Execute performs the specified archive action (create, extract, or list).
 * desc: Routes to archiveList, archiveExtract, or archiveCreate based on the action parameter, auto-detecting format from extension.
 * param: ctx - context for cancellation
 * param: params - must contain "action" and "archive_path"; optionally "files", "dest", "format"
 * return: operation result string, or error for unknown actions or missing parameters
 */
// Execute satisfies the Tool interface for callers outside the DAG.
func (a *Archive) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(a.ExecuteTyped(ctx, params))
}

func (a *Archive) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	action, _ := params["action"].(string)
	archivePath, _ := params["archive_path"].(string)
	if archivePath == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("archive: archive_path is required")
	}

	format, _ := params["format"].(string)
	if format == "" {
		if strings.HasSuffix(archivePath, ".tar.gz") || strings.HasSuffix(archivePath, ".tgz") {
			format = "tar.gz"
		} else {
			format = "zip"
		}
	}

	switch action {
	case "list":
		return archiveList(archivePath, format)
	case "extract":
		dest, _ := params["dest"].(string)
		if dest == "" {
			dest = "."
		}
		return archiveExtract(archivePath, dest, format)
	case "create":
		filesRaw, _ := params["files"].([]any)
		var files []string
		for _, f := range filesRaw {
			if s, ok := f.(string); ok {
				files = append(files, s)
			}
		}
		if len(files) == 0 {
			return toolapi.ToolMessage{}, fmt.Errorf("archive: files list is required for create")
		}
		return archiveCreate(archivePath, files, format)
	default:
		return toolapi.ToolMessage{}, fmt.Errorf("archive: unknown action %q", action)
	}
}

/*
 * archiveList lists the contents of an archive file.
 * desc: Opens the archive and returns a formatted list of entries with size, date, and name.
 * param: path - path to the archive file
 * param: format - archive format ("zip" or "tar.gz")
 * return: formatted entry list with count header, or error on read failure
 */
func archiveList(path, format string) (toolapi.ToolMessage, error) {
	switch format {
	case "zip":
		r, err := zip.OpenReader(path)
		if err != nil {
			return toolapi.ToolMessage{}, fmt.Errorf("archive: %w", err)
		}
		defer r.Close()
		var lines, names []string
		for _, f := range r.File {
			lines = append(lines, fmt.Sprintf("%10d  %s  %s", f.UncompressedSize64, f.Modified.Format("2006-01-02 15:04"), f.Name))
			names = append(names, f.Name)
		}
		if len(lines) == 0 {
			// An archive with nothing in it is a result about the archive, not a
			// listing of length zero dressed as a success. It read "0 entries:" with
			// nothing after the colon.
			return toolapi.ToolEmpty("listing", "the archive "+path+" holds no entries"), nil
		}
		return toolapi.ToolOK("listing", fmt.Sprintf("%d entries:\n%s", len(lines), strings.Join(lines, "\n")),
			archiveData{Action: "list", Path: path, Count: len(lines), Entries: names}), nil

	case "tar.gz":
		f, err := os.Open(path)
		if err != nil {
			return toolapi.ToolMessage{}, fmt.Errorf("archive: %w", err)
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return toolapi.ToolMessage{}, fmt.Errorf("archive: %w", err)
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		var lines, names []string
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return toolapi.ToolMessage{}, fmt.Errorf("archive: %w", err)
			}
			lines = append(lines, fmt.Sprintf("%10d  %s  %s", hdr.Size, hdr.ModTime.Format("2006-01-02 15:04"), hdr.Name))
			names = append(names, hdr.Name)
		}
		if len(lines) == 0 {
			// An archive with nothing in it is a result about the archive, not a
			// listing of length zero dressed as a success. It read "0 entries:" with
			// nothing after the colon.
			return toolapi.ToolEmpty("listing", "the archive "+path+" holds no entries"), nil
		}
		return toolapi.ToolOK("listing", fmt.Sprintf("%d entries:\n%s", len(lines), strings.Join(lines, "\n")),
			archiveData{Action: "list", Path: path, Count: len(lines), Entries: names}), nil

	default:
		return toolapi.ToolMessage{}, fmt.Errorf("archive: unsupported format %q", format)
	}
}

/*
 * archiveExtract extracts an archive to the specified destination directory.
 * desc: Extracts files from the archive with zip-slip protection, creating directories as needed.
 * param: archivePath - path to the archive file
 * param: dest - destination directory for extracted files
 * param: format - archive format ("zip" or "tar.gz")
 * return: confirmation message with extracted file count, or error on failure
 */
/*
 * withinDest reports whether an extracted path stays inside the destination.
 *
 * The test was a plain prefix comparison, which a sibling directory passes: with a
 * destination of /tmp/out, an entry named ../outside/x cleans to /tmp/outside/x, which
 * begins with /tmp/out and was written there. Requiring the separator is what makes the
 * comparison mean "inside this directory" rather than "starts with these letters".
 *
 * param: dest - the directory the caller named.
 * param: target - where the entry would be written.
 * return: true when the target is the destination or below it.
 */
func withinDest(dest, target string) bool {
	dest = filepath.Clean(dest)
	target = filepath.Clean(target)
	return target == dest || strings.HasPrefix(target, dest+string(os.PathSeparator))
}

/*
 * archiveExtractResult reports what an extraction did, including what it did not do.
 *
 * Every per-entry failure was passed over silently and the count returned as a success,
 * so an archive of a hundred entries where sixty could not be written reported
 * "extracted 40 files" — which reads exactly like an archive that held forty. An entry
 * whose path pointed outside the destination was passed over the same way, and that one
 * is evidence rather than noise: a traversal path inside an archive is a fact about the
 * archive, and it was the one thing this function was in a position to notice.
 *
 * param: path - the archive read.
 * param: dest - where its contents were written.
 * param: count - files written.
 * param: skipped - entries that could not be written.
 * param: refused - entries whose path pointed outside dest.
 * return: the message a run receives.
 */
func archiveExtractResult(path, dest string, count, skipped, refused int) (toolapi.ToolMessage, error) {
	data := archiveData{
		Action: "extract", Path: path, Dest: dest, Count: count,
		Skipped: skipped, Refused: refused, Complete: skipped == 0 && refused == 0,
	}
	text := fmt.Sprintf("extracted %d files to %s", count, dest)
	if refused > 0 {
		text += fmt.Sprintf("; %d entries refused for pointing outside %s", refused, dest)
	}
	if skipped > 0 {
		text += fmt.Sprintf("; %d entries could not be written", skipped)
	}
	switch {
	case count == 0 && (skipped > 0 || refused > 0):
		// Nothing arrived, and entries were there to arrive. Reporting zero files as a
		// success reads as an empty archive rather than a failed extraction.
		return toolapi.ToolFail("status", text, data), nil
	case count == 0:
		return toolapi.ToolEmpty("status", "the archive holds no files to extract"), nil
	default:
		return toolapi.ToolOK("status", text, data), nil
	}
}

func archiveExtract(archivePath, dest, format string) (toolapi.ToolMessage, error) {
	if err := os.MkdirAll(dest, 0755); err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("archive: mkdir: %w", err)
	}

	switch format {
	case "zip":
		r, err := zip.OpenReader(archivePath)
		if err != nil {
			return toolapi.ToolMessage{}, fmt.Errorf("archive: %w", err)
		}
		defer r.Close()
		count, skipped, refused := 0, 0, 0
		for _, f := range r.File {
			target := filepath.Join(dest, f.Name)
			if !withinDest(dest, target) {
				refused++
				continue
			}
			if f.FileInfo().IsDir() {
				os.MkdirAll(target, 0755)
				continue
			}
			os.MkdirAll(filepath.Dir(target), 0755)
			rc, err := f.Open()
			if err != nil {
				skipped++
				continue
			}
			out, err := os.Create(target)
			if err != nil {
				rc.Close()
				skipped++
				continue
			}
			_, copyErr := io.Copy(out, rc)
			closeErr := out.Close()
			rc.Close()
			// A copy that stopped part way leaves a file shorter than the entry, and
			// a close that failed may mean the bytes never reached the disk. Counting
			// either as extracted puts a file in the count that is not on disk whole.
			if copyErr != nil || closeErr != nil {
				skipped++
				continue
			}
			count++
		}
		return archiveExtractResult(archivePath, dest, count, skipped, refused)

	case "tar.gz":
		f, err := os.Open(archivePath)
		if err != nil {
			return toolapi.ToolMessage{}, fmt.Errorf("archive: %w", err)
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return toolapi.ToolMessage{}, fmt.Errorf("archive: %w", err)
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		count, skipped, refused := 0, 0, 0
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return toolapi.ToolMessage{}, fmt.Errorf("archive: %w", err)
			}
			target := filepath.Join(dest, hdr.Name)
			if !withinDest(dest, target) {
				refused++
				continue
			}
			switch hdr.Typeflag {
			case tar.TypeDir:
				os.MkdirAll(target, 0755)
			case tar.TypeReg:
				os.MkdirAll(filepath.Dir(target), 0755)
				out, err := os.Create(target)
				if err != nil {
					skipped++
					continue
				}
				_, copyErr := io.Copy(out, tr)
				closeErr := out.Close()
				if copyErr != nil || closeErr != nil {
					skipped++
					continue
				}
				count++
			}
		}
		return archiveExtractResult(archivePath, dest, count, skipped, refused)

	default:
		return toolapi.ToolMessage{}, fmt.Errorf("archive: unsupported format %q", format)
	}
}

/*
 * archiveCreate creates an archive from the specified files.
 * desc: Walks the given file paths and adds all regular files to a new archive in the specified format.
 * param: archivePath - output path for the new archive file
 * param: files - list of file/directory paths to include
 * param: format - archive format ("zip" or "tar.gz")
 * return: confirmation message with archive path and file count, or error on failure
 */
func archiveCreate(archivePath string, files []string, format string) (toolapi.ToolMessage, error) {
	switch format {
	case "zip":
		out, err := os.Create(archivePath)
		if err != nil {
			return toolapi.ToolMessage{}, fmt.Errorf("archive: %w", err)
		}
		defer out.Close()
		zw := zip.NewWriter(out)
		count := 0
		for _, path := range files {
			err := filepath.Walk(path, func(fpath string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return err
				}
				w, err := zw.Create(fpath)
				if err != nil {
					return err
				}
				f, err := os.Open(fpath)
				if err != nil {
					return err
				}
				defer f.Close()
				// A copy that stopped part way puts a shorter file in the archive
				// than the one on disk, and the count said it was added.
				if _, err := io.Copy(w, f); err != nil {
					return err
				}
				count++
				return nil
			})
			if err != nil {
				zw.Close()
				return toolapi.ToolMessage{}, fmt.Errorf("archive: %w", err)
			}
		}
		// Closing the writer is what writes the index at the end of a zip, so its
		// error is the difference between an archive and a file that looks like one.
		// It was deferred, which discarded it: a disk that filled during the write
		// produced an unreadable file and the tool reported "created ... with N
		// files", and the fault surfaced later as a corrupt archive nothing connected
		// back to this step.
		if err := zw.Close(); err != nil {
			return toolapi.ToolMessage{}, fmt.Errorf("archive: finishing %s: %w", archivePath, err)
		}
		if err := out.Close(); err != nil {
			return toolapi.ToolMessage{}, fmt.Errorf("archive: finishing %s: %w", archivePath, err)
		}
		return toolapi.ToolOK("status", fmt.Sprintf("created %s with %d files", archivePath, count),
			archiveData{Action: "create", Path: archivePath, Count: count}), nil

	case "tar.gz":
		out, err := os.Create(archivePath)
		if err != nil {
			return toolapi.ToolMessage{}, fmt.Errorf("archive: %w", err)
		}
		defer out.Close()
		gz := gzip.NewWriter(out)
		tw := tar.NewWriter(gz)
		count := 0
		for _, path := range files {
			err := filepath.Walk(path, func(fpath string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return err
				}
				hdr, err := tar.FileInfoHeader(info, "")
				if err != nil {
					return err
				}
				hdr.Name = fpath
				if err := tw.WriteHeader(hdr); err != nil {
					return err
				}
				f, err := os.Open(fpath)
				if err != nil {
					return err
				}
				defer f.Close()
				if _, err := io.Copy(tw, f); err != nil {
					return err
				}
				count++
				return nil
			})
			if err != nil {
				tw.Close()
				gz.Close()
				return toolapi.ToolMessage{}, fmt.Errorf("archive: %w", err)
			}
		}
		// Three closes, each writing the end of its own layer: the tar's trailing
		// blocks, the gzip footer, and the file itself. All three were deferred and
		// their errors discarded, so a write that failed at any layer still reported a
		// created archive.
		// In this order, and not from a map: each layer writes its ending into the one
		// beneath it, so the tar has to finish before the gzip it sits in and the gzip
		// before the file. Go randomises map iteration, so ranging a map here would
		// close them in a different order on different runs.
		layers := []struct {
			what   string
			closer io.Closer
		}{{"tar", tw}, {"gzip", gz}, {"file", out}}
		for _, layer := range layers {
			what, closer := layer.what, layer.closer
			if err := closer.Close(); err != nil {
				return toolapi.ToolMessage{}, fmt.Errorf("archive: finishing the %s layer of %s: %w",
					what, archivePath, err)
			}
		}
		return toolapi.ToolOK("status", fmt.Sprintf("created %s with %d files", archivePath, count),
			archiveData{Action: "create", Path: archivePath, Count: count}), nil

	default:
		return toolapi.ToolMessage{}, fmt.Errorf("archive: unsupported format %q", format)
	}
}

var _ toolapi.Tool = (*Archive)(nil)
