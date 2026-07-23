package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/agent/prompt"
	"github.com/Compdeep/kaiju/internal/agent/tools"
)

// VisionTool lets the planner hand an image to the configured vision model
// mid-plan — an image web_fetch just downloaded, or an uploaded photo it needs
// to reason about inside a larger task. It is a thin wrapper over the same vision
// lane the API uses directly (SoulPrompt + prompt.Vision → OneShot on the vision
// model), so a tool-less reasoning model can still "look at" an image by planning
// an image_read step. Register it only when a vision model is configured.
type VisionTool struct {
	agent *Agent
}

// NewVisionTool constructs the image_read tool bound to an Agent's vision lane.
func NewVisionTool(a *Agent) *VisionTool { return &VisionTool{agent: a} }

func (t *VisionTool) Name() string { return "image_read" }

func (t *VisionTool) Description() string {
	return "Look at an image file and answer a question about it using the vision model. " +
		"Give it an image path (png, jpg, jpeg, webp, or gif) and a question; it returns what " +
		"the vision model sees. Use this to read charts, screenshots, photos, or scanned pages " +
		"mid-task — file_read on an image only returns unreadable binary."
}

var visionToolParamSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Path to the image file (png, jpg, jpeg, webp, gif)."},
		"prompt": {"type": "string", "description": "What to ask about the image. Defaults to a general description."}
	},
	"required": ["path"]
}`)

func (t *VisionTool) Parameters() json.RawMessage { return visionToolParamSchema }

// Impact is observe-only: it reads an image file and asks a model about it.
func (t *VisionTool) Impact(map[string]any) int { return tools.ImpactObserve }

func (t *VisionTool) Execute(ctx context.Context, params map[string]any) (string, error) {
	raw, _ := params["path"].(string)
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("image_read: 'path' is required")
	}
	ask, _ := params["prompt"].(string)
	if strings.TrimSpace(ask) == "" {
		ask = "Describe this image in detail."
	}

	provider, model := t.agent.VisionModel()
	if model == "" {
		return "", fmt.Errorf("image_read: no vision model is configured on this instance")
	}

	path, err := t.resolve(raw)
	if err != nil {
		return "", err
	}
	uri, err := imageDataURI(path)
	if err != nil {
		return "", err
	}

	visionSystem := ComposeSystemPrompt(t.agent.SoulPrompt(), prompt.Vision)
	msgs := BuildMessagesWithHistory(visionSystem, ask, nil)
	llm.AttachImages(msgs, []string{uri})

	content, _, err := t.agent.OneShot(ctx, provider, model, msgs, 0.3, 1024)
	if err != nil {
		return "", fmt.Errorf("image_read: vision model: %w", err)
	}
	if strings.TrimSpace(content) == "" {
		return "(vision model returned no description)", nil
	}
	return content, nil
}

// resolve keeps file access inside the workspace sandbox, mirroring file_read:
// a relative path joins the workspace; an absolute path must live under it.
func (t *VisionTool) resolve(p string) (string, error) {
	ws := t.agent.Workspace()
	if ws == "" {
		return p, nil
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(ws, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(ws)
	if err != nil {
		return "", err
	}
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("image_read: path escapes workspace: %s", p)
	}
	return abs, nil
}

// imageDataURI reads an image file and encodes it as a base64 data URI, picking
// the MIME type from the file extension (the vision API needs the type declared).
func imageDataURI(path string) (string, error) {
	mime := imageMime(filepath.Ext(path))
	if mime == "" {
		return "", fmt.Errorf("image_read: unsupported image type %q (use png, jpg, jpeg, webp, or gif)", filepath.Ext(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("image_read: %w", err)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// imageMime maps a file extension to an image MIME type, or "" if unsupported.
func imageMime(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	}
	return ""
}
