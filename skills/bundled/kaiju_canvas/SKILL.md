---
name: kaiju_canvas
description: "Render diagrams, charts, SVGs, and interactive HTML in the composable panel. Use when the user asks to draw, visualize, diagram, or render something visual."
---

## When to Use

Use when the user asks to:
- Draw a diagram (architecture, flowchart, sequence, ER)
- Visualize data as a chart or graph
- Render SVG or HTML content
- Create an interactive visualization
- Display mermaid diagrams

## Planning Guidance

### Mermaid diagram

Push mermaid syntax directly to the canvas panel:

1. `panel_push` — plugin `canvas`, mime `text/x-mermaid`, content is the mermaid source

Example mermaid content:
```
graph TD
    A[User Request] --> B[Planner]
    B --> C[DAG Scheduler]
    C --> D[Tool Execution]
    D --> E[Aggregator]
```

### SVG diagram

Generate SVG and push to preview:

1. `panel_push` — plugin `preview`, mime `image/svg+xml`, content is the SVG markup

### HTML visualization

For interactive visualizations (charts, dashboards):

1. `panel_push` — plugin `preview`, mime `text/html`, content is self-contained HTML with inline CSS/JS

### Data chart from a file

1. `file_read` — read the data source (CSV, JSON, etc.)
2. `panel_push` — plugin `preview`, generate an HTML page with a chart library (Chart.js, D3 inline) that renders the data (depends on step 0)

### Architecture diagram from code

1. `file_read` calls in parallel for the source files to analyze
2. `panel_push` — plugin `canvas`, mime `text/x-mermaid`, generate a mermaid diagram from the code structure (depends on all reads)

### Save to file

If the user wants to keep the visualization:

1. `panel_push` — display it in the panel
2. `file_write` — write the same content to a file (parallel with step 0)

### Plugin choices

| Content type | Plugin | Mime |
|-------------|--------|------|
| Mermaid diagrams | `canvas` | `text/x-mermaid` |
| SVG graphics | `preview` | `image/svg+xml` |
| HTML/interactive | `preview` | `text/html` |
| Raw data/JSON | `graph` | `application/json` |

### What NOT to do

- Don't use `file_write` + `file_read` to display ephemeral content — use `panel_push` for direct display
- Don't generate images when SVG or mermaid can represent the same thing — vector formats are sharper and lighter
- Don't create complex multi-file web apps for a simple chart — inline everything in a single HTML push
