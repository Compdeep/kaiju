package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Compdeep/kaiju/internal/agent/tools"
)

/*
 * PanelPush sends ephemeral content directly to the composable panel without writing a file.
 * desc: Tool for pushing generated diagrams, HTML previews, inline code, or other content to the UI panel.
 */
type PanelPush struct{}

/*
 * NewPanelPush creates a new PanelPush tool instance.
 * desc: Returns a zero-value PanelPush ready for use.
 * return: pointer to a new PanelPush
 */
func NewPanelPush() *PanelPush { return &PanelPush{} }

/*
 * Name returns the tool identifier.
 * desc: Returns "panel_push" as the tool name.
 * return: the string "panel_push"
 */
func (p *PanelPush) Name() string { return "panel_push" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that this tool pushes content to the composable panel for rendering.
 * return: description string
 */
func (p *PanelPush) Description() string {
	return "Push content directly to the composable panel. Use for generated HTML, SVG, diagrams, code snippets, or data visualizations that don't need to be saved to a file."
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactObserve since pushing to a panel is non-destructive.
 * param: _ - unused parameters
 * return: ImpactObserve (0)
 */
func (p *PanelPush) Impact(map[string]any) int { return tools.ImpactObserve }

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines plugin, content, title, and mime parameters for panel rendering.
 * return: JSON schema as raw bytes
 */
func (p *PanelPush) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"plugin": {
				"type": "string",
				"enum": ["preview", "code", "canvas", "graph"],
				"description": "Panel plugin to render the content"
			},
			"content": {
				"type": "string",
				"description": "The content to display (HTML, SVG, code, JSON data, mermaid diagram, etc.)"
			},
			"title": {
				"type": "string",
				"description": "Tab label for this content"
			},
			"mime": {
				"type": "string",
				"description": "Content type hint (e.g. text/html, image/svg+xml, application/json, text/x-mermaid)"
			}
		},
		"required": ["plugin", "content"],
		"additionalProperties": false
	}`)
}

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing a result confirmation string.
 * return: JSON schema as raw bytes
 */
func (p *PanelPush) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"result":{"type":"string"}}}`)
}

/*
 * Execute pushes content to the specified panel plugin.
 * desc: Validates that plugin and content are provided, then returns a confirmation message.
 * param: _ - unused context
 * param: params - must contain "plugin" and "content"; optionally "title" and "mime"
 * return: confirmation message with byte count and plugin name, or error if required params missing
 */
func (p *PanelPush) Execute(_ context.Context, params map[string]any) (string, error) {
	plugin, _ := params["plugin"].(string)
	content, _ := params["content"].(string)
	if plugin == "" || content == "" {
		return "", fmt.Errorf("panel_push: plugin and content are required")
	}
	return fmt.Sprintf("pushed %d bytes to %s panel", len(content), plugin), nil
}

/*
 * DisplayHint returns the content as an ephemeral panel push for frontend rendering.
 * desc: Constructs a DisplayHint from the plugin, content, title, and mime parameters.
 * param: params - tool parameters containing plugin, content, title, and mime
 * param: result - the execution result string (unused)
 * return: DisplayHint with plugin/title/content/mime, or nil if required params are missing
 */
func (p *PanelPush) DisplayHint(params map[string]any, result string) *tools.DisplayHint {
	plugin, _ := params["plugin"].(string)
	content, _ := params["content"].(string)
	title, _ := params["title"].(string)
	mime, _ := params["mime"].(string)
	if plugin == "" || content == "" {
		return nil
	}
	if title == "" {
		title = plugin
	}
	return &tools.DisplayHint{
		Plugin:  plugin,
		Title:   title,
		Content: content,
		Mime:    mime,
	}
}

var _ tools.Tool = (*PanelPush)(nil)
var _ tools.Outputter = (*PanelPush)(nil)
var _ tools.Displayer = (*PanelPush)(nil)
