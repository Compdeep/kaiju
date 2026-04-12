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

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
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
	return json.RawMessage(`{"type":"object","properties":{"result":{"type":"string","description":"archive operation result"}}}`)
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactAffect since archive operations modify the filesystem.
 * param: params - unused parameters
 * return: ImpactAffect (1)
 */
func (a *Archive) Impact(params map[string]any) int {
	return agenttools.ImpactAffect
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
func (a *Archive) Execute(ctx context.Context, params map[string]any) (string, error) {
	action, _ := params["action"].(string)
	archivePath, _ := params["archive_path"].(string)
	if archivePath == "" {
		return "", fmt.Errorf("archive: archive_path is required")
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
			return "", fmt.Errorf("archive: files list is required for create")
		}
		return archiveCreate(archivePath, files, format)
	default:
		return "", fmt.Errorf("archive: unknown action %q", action)
	}
}

/*
 * archiveList lists the contents of an archive file.
 * desc: Opens the archive and returns a formatted list of entries with size, date, and name.
 * param: path - path to the archive file
 * param: format - archive format ("zip" or "tar.gz")
 * return: formatted entry list with count header, or error on read failure
 */
func archiveList(path, format string) (string, error) {
	switch format {
	case "zip":
		r, err := zip.OpenReader(path)
		if err != nil {
			return "", fmt.Errorf("archive: %w", err)
		}
		defer r.Close()
		var lines []string
		for _, f := range r.File {
			lines = append(lines, fmt.Sprintf("%10d  %s  %s", f.UncompressedSize64, f.Modified.Format("2006-01-02 15:04"), f.Name))
		}
		return fmt.Sprintf("%d entries:\n%s", len(lines), strings.Join(lines, "\n")), nil

	case "tar.gz":
		f, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("archive: %w", err)
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return "", fmt.Errorf("archive: %w", err)
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		var lines []string
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", fmt.Errorf("archive: %w", err)
			}
			lines = append(lines, fmt.Sprintf("%10d  %s  %s", hdr.Size, hdr.ModTime.Format("2006-01-02 15:04"), hdr.Name))
		}
		return fmt.Sprintf("%d entries:\n%s", len(lines), strings.Join(lines, "\n")), nil

	default:
		return "", fmt.Errorf("archive: unsupported format %q", format)
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
func archiveExtract(archivePath, dest, format string) (string, error) {
	if err := os.MkdirAll(dest, 0755); err != nil {
		return "", fmt.Errorf("archive: mkdir: %w", err)
	}

	switch format {
	case "zip":
		r, err := zip.OpenReader(archivePath)
		if err != nil {
			return "", fmt.Errorf("archive: %w", err)
		}
		defer r.Close()
		count := 0
		for _, f := range r.File {
			target := filepath.Join(dest, f.Name)
			// Prevent zip slip
			if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dest)) {
				continue
			}
			if f.FileInfo().IsDir() {
				os.MkdirAll(target, 0755)
				continue
			}
			os.MkdirAll(filepath.Dir(target), 0755)
			rc, err := f.Open()
			if err != nil {
				continue
			}
			out, err := os.Create(target)
			if err != nil {
				rc.Close()
				continue
			}
			io.Copy(out, rc)
			out.Close()
			rc.Close()
			count++
		}
		return fmt.Sprintf("extracted %d files to %s", count, dest), nil

	case "tar.gz":
		f, err := os.Open(archivePath)
		if err != nil {
			return "", fmt.Errorf("archive: %w", err)
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return "", fmt.Errorf("archive: %w", err)
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		count := 0
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", fmt.Errorf("archive: %w", err)
			}
			target := filepath.Join(dest, hdr.Name)
			if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dest)) {
				continue
			}
			switch hdr.Typeflag {
			case tar.TypeDir:
				os.MkdirAll(target, 0755)
			case tar.TypeReg:
				os.MkdirAll(filepath.Dir(target), 0755)
				out, err := os.Create(target)
				if err != nil {
					continue
				}
				io.Copy(out, tr)
				out.Close()
				count++
			}
		}
		return fmt.Sprintf("extracted %d files to %s", count, dest), nil

	default:
		return "", fmt.Errorf("archive: unsupported format %q", format)
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
func archiveCreate(archivePath string, files []string, format string) (string, error) {
	switch format {
	case "zip":
		out, err := os.Create(archivePath)
		if err != nil {
			return "", fmt.Errorf("archive: %w", err)
		}
		defer out.Close()
		zw := zip.NewWriter(out)
		defer zw.Close()
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
				io.Copy(w, f)
				count++
				return nil
			})
			if err != nil {
				return "", fmt.Errorf("archive: %w", err)
			}
		}
		return fmt.Sprintf("created %s with %d files", archivePath, count), nil

	case "tar.gz":
		out, err := os.Create(archivePath)
		if err != nil {
			return "", fmt.Errorf("archive: %w", err)
		}
		defer out.Close()
		gz := gzip.NewWriter(out)
		defer gz.Close()
		tw := tar.NewWriter(gz)
		defer tw.Close()
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
				io.Copy(tw, f)
				count++
				return nil
			})
			if err != nil {
				return "", fmt.Errorf("archive: %w", err)
			}
		}
		return fmt.Sprintf("created %s with %d files", archivePath, count), nil

	default:
		return "", fmt.Errorf("archive: unsupported format %q", format)
	}
}

var _ agenttools.Tool = (*Archive)(nil)
