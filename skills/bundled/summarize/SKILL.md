---
name: summarize
description: "Summarize or extract text from URLs, podcasts, videos, and local files. Use when asked to summarize a webpage, article, YouTube video, or document."
---

## When to Use

Use when the user asks to summarize, extract text from, or transcribe:
- Web pages or articles
- YouTube videos (via transcript)
- Local text files, PDFs, or documents
- Podcast episodes (via feed URL)

## Planning Guidance

### Summarize a URL

1. `web_fetch` — fetch the page with `format=summary` and `focus` describing what to extract

Single step. Use a broad focus to capture the key points in one fetch.

### Summarize multiple URLs

Plan fetches in parallel:

1. `web_fetch` — fetch URL 1 with `format=summary`
2. `web_fetch` — fetch URL 2 with `format=summary` (parallel with step 0)
3. `web_fetch` — fetch URL 3 with `format=summary` (parallel with step 0)

### Summarize a YouTube video

1. `bash` — `yt-dlp --write-auto-sub --skip-download --sub-lang en -o "/tmp/%(id)s" "URL"` to get transcript
2. `file_read` — read the subtitle file (depends on step 0)

If `yt-dlp` is not available, fall back to `web_fetch` on the YouTube page.

### Summarize a local file

1. `file_read` — read the file

Single step. The aggregator synthesizes the summary from the content.

### Summarize multiple local files

Plan parallel reads:

1. `file_read` — read file A
2. `file_read` — read file B (parallel with step 0)
3. `file_read` — read file C (parallel with step 0)

### What NOT to do

- Don't plan `web_search` before `web_fetch` if the user already gave you the URL
- Don't fetch the same URL multiple times with different focus values — use one broad focus
- Don't plan sequential fetches for independent URLs — parallelize them
