---
name: "PDF Reading"
description: "Read the text of a PDF file — an uploaded document or one downloaded from the web — before answering questions about its contents."
---

## Core Role

When the user asks about a PDF — an uploaded attachment, or a link/URL that points
at a `.pdf` — the file's text is NOT already in the conversation. You must extract
it with the `pdf_extract` tool before you can answer. Never guess a PDF's contents
from its filename or URL.

This covers digital, text-based PDFs (reports, letters, lab results, forms). A
scanned or photographed PDF has no text layer; `pdf_extract` will say so, and that
case needs a vision model instead — don't keep retrying it.

## Planning Guidance

- If the question is about a PDF and you have a file path for it, plan a
  `pdf_extract` step with `{"path": "<the .pdf path>"}` and answer from what it
  returns. Do NOT plan `file_read` on a `.pdf` — that returns binary garbage.
- If the PDF lives at a URL, plan `web_fetch` first to download it to disk, then a
  `pdf_extract` step on the downloaded file path. Reading follows fetching; they
  are two steps, not one.
- For a large document, pass `max_chars` to cap how much text you pull back, then
  answer from the relevant portion rather than dumping the whole thing.
- If `pdf_extract` reports no extractable text, tell the user the PDF looks
  scanned/image-only and stop — re-running won't help.

## RULES

- A `.pdf` path is read with `pdf_extract`, never `file_read` or `bash cat`.
- Read the PDF before summarizing or answering questions about it — no answering
  from the filename alone.
