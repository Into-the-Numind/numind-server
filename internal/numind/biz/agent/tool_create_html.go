package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// createHTMLTool renders an HTML page from content or a Go template and uploads it.
type createHTMLTool struct {
	BaseTool
}

var _ FullTool = (*createHTMLTool)(nil)

// defaultHTMLTemplate wraps a body *fragment* in a minimal styled document.
// It is used ONLY when the caller passes an HTML fragment (not a full document)
// and supplies no custom template. {{.Title}} is escaped (plain text); {{.Body}}
// is rendered raw (template.HTML) because the agent authored it as HTML.
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
	return "Render an HTML page and upload it to cloud storage, returning a download URL. " +
		"Pass `content` as a COMPLETE HTML document (starting with <!DOCTYPE html> or <html>) " +
		"and it is served verbatim — your CSS, layout, and markup render as-is. " +
		"A bare fragment is wrapped in a minimal styled page. " +
		"Content is treated as HTML (NOT escaped), so write real markup, not escaped text. " +
		"Use for formatted reports, dashboards, or styled documents. " +
		"For programmatic charts or office formats (pptx/docx/xlsx/pdf), prefer the load_skill → run_python path."
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
			"content":  {"description": "The HTML to publish. Preferred: a complete HTML document (\"<!DOCTYPE html>...\") served verbatim. A bare fragment is wrapped in a default styled page. An object is used as template variables when 'template' is set."},
			"template": {"type": "string", "description": "Optional Go html/template string; receives 'content' (object) as data. Variables ARE escaped by html/template."},
			"title":    {"type": "string", "description": "Optional page title (used by the default wrapper when content is a fragment)"},
			"filename": {"type": "string", "description": "Optional output filename (e.g. report.html)"}
		},
		"required": ["content"]
	}`)
}

type createHTMLInput struct {
	// Template is an optional Go html/template string.
	Template string `json:"template,omitempty"`
	// Content is the page content: a string is raw HTML (full document or fragment);
	// a map provides template variables when Template is set.
	Content interface{} `json:"content"`
	// Title is used by the default wrapper as {{.Title}} for fragment content.
	Title string `json:"title,omitempty"`
	// Filename is the optional output filename.
	Filename string `json:"filename,omitempty"`
}

func (t *createHTMLTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in createHTMLInput
	// Model-input and recoverable failures stay soft: a non-nil Go error is a
	// NodeRunError that kills the whole agent run (tool-soft-error-sweep).
	if err := json.Unmarshal(input, &in); err != nil {
		return softToolError("create_html", "invalid input: %v", err)
	}

	filename := in.Filename
	if filename == "" {
		filename = "generated_" + time.Now().Format("20060102_150405") + ".html"
	} else if lower := strings.ToLower(filename); !strings.HasSuffix(lower, ".html") && !strings.HasSuffix(lower, ".htm") {
		// Guarantee an .html extension so the read-path re-sign (cosIsInlineRenderName,
		// extension-based) agrees with the write-path inline signing (mime-based) — else
		// an extension-less name would re-sign as a download and break iframe preview on
		// reload (问题五 review P2).
		filename += ".html"
	}

	htmlBytes, err := renderHTML(in)
	if err != nil {
		// Template parse/render failures are input-driven (model-supplied
		// template). renderHTML errors already carry the "create_html: " prefix;
		// trim it so softToolError does not double it.
		return softToolError("create_html", "%s", strings.TrimPrefix(err.Error(), "create_html: "))
	}

	result, uploadErr := uploadGeneratedFile(ctx, htmlBytes, "text/html; charset=utf-8", filename, "html")
	if uploadErr != nil {
		return softToolError("create_html", "upload failed: %v", uploadErr)
	}
	return result, nil
}

// renderHTML produces the final HTML bytes from the tool input. Extracted from
// Execute so the rendering contract is unit-testable without COS/upload.
//
// Rendering rules:
//   - A custom Template is rendered with `content` as the template data
//     (html/template escapes interpolated variables; the template author owns markup).
//   - Otherwise a STRING content is the agent-authored HTML:
//   - a full document ("<!doctype"/"<html" prefix) is returned verbatim;
//   - a fragment is wrapped in defaultHTMLTemplate with a raw (unescaped) body.
//   - A MAP content without a template is treated as {title, body} (case-insensitive),
//     wrapped in defaultHTMLTemplate with a raw body.
//
// SECURITY / threat model: create_html output is uploaded to COS under
// agent-outputs/<userID>/ and handed back to the SAME user as a short-lived
// presigned URL on the object-storage origin (not the app origin, no app
// cookies/session). It is a first-party artifact the user explicitly asked the
// agent to generate — analogous to run_python writing an arbitrary file. The
// tool's entire purpose is to render agent-authored HTML, so we do NOT escape it;
// escaping produces unusable output (the page source shown as literal text).
// Callers needing escaped interpolation can supply a custom `template`.
func renderHTML(in createHTMLInput) ([]byte, error) {
	if in.Template != "" {
		return renderWithTemplate(in.Template, templateData(in))
	}

	switch v := in.Content.(type) {
	case string:
		if isFullHTMLDocument(v) {
			return []byte(v), nil
		}
		return renderFragment(in.Title, v)
	case map[string]interface{}:
		title := firstStringKey(v, "title", "Title")
		if title == "" {
			title = in.Title
		}
		body := firstStringKey(v, "body", "Body")
		return renderFragment(title, body)
	case nil:
		return renderFragment(in.Title, "")
	default:
		// Unexpected JSON shape (number/bool/array). Stringify into the body so
		// the call still produces a usable page rather than erroring out.
		return renderFragment(in.Title, fmt.Sprintf("%v", v))
	}
}

// isFullHTMLDocument reports whether s looks like a standalone HTML document
// (so it should be served verbatim rather than wrapped). A leading UTF-8 BOM
// is stripped first so a BOM-prefixed full document isn't mis-wrapped.
func isFullHTMLDocument(s string) bool {
	low := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(s, "\ufeff")))
	return strings.HasPrefix(low, "<!doctype") || strings.HasPrefix(low, "<html")
}

// renderFragment wraps an HTML body fragment in the default styled document.
// Body is injected as template.HTML (raw); Title stays a plain escaped string.
func renderFragment(title, body string) ([]byte, error) {
	tmpl, err := template.New("html").Parse(defaultHTMLTemplate)
	if err != nil {
		return nil, fmt.Errorf("create_html: template parse error: %w", err)
	}
	data := struct {
		Title string
		Body  template.HTML
	}{
		Title: title,
		Body:  template.HTML(body), //nolint:gosec // see renderHTML threat-model note: first-party artifact, raw HTML is the feature.
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("create_html: template render error: %w", err)
	}
	return buf.Bytes(), nil
}

// renderWithTemplate renders a caller-supplied Go html/template with the given data.
func renderWithTemplate(tmplStr string, data interface{}) ([]byte, error) {
	tmpl, err := template.New("html").Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("create_html: template parse error: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("create_html: template render error: %w", err)
	}
	return buf.Bytes(), nil
}

// templateData prepares the data passed to a custom template. A string content
// populates {{.Title}}/{{.Body}}; a map is passed through with case-insensitive
// title/body aliases so Go's case-sensitive html/template finds {{.Title}}/{{.Body}}.
func templateData(in createHTMLInput) interface{} {
	switch v := in.Content.(type) {
	case string:
		return struct {
			Title string
			Body  string
		}{Title: in.Title, Body: v}
	case map[string]interface{}:
		if title, ok := v["title"]; ok && v["Title"] == nil {
			v["Title"] = title
		}
		if body, ok := v["body"]; ok && v["Body"] == nil {
			v["Body"] = body
		}
		return v
	default:
		return in.Content
	}
}

// firstStringKey returns the first key in keys whose value in m is a non-empty string.
func firstStringKey(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
