package agent

// tool_lark_create_doc.go — the lark_create_doc agent tool (feishu-integration
// T10). Creates a 飞书 docx document on behalf of the run initiator and writes
// the provided content into it. Scope: docx:document.
//
// Failure policy (design.md §8): EVERY failure path returns a SOFT tool result
// (a successful ToolResult whose "error" field describes the problem), never a Go
// error — a Go error becomes an Eino NodeRunError that kills the whole agent run.

import (
	"context"
	"encoding/json"
	"strings"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/middleware"
)

// larkCreateDocTool implements FullTool for lark_create_doc.
type larkCreateDocTool struct {
	BaseTool
	provider feishu.LarkAPIProvider // nil → 飞书 integration off (soft error at Execute)
}

var _ FullTool = (*larkCreateDocTool)(nil)

func (t *larkCreateDocTool) Name() string { return "lark_create_doc" }
func (t *larkCreateDocTool) Description() string {
	return "Create a 飞书 (Lark) document on behalf of the user and write content into it. " +
		"Requires the user to have connected 飞书 (scope docx:document). " +
		"Input: { title: string, content?: string }. Returns: { document_id, title, url }."
}
func (t *larkCreateDocTool) UserFacingName() string { return "创建飞书文档" }
func (t *larkCreateDocTool) NarrationVerb() string  { return "创建飞书文档" }

// Writes to the user's 飞书 workspace → not read-only / not concurrency-safe.
func (t *larkCreateDocTool) IsReadOnly() bool                   { return false }
func (t *larkCreateDocTool) IsConcurrencySafe(_ ToolInput) bool { return false }

func (t *larkCreateDocTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"title":   {"type": "string", "description": "The document title."},
			"content": {"type": "string", "description": "Optional document body text to write as the first block."}
		},
		"required": ["title"]
	}`)
}

type larkCreateDocInput struct {
	Title   string `json:"title"`
	Content string `json:"content,omitempty"`
}

type larkCreateDocOutput struct {
	DocumentID string `json:"document_id,omitempty"`
	Title      string `json:"title,omitempty"`
	URL        string `json:"url,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (t *larkCreateDocTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	const label = "创建飞书文档"

	var in larkCreateDocInput
	if err := json.Unmarshal(input, &in); err != nil {
		return larkSoftError("lark_create_doc 输入格式错误：%s", err.Error())
	}
	if strings.TrimSpace(in.Title) == "" {
		return larkSoftError("lark_create_doc 需要 title 参数（文档标题不能为空）。")
	}

	userID, _ := middleware.UserIDFromCtx(ctx)
	endSpan := larkStartSpan(ctx, "create_doc", userID, in)

	api, soft, proceed := larkAPIFor(ctx, t.provider, label)
	if !proceed {
		endSpan(map[string]any{"outcome": "precondition_failed"}, "precondition failed")
		return soft, nil
	}

	res, err := api.CreateDoc(ctx, in.Title, in.Content)
	if err != nil {
		// The `docs +create` shortcut imports the whole doc in one call — any failure
		// means no doc was created (no partial-success path).
		endSpan(map[string]any{"outcome": "error"}, err.Error())
		return larkSoftErrorForAPIErr(label, err)
	}

	endSpan(map[string]any{"document_id": res.DocumentID, "outcome": "ok"}, "")
	out, _ := json.Marshal(larkCreateDocOutput{
		DocumentID: res.DocumentID,
		Title:      res.Title,
		URL:        res.URL,
	})
	return ToolResult(out), nil
}
