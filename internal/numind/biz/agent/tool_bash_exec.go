package agent

import (
	"context"
	"errors"
)

type bashExecTool struct {
	BaseTool
}

var _ FullTool = (*bashExecTool)(nil)

func (t *bashExecTool) Name() string { return "bash_exec" }
func (t *bashExecTool) Description() string {
	return "[stub] Execute shell command in sandbox. Requires #4 sandbox-integration."
}
func (t *bashExecTool) UserFacingName() string        { return "代码执行" }
func (t *bashExecTool) NarrationVerb() string         { return "执行" }
func (t *bashExecTool) IsDestructive() bool           { return true }
func (t *bashExecTool) IsReadOnly() bool              { return false }
func (t *bashExecTool) IsEnabled(cfg ToolConfig) bool { return cfg.EnableSandbox }
func (t *bashExecTool) InterruptBehavior() string     { return "cancel" }

func (t *bashExecTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
	return nil, errors.New("bash_exec requires #4 sandbox-integration")
}
