package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"time"
)

// createHTMLTool renders an HTML page from content or a Go template and uploads it.
type createHTMLTool struct {
	BaseTool
}

var _ FullTool = (*createHTMLTool)(nil)

// defaultHTMLTemplate is used when the caller does not provide a custom template.
// {{.Title}} and {{.Body}} are the two template variables available by default.
const defaultHTMLTemplate = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="UTF-8">
<title>{{.Title}}</title>
<style>
body{font-family:sans-serif;max-width:900px;margin:40px auto;padding:0 20px;line-height:1.6}
</style>
</head>
<body>{{.Body}}</body>
</html>`

func (t *createHTMLTool) Name() string { return "create_html" }
func (t *createHTMLTool) Description() string {
	return "Render an HTML page from content or a template and upload it to cloud storage. " +
		"Returns a download URL. Use for formatted reports, dashboards, or styled documents. " +
		"Content is HTML-escaped by default for safety. " +
		"For multi-axis or complex static charts, prefer the invoke_skill path."
}
func (t *createHTMLTool) UserFacingName() string      { return "生成 HTML 页面" }
func (t *createHTMLTool) NarrationVerb() string       { return "生成" }
func (t *createHTMLTool) IsReadOnly() bool            { return false }
func (t *createHTMLTool) IsEnabled(_ ToolConfig) bool { return true }
func (t *createHTMLTool) InterruptBehavior() string   { return "cancel" }

func (t *createHTMLTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"content":  {"description": "Page content — string for body text, or object for template variables"},
			"template": {"type": "string", "description": "Optional Go html/template string; uses {{.Title}} and {{.Body}} by default"},
			"title":    {"type": "string", "description": "Optional page title (used in default template as {{.Title}})"},
			"filename": {"type": "string", "description": "Optional output filename (e.g. report.html)"}
		},
		"required": ["content"]
	}`)
}

type createHTMLInput struct {
	// Template is an optional Go html/template string.
	Template string `json:"template,omitempty"`
	// Content is the template data: a string populates {{.Body}}; a map provides arbitrary keys.
	Content interface{} `json:"content"`
	// Title is used by the default template as {{.Title}}.
	Title string `json:"title,omitempty"`
	// Filename is the optional output filename.
	Filename string `json:"filename,omitempty"`
}

func (t *createHTMLTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in createHTMLInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("create_html: invalid input: %w", err)
	}

	filename := in.Filename
	if filename == "" {
		filename = "generated_" + time.Now().Format("20060102_150405") + ".html"
	}

	tmplStr := in.Template
	if tmplStr == "" {
		tmplStr = defaultHTMLTemplate
	}

	tmpl, err := template.New("html").Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("create_html: template parse error: %w", err)
	}

	// Determine template data.
	// SECURITY: use plain string fields so html/template automatically HTML-escapes
	// {{.Title}} and {{.Body}} at render time. Using template.HTML would bypass escaping
	// and allow XSS if LLM-generated or user-supplied content contains script tags.
	// If a caller genuinely needs raw HTML injection (trusted internal use only),
	// they must provide a custom template that explicitly wraps the value with
	// the template.HTML cast — that is a conscious decision by the template author.
	var renderData interface{}
	switch v := in.Content.(type) {
	case string:
		// When content is a plain string, inject into default template variables.
		// Both Title and Body are plain strings — html/template will escape them.
		renderData = struct {
			Title string
			Body  string
		}{
			Title: in.Title,
			Body:  v,
		}
	default:
		// Map or other complex type — pass directly as template data.
		renderData = in.Content
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, renderData); err != nil {
		return nil, fmt.Errorf("create_html: template render error: %w", err)
	}

	return uploadGeneratedFile(ctx, buf.Bytes(), "text/html; charset=utf-8", filename, "html")
}
