# Tool Actions

Actions are side-effects that tools emit alongside their results. When a tool executes, it can attach one or more actions to its DAG node. These actions are delivered to frontends and API consumers as part of the node event — not as separate events.

## How It Works

```
tool.Execute()
  → fireNode checks: does the tool implement Displayer?
  → if yes, calls DisplayHint(params, result) → NodeAction
  → attaches action to node.Actions[]
  → scheduler calls SetResult → emits "node" event with actions included
  → frontend reads node.actions, routes each by type
```

Actions live **on the node**, not on a side-channel. This means:
- The DAG trace includes actions — you can replay what the agent displayed
- External API consumers see actions in the same event stream as node state
- Actions are auditable — they're part of the execution record

## NodeAction Schema

```json
{
  "type": "panel_show",
  "plugin": "preview",
  "title": "index.html",
  "path": "/home/user/project/index.html",
  "content": null,
  "mime": "text/html",
  "line": 0,
  "message": null
}
```

| Field     | Type   | Description |
|-----------|--------|-------------|
| `type`    | string | Action type. Currently: `panel_show`. Future: `notify`, `navigate`, etc. |
| `plugin`  | string | Panel plugin id: `preview`, `code`, `canvas`, `graph`, `files` |
| `title`   | string | Tab label in the panel |
| `path`    | string | File path for file-based content |
| `content` | string | Inline content for ephemeral display (no file written) |
| `mime`    | string | Content type hint: `text/html`, `image/svg+xml`, `text/x-mermaid`, etc. |
| `line`    | int    | Scroll-to line number (code plugin) |
| `message` | string | Human-readable text (for `notify` actions) |

## Action Types

### `panel_show`

Opens a tab in the composable panel. Two modes:

**File-based** — tool writes a file, action points to it:
```json
{"type": "panel_show", "plugin": "preview", "path": "build/index.html", "title": "index.html", "mime": "text/html"}
```

**Ephemeral** — content pushed directly, no file:
```json
{"type": "panel_show", "plugin": "canvas", "title": "Architecture", "content": "graph TD; A-->B", "mime": "text/x-mermaid"}
```

### Future Action Types

The `NodeAction` struct is extensible. Planned types:

| Type | Use case |
|------|----------|
| `notify` | Surface a notification to the user |
| `navigate` | Switch view, open a modal, focus a file |
| `trigger` | Invoke another tool or workflow |
| `confirm` | Request user confirmation before proceeding |

For domain-specific deployments (defence, industrial, robotics), actions are the mechanism for tool output to drive physical or system-level responses. A drone operator skill could emit `{"type": "waypoint", ...}` or `{"type": "sensor_mode", ...}` — the frontend or external consumer routes them as needed.

## Building Tools with Actions

### Go: Implement the Displayer Interface

Any tool can emit actions by implementing the optional `Displayer` interface:

```go
type Displayer interface {
    DisplayHint(params map[string]any, result string) *DisplayHint
}
```

The `DisplayHint` is converted to a `NodeAction` by the dispatcher. Return `nil` to emit no action.

Example — a tool that auto-displays HTML output:

```go
func (t *MyTool) DisplayHint(params map[string]any, result string) *tools.DisplayHint {
    path, _ := params["path"].(string)
    if filepath.Ext(path) == ".html" {
        return &tools.DisplayHint{
            Plugin: "preview",
            Path:   path,
            Title:  filepath.Base(path),
            Mime:   "text/html",
        }
    }
    return nil
}
```

### SKILL.md: The panel_push Tool

Skills can't implement Go interfaces, but they can use the `panel_push` built-in tool to push ephemeral content:

```json
{
  "skill": "panel_push",
  "params": {
    "plugin": "canvas",
    "content": "<svg>...</svg>",
    "title": "Diagram",
    "mime": "image/svg+xml"
  }
}
```

`panel_push` implements `Displayer` and emits a `panel_show` action with the content inline.

### Automatic Display Hints

`file_write` has built-in display hints based on file extension:

| Extension | Plugin | Behavior |
|-----------|--------|----------|
| `.html`, `.htm` | `preview` | Opens in sandboxed iframe |
| `.svg` | `preview` | Renders as SVG |
| `.go`, `.js`, `.ts`, `.py`, `.rs`, `.java`, `.c`, `.cpp`, `.rb`, `.sh`, `.css`, `.json`, `.yaml`, `.yml`, `.toml`, `.sql`, `.md`, `.vue`, `.jsx`, `.tsx` | `code` | Opens in code viewer |

Other extensions produce no action — the panel stays closed.

## SSE Event Example

A completed node with an action, as delivered over the `/events` SSE stream:

```json
{
  "type": "node",
  "id": "n3",
  "node": {
    "id": "n3",
    "type": "skill",
    "state": "resolved",
    "skill": "file_write",
    "tag": "write homepage",
    "ms": 42,
    "result_size": 2048,
    "result": "wrote 2048 bytes to index.html",
    "actions": [
      {
        "type": "panel_show",
        "plugin": "preview",
        "path": "index.html",
        "title": "index.html",
        "mime": "text/html"
      }
    ]
  }
}
```

## Frontend Routing

The `tools.js` service reads `node.actions` on every `node` and `done` event:

```js
function handleActions(actions) {
  for (const action of actions) {
    switch (action.type) {
      case 'panel_show':
        panel.pushTab({ ... })
        break
      // Future: case 'notify', case 'navigate', etc.
    }
  }
}
```

Actions are routed to the appropriate store. The panel store manages tabs; a future notification store would manage toasts. The tools service is the single routing point.
