package agent

import (
	"context"
	"fmt"
	"time"
)

type getCurrentDateTool struct {
	BaseTool
}

// 编译期断言（FullTool 由 Task 2 提供）
var _ FullTool = (*getCurrentDateTool)(nil)

func (t *getCurrentDateTool) Name() string { return "get_current_date" }
func (t *getCurrentDateTool) Description() string {
	return "Return today's date in ISO 8601 format (YYYY-MM-DD)."
}
func (t *getCurrentDateTool) UserFacingName() string { return "当前日期" }
func (t *getCurrentDateTool) NarrationVerb() string  { return "获取" }
func (t *getCurrentDateTool) IsReadOnly() bool       { return true }

func (t *getCurrentDateTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
	return ToolResult(fmt.Sprintf(`"%s"`, time.Now().UTC().Format("2006-01-02"))), nil
}
