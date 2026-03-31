---
name: kaiju_display
description: Handles explicit display requests — show files, preview websites, render diagrams, visualize data in the composable panel
---

## When to Use

Use when the user explicitly asks to display, show, preview, visualize, or render something in the panel. Examples:

- "show me that file"
- "preview the website"
- "draw a diagram of the architecture"
- "visualize the test results"
- "open the canvas"
- "display that as a graph"

Do NOT use for implicit display — tools like `file_write` automatically push to the panel when they produce displayable content. This skill is only for explicit user requests.

## Planning Guidance

### Show a file

If the user asks to see a file that already exists:

1. `file_read` — read the file
2. `panel_push` — push content to the appropriate plugin (depends on read, use `param_refs` to chain content)

Choose the plugin based on file type:
- `.html`, `.svg` → `preview`
- Code files (`.go`, `.js`, `.py`, etc.) → `code`
- `.json` data → `graph` (if the user wants visualization) or `code` (if they want to read it)

### Preview a web project

If the user wants to preview HTML they just built or a local site:

1. `panel_push` with plugin `preview` — push the HTML content directly

For multi-file projects with CSS/JS, prefer pushing the main HTML file via `file_read` → `panel_push` rather than reconstructing it inline.

### Render a diagram

If the user asks for architecture diagrams, flowcharts, or relationship maps:

1. `panel_push` with plugin `canvas`, mime `text/x-mermaid` — push mermaid syntax
2. Or `panel_push` with plugin `preview`, mime `image/svg+xml` — push generated SVG

Prefer mermaid for structured diagrams (flowcharts, sequence diagrams, ER diagrams). Prefer SVG for custom visuals.

### Visualize data

If the user asks to graph or chart data:

1. `bash` or `file_read` — gather the data
2. `panel_push` with plugin `graph` — push JSON data (depends on data gathering step)

### What NOT to do

- Don't plan `panel_push` for content that `file_write` already handles — writing an HTML file automatically triggers the preview panel
- Don't read a file just to display it if the user only wants a quick preview — use `panel_push` with inline content when possible
- Don't plan `web_search` to find content to display — this skill is for content that already exists locally or can be generated
