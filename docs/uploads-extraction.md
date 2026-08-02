# Uploads & Document Extraction

Kaiju turns documents into text through three seams: a built-in tool for Office
files, an optional plugin for PDFs, and the `web_fetch` decoder path that reuses
both when a search turns up a linked document. Uploaded files ride a separate,
synchronous processor that lands them in the workspace with metadata sidecars.
This doc covers all four.

## office_extract — built-in OOXML reader

`internal/tools/office.go`. The `office_extract` tool reads Office Open XML —
Word (`.docx`), PowerPoint (`.pptx`), Excel (`.xlsx`) — into plain text.

**Why it's a built-in, not a plugin.** OOXML files are just a ZIP of XML parts, so
extraction is *pure standard library* — `archive/zip` + `encoding/xml`, no
third-party dependency. That's the line: `pdf` is a build-tag plugin because its
PDF library must be kept out of the default binary; `office_extract` has no such
dependency, so it ships in every build (`ImpactObserve` — it opens a file
read-only).

**Dispatch.** `extractOOXML` chooses the family by extension, or — when the
extension is empty (the path `web_fetch` takes with only a MIME type) — by
**sniffing the ZIP's part layout** (`word/document.xml` → Word,
`ppt/slides/slide*` → PowerPoint, `xl/workbook.xml` → Excel). One code path serves
both the tool and the decoder.

| Format | Extraction |
|---|---|
| `.docx` | `word/document.xml`; `<w:t>` runs are the text, `<w:p>` paragraphs become line breaks. |
| `.pptx` | every `ppt/slides/slideN.xml`, ordered by the **numeric** N (so slide2 precedes slide10, which a lexical sort gets wrong); `<a:t>` text, `<a:p>` paragraph breaks, one `--- Slide N ---` block each. |
| `.xlsx` | `xl/sharedStrings.xml` loaded once as the string pool, then each `xl/worksheets/sheetN.xml` streamed to **tab-separated** rows; `t="s"` cells resolve their `<v>` index against the pool, inline strings and numbers use their value directly. |

**Legacy formats are rejected.** `.doc` / `.ppt` / `.xls` are the old OLE binary
containers — a heavy converter, not a ZIP — so the tool returns an explicit
"save it as the modern format" error rather than emitting garbage.

**Limits.** Output is capped at `officeMaxChars` (200 000 chars; caller can raise
via `max_chars`) so a huge document can't blow the context window. File access is
**workspace-sandboxed**: `resolve` joins a relative path onto the workspace and
requires an absolute path to live under it, mirroring `pdf_extract`. An opened
file with no extractable text returns an honest "found no extractable text" line,
not an empty success.

## pdf — the build-tag plugin

`internal/plugins/pdf/pdf.go` (build tag `plugin_pdf`). The PDF counterpart to
`office_extract`, kept as a plugin because its library (`ledongthuc/pdf`) is a
third-party dependency the default binary shouldn't carry. It contributes:

- the `pdf_extract` tool (`ImpactObserve`, workspace-sandboxed, 200 000-char cap),
  reading a **digital text layer only** — a scanned / image-only PDF returns
  little or no text (that needs a vision model); and
- an `application/pdf` **binary decoder** so `web_fetch` can read a PDF a search
  turned up.

Compile with `-tags plugin_pdf` and switch on with `plugins: ["pdf"]`. See
`plugins.md` for the framework.

## The uploads processor

`internal/agent/uploads/`. When a user uploads a file, a **synchronous** pipeline
runs — the HTTP request blocks until the file is on disk and its sidecars are
written, so the frontend chip goes straight from "uploading…" to "✓".

```
validate → write → extract metadata → optional summary → memory entry
```

1. **Validate** (`processor.go`). The extension must be in `allowedExt` (text,
   code, CSV/TSV, JSON/JSONL, the three OOXML types, `.pdf`, common images);
   anything else is rejected. `declaredSize` is checked against `MaxFileSize`
   (25 MB) up front.
2. **Write.** Destination is `<workspace>/uploads/<session-id>/<file>`, resolved
   through `workspace.SafeJoin` so an upload can't escape the workspace. The
   stream is copied under an `io.LimitReader(MaxFileSize+1)` cap so a lying
   `Content-Length` can't blow past the limit; a per-session quota
   (`MaxSessionTotal`, 200 MB) is enforced too.
3. **Extract metadata** (`extract.go`) into a `<file>.meta.json` sidecar — a
   *preview*, so the agent can decide whether it needs the whole file: line count
   + head/tail for text, header + sampled rows for CSV/TSV, inferred schema +
   sample records for JSON/JSONL. **Binary types (PDF, images, OOXML) get no
   preview beyond size + type** — their text is extracted on demand later by
   `office_extract` / `pdf_extract`, not at upload time. Extraction failure is
   non-fatal: the file is still on disk and readable raw.
4. **Optional summary.** For a text-class file over `SummaryThreshold` (100 KB),
   the executor LLM writes a `<file>.summary.md` (≈10 bullets + a short overview,
   one call, head/tail-elided if huge). Skipped when no executor client is set;
   failure is non-fatal.
5. **Memory entry.** A `upload:<session>:<file>` key records the paths, type,
   size and session, tagged `upload` / `session:<sid>` / `type:<mime>`, so the
   agent can find the file in later turns. Re-uploading the same name overwrites
   cleanly.

Tiny text files (≤ `InlineThreshold`, 8 KB) are returned inline in the `Result`
for zero-round-trip access. Uploaded **images** are re-read each turn as base64
data URIs (`SessionImageDataURIs`) — that's what "pins" an image so it stays
visible to the vision model across follow-up questions.

## web_fetch extraction seams

`internal/tools/web.go` + `internal/agent/tools/decoders.go`. Core `web_fetch`
does a plain HTTP GET plus readability. Two seams let it read *more* without
pulling heavy dependencies into the default binary; with no plugin compiled in,
every lookup is a miss and `web_fetch` behaves exactly as it always has.

**1. The read-cap raise.** HTML extraction needs ~256 KB, but a binary
`web_fetch` is about to *decode* (a multi-MB PDF or Office report) needs the whole
file — the HTML cap would corrupt it. Both signals are known before the body is
read, so the cap is lifted from **256 KB to 16 MB** when either
`HasBinaryDecoder(Content-Type)` is true **or** the URL looks like a `.pdf`
(`looksLikePDFURL`):

```go
readCap := int64(256 * 1024)
if agenttools.HasBinaryDecoder(ctype) || looksLikePDFURL(rawURL) {
    readCap = 16 * 1024 * 1024
}
```

**2. `decodePageBinary`.** For a non-error response, `web_fetch` tries a
registered binary decoder — first by `Content-Type`, then (when the type is
missing/generic) by a `.pdf` URL fallback. A hit means the body was a document:
the extracted text becomes the content (truncated to ~16 000 chars for context);
a decoder that returns empty yields an honest "downloaded a document with no
extractable text (likely scanned or image-only)" note — no fabrication. The
`office_extract` decoders (`RegisterOfficeDecoders`, called at startup) and the
`pdf` plugin's decoder both plug in here.

**3. `primaryContent` — the reader plugin is PRIMARY.** A registered reader
fallback (the webreader plugin: headless render + extraction) is not a
last-resort — it is the **primary reader for *every* page**. `primaryContent`
calls `ReaderFallback` first and, when it returns **≥ 200 chars**, uses that
verbatim; otherwise `web_fetch` falls back to built-in readability
(`stripHTML` → OG/meta):

```go
func primaryContent(ctx context.Context, rawURL string) (string, bool) {
    if txt, ok, _ := agenttools.ReaderFallback(ctx, rawURL); ok {
        if t := strings.TrimSpace(txt); len(t) >= 200 {
            return t, true
        }
    }
    return "", false
}
```

The 200-char floor exists because a JS/SPA page usually returns a couple hundred
chars of static nav/boilerplate, so the old "readability came back thin" trigger
never fired on exactly the pages a renderer is needed for — making the plugin the
default reader closes that gap. When nothing is extractable — no decoder, no
plugin, thin readability — `web_fetch` returns an **empty envelope** with a note
saying so; the coverage edge reads that and does not invent content. See
`plugins.md` for the reader plugin.

## Relevant source

| file | responsibility |
|---|---|
| `internal/tools/office.go` | `office_extract` tool + OOXML extractors + `RegisterOfficeDecoders` |
| `internal/plugins/pdf/pdf.go` | the `pdf_extract` tool + `application/pdf` decoder (build-tag plugin) |
| `internal/agent/uploads/processor.go` | the synchronous upload pipeline + limits |
| `internal/agent/uploads/extract.go` | metadata extractors (text/CSV/JSON/JSONL) + LLM summary |
| `internal/tools/web.go` | `web_fetch`: read-cap raise, `decodePageBinary`, `primaryContent` |
| `internal/agent/tools/decoders.go` | the two `web_fetch` seams (binary decoder + reader fallback) |
