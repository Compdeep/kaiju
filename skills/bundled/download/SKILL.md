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

**When the user wants files downloaded (download, get, grab, save, fetch, "more", "again", "try again"), they mean SAVE FILES TO DISK.** web_search and web_fetch find links — they do NOT download files. You MUST plan bash commands with yt-dlp or curl to actually download. A plan with only web_search/web_fetch steps is WRONG for a download request.

### Video download (TikTok, YouTube, Instagram, etc.)

Use `yt-dlp` via bash — it supports 1000+ sites including TikTok, YouTube, Instagram, Twitter, Vimeo, Reddit, and more. Downloads auto-detect a 5-minute timeout. For very large files, set `timeout_sec: 0` (no timeout).

1. `bash` — `yt-dlp -o 'media/%(title)s.%(ext)s' '<URL>'`

For multiple videos in parallel:

1. `bash` — `yt-dlp -o '%(title)s.%(ext)s' '<URL1>'`
2. `bash` — `yt-dlp -o '%(title)s.%(ext)s' '<URL2>'` (parallel)
3. `bash` — `yt-dlp -o '%(title)s.%(ext)s' '<URL3>'` (parallel)

If yt-dlp is not installed, install it first:

1. `bash` — `pip install yt-dlp`
2. `bash` — download command (depends on step 0)

**Never download full playlists unless the user explicitly asks for a playlist.** If a search returns a playlist URL, either extract individual video URLs from it or use `--no-playlist --max-downloads 1` to get a single video. Playlists can contain hundreds of videos and will time out.

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

Open the media panel so the user can see what was downloaded:

1. `panel_push` with `{"plugin": "media", "title": "Media"}` — opens the media browser showing all downloaded files

### When downloads fail

If yt-dlp fails, try alternatives in this order:
1. Try different yt-dlp flags: `--extractor-args "youtube:player-client=mediaconnect"`
2. Try a different platform — search for the same content on Dailymotion, Vimeo, or other sites
3. Try `gallery-dl`, `curl`, or `wget` for direct URLs
4. If everything fails, return to the user honestly: explain what was tried and what blocked it

**Do NOT attempt to upgrade Python, install system packages, compile from source, or use sudo.** These are system admin tasks, not download tasks. If the tools on the system can't do it, say so.

**Never hardcode or guess URLs.** Always web_search first, then extract real URLs from the results.

### RULES

- Always web_search for real URLs before downloading. Never fabricate video IDs.
- Never download full playlists unless explicitly asked. Use `--no-playlist --max-downloads 1`.
- Do NOT upgrade system Python, install compilers, or build from source. Use what's available.
- If all attempts fail, tell the user what's wrong and what they'd need to fix (e.g. "yt-dlp needs Python 3.10+").
- Don't use `web_fetch` to download binary files — it's for reading web pages.
- Don't plan sequential downloads for independent URLs — parallelize them.
