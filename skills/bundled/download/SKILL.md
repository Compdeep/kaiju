---
name: download
description: "Download files from the internet — videos, documents, images, repos, archives. Use when the user asks to download, save, grab, or fetch any file or media from a URL."
---

## When to Use

Use when the user asks to download, save, or grab:
- Videos (TikTok, YouTube, Instagram, Twitter, Vimeo, any supported site)
- Files (PDFs, images, archives, binaries)
- Repositories (git repos)
- Multiple files or bulk downloads

Do NOT use for reading web page content — use `web_fetch` for that.

## Planning Guidance

### Video download (TikTok, YouTube, Instagram, etc.)

Use `yt-dlp` via bash — it supports 1000+ sites including TikTok, YouTube, Instagram, Twitter, Vimeo, Reddit, and more.

1. `bash` — `yt-dlp -o '%(title)s.%(ext)s' '<URL>'`

For multiple videos in parallel:

1. `bash` — `yt-dlp -o '%(title)s.%(ext)s' '<URL1>'`
2. `bash` — `yt-dlp -o '%(title)s.%(ext)s' '<URL2>'` (parallel)
3. `bash` — `yt-dlp -o '%(title)s.%(ext)s' '<URL3>'` (parallel)

If yt-dlp is not installed, install it first:

1. `bash` — `pip install yt-dlp`
2. `bash` — download command (depends on step 0)

Common yt-dlp options:
- Best quality: `yt-dlp -f best '<URL>'`
- Audio only: `yt-dlp -x --audio-format mp3 '<URL>'`
- Custom filename: `yt-dlp -o 'output.%(ext)s' '<URL>'`
- List formats: `yt-dlp -F '<URL>'`
- With subtitles: `yt-dlp --write-subs '<URL>'`
- Playlist: `yt-dlp --yes-playlist '<URL>'`

### File download (PDF, image, archive, binary)

Use `curl` or `wget`:

1. `bash` — `curl -L -O '<URL>'`

Or with a custom filename:

1. `bash` — `curl -L -o 'filename.pdf' '<URL>'`

For multiple files in parallel:

1. `bash` — `curl -L -O '<URL1>'`
2. `bash` — `curl -L -O '<URL2>'` (parallel)
3. `bash` — `curl -L -O '<URL3>'` (parallel)

### Git repository

1. `bash` — `git clone '<REPO_URL>'`

For a specific branch:

1. `bash` — `git clone -b '<BRANCH>' '<REPO_URL>'`

### Bulk download (many files from a list)

1. `file_read` — read the file containing URLs
2. `bash` — `xargs -n 1 -P 4 curl -L -O < urls.txt` (depends on step 0)

Or if URLs come from a search:

1. `web_search` — find the download page
2. `web_fetch` — extract download URLs from the page (depends on step 0)
3. `bash` — download each URL with curl (depends on step 1, use `param_refs`)

### Check if tools are installed

If unsure whether yt-dlp, curl, or wget are available:

1. `bash` — `which yt-dlp curl wget 2>/dev/null`

### After downloading

Push the downloaded file to the composable panel if it's viewable:

1. `bash` — download the file
2. `panel_push` — display it if it's HTML, SVG, or an image (depends on step 0)

### What NOT to do

- Don't use `web_fetch` to download binary files — it's for reading web pages
- Don't plan sequential downloads for independent URLs — parallelize them
- Don't search for "how to download" — use yt-dlp for media, curl for files
- Don't declare a gap for downloads — bash with yt-dlp or curl can handle it
