package agent

// tool_lark_read_bitable.go — the lark_read_bitable agent tool (feishu-integration
// T10). Reads records from a 飞书 (Lark) 多维表格 (bitable) table. Scope:
// bitable:app:readonly.
//
// Failure policy (design.md §8): EVERY failure path returns a SOFT tool result,
// never a Go error (a Go error kills the agent run via Eino NodeRunError).

import (
	"context"
	"encoding/json"
	"strings"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/middleware"
)

// larkReadBitableDefaultPageSize / larkReadBitableMaxPageSize bound the page size
// the LLM may request. 飞书's list-records cap is 500; we default modestly to keep
// tool results small, and clamp anything larger.
const (
	larkReadBitableDefaultPageSize = 20
	larkReadBitableMaxPageSize     = 100
)

// larkReadBitableTool implements FullTool for lark_read_bitable.
type larkReadBitableTool struct {
	BaseTool
	provider feishu.LarkAPIProvider // nil → 飞书 integration off (soft error at Execute)
}

var _ FullTool = (*larkReadBitableTool)(nil)

func (t *larkReadBitableTool) Name() string { return "lark_read_bitable" }
func (t *larkReadBitableTool) Description() string {
	return "Read records from a 飞书 (Lark) Bitable (多维表格) table on behalf of the connected user. " +
		"Read-only (scope bitable:app:readonly). " +
		"Input: { app_token: string, table_id: string, page_size?: number, page_token?: string }. " +
		"Returns: { records: [{record_id, fields}], has_more, page_token, total }."
}
func (t *larkReadBitableTool) UserFacingName() string { return "读取飞书多维表格" }
func (t *larkReadBitableTool) NarrationVerb() string  { return "读取飞书多维表格" }

// Read-only by definition (bitable:app:readonly).
func (t *larkReadBitableTool) IsReadOnly() bool            { return true }
func (t *larkReadBitableTool) IsSearchOrReadCommand() bool { return true }

func (t *larkReadBitableTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"app_token":  {"type": "string", "description": "The Bitable app token (the base id)."},
			"table_id":   {"type": "string", "description": "The table id within the base."},
			"page_size":  {"type": "integer", "description": "Number of records to return (default 20, max 100)."},
			"page_token": {"type": "string", "description": "Paging token from a previous call's page_token to fetch the next page."}
		},
		"required": ["app_token", "table_id"]
	}`)
}

// larkReadBitableInput models the tool's parameters. PageSize is json.Number so a
// model that sends a string ("20") or a number (20) both parse — LLMs are loose
// with numeric types (CLAUDE.md: tools must tolerate string/number for numeric
// fields rather than hard-erroring).
type larkReadBitableInput struct {
	AppToken  string      `json:"app_token"`
	TableID   string      `json:"table_id"`
	PageSize  json.Number `json:"page_size,omitempty"`
	PageToken string      `json:"page_token,omitempty"`
}

type larkReadBitableOutput struct {
	Records   []feishu.BitableRecord `json:"records"`
	HasMore   bool                   `json:"has_more"`
	PageToken string                 `json:"page_token,omitempty"`
	Total     int                    `json:"total"`
	Error     string                 `json:"error,omitempty"`
}

func (t *larkReadBitableTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	const label = "读取飞书多维表格"

	var in larkReadBitableInput
	if err := json.Unmarshal(input, &in); err != nil {
		return larkSoftError("lark_read_bitable 输入格式错误：%s", err.Error())
	}
	if strings.TrimSpace(in.AppToken) == "" {
		return larkSoftError("lark_read_bitable 需要 app_token 参数（多维表格 app token 不能为空）。")
	}
	if strings.TrimSpace(in.TableID) == "" {
		return larkSoftError("lark_read_bitable 需要 table_id 参数（表格 id 不能为空）。")
	}

	pageSize := larkReadBitableDefaultPageSize
	if in.PageSize != "" {
		if v, err := in.PageSize.Int64(); err == nil && v > 0 {
			pageSize = int(v)
		}
		// A malformed page_size is tolerated (falls back to the default) rather than
		// erroring — keeps the run alive on loose model output.
	}
	if pageSize > larkReadBitableMaxPageSize {
		pageSize = larkReadBitableMaxPageSize
	}

	userID, _ := middleware.UserIDFromCtx(ctx)
	endSpan := larkStartSpan(ctx, "read_bitable", userID, map[string]any{
		"app_token": in.AppToken,
		"table_id":  in.TableID,
		"page_size": pageSize,
	})

	api, soft, proceed := larkAPIFor(ctx, t.provider, label)
	if !proceed {
		endSpan(map[string]any{"outcome": "precondition_failed"}, "precondition failed")
		return soft, nil
	}

	res, err := api.ReadBitable(ctx, in.AppToken, in.TableID, pageSize, in.PageToken)
	if err != nil {
		endSpan(map[string]any{"outcome": "error"}, err.Error())
		return larkSoftErrorForAPIErr(label, err)
	}

	endSpan(map[string]any{"records": len(res.Records), "has_more": res.HasMore, "outcome": "ok"}, "")
	out, _ := json.Marshal(larkReadBitableOutput{
		Records:   res.Records,
		HasMore:   res.HasMore,
		PageToken: res.PageToken,
		Total:     res.Total,
	})
	return ToolResult(out), nil
}
