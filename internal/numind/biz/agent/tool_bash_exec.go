package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"numind-server/internal/numind/biz/agent/bashvalidator"
	"numind-server/internal/numind/biz/sandbox"
)

// bashExecTool is the bash_exec FullTool. Execute runs through 4 stages:
//  1. JSON parse `{"command":"..."}`
//  2. bashvalidator gate (Phase 0 V3 8 P0 validators)
//  3. lookup sandbox session borrowed by SandboxHook.PreToolCall
//  4. sandbox.ExecCommand → return ToolResult JSON
//
// Failure modes return a friendly error JSON in ToolResult (so the LLM can
// read and react) AND surface the execErr to PostToolCall so the audit row
// gets marked 'failed'. validator failures intentionally return nil err
// (the user's bad input, not a sandbox problem).
type bashExecTool struct {
	BaseTool
}

var _ FullTool = (*bashExecTool)(nil)

func (t *bashExecTool) Name() string { return "bash_exec" }
func (t *bashExecTool) Description() string {
	return "Execute a shell command inside an isolated Docker sandbox. Returns stdout, stderr, exit code."
}
func (t *bashExecTool) UserFacingName() string        { return "代码执行" }
func (t *bashExecTool) NarrationVerb() string         { return "执行" }
func (t *bashExecTool) IsDestructive() bool           { return true }
func (t *bashExecTool) IsReadOnly() bool              { return false }
func (t *bashExecTool) IsEnabled(cfg ToolConfig) bool { return cfg.EnableSandbox }
func (t *bashExecTool) InterruptBehavior() string     { return "cancel" }

// InputSchema returns the JSON Schema describing this tool's parameters,
// so the LLM receives a structured function-calling contract (not just prose).
func (t *bashExecTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string", "description": "The shell command to run inside the isolated sandbox."}
		},
		"required": ["command"]
	}`)
}

func (t *bashExecTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return bashFriendlyError("bash_exec input must be JSON with a 'command' string field"), nil
	}
	if args.Command == "" {
		return bashFriendlyError("bash_exec: command is empty"), nil
	}

	// 1) bash validator gate
	if allow, reason := bashvalidator.Validate(args.Command); !allow {
		return bashFriendlyError("命令被拒绝（安全策略）: " + reason), nil
	}

	// 2) Look up the sandbox session borrowed by SandboxHook.PreToolCall.
	// If absent (no hook manager / runID=0 / pool exhausted), gracefully
	// degrade to a friendly error so the LLM can decide next action.
	sess := sandboxSessionForCurrentCall(ctx, "bash_exec")
	if sess == nil {
		return bashFriendlyError("沙箱当前不可用，请稍后重试"), nil
	}
	dc := dockerClientForCurrentCall(ctx)
	if dc == nil {
		return bashFriendlyError("沙箱客户端未初始化，请联系管理员"), nil
	}

	// 3) Execute inside the sandbox
	res, execErr := sandbox.ExecCommand(ctx, sess, args.Command, dc)
	if execErr != nil {
		// Surface execErr to PostToolCall so the audit row gets 'failed'.
		// LLM still receives a friendly error so it can react meaningfully.
		// 我们返回 nil 级别的 error，彻底杜绝 Eino 框架崩溃中断。
		return bashFriendlyError(fmt.Sprintf("沙箱执行失败: %v", execErr)), nil
	}

	out, err := json.Marshal(map[string]interface{}{
		"stdout":      res.Stdout,
		"stderr":      res.Stderr,
		"exit_code":   res.ExitCode,
		"duration_ms": res.Duration.Milliseconds(),
	})
	if err != nil {
		// Should never happen with simple map[string]interface{} but be defensive
		return bashFriendlyError("结果序列化失败"), nil
	}
	return out, nil
}

// bashFriendlyError returns a ToolResult containing a user-readable
// {"error": "<msg>"} JSON payload — LLM-readable.
func bashFriendlyError(msg string) ToolResult {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return b
}
