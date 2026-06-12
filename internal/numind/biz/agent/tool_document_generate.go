package agent

// document_generate 在 #3 是 **partial stub**（与 image_gen / bash_exec 同等处理）。
//
// 原因：aiservice.Chat 内部通过 `taskID` 在 `ai_service_task` 表查路由 + 计费规则。
// `agent.document_generate` 是新 taskID，**未注册到 profile.allTaskIDsList**，
// **未在 migrations/seed_pricing_rules.sql 配 qwen-long 计费规则**。
// 直接调 aiservice.Chat 在运行时会触发 Gateway.ResolveTask error。
//
// 完整实现路径（feature #12 agent-mode-billing-integration 时落地）：
//   1. profile/constants.go 新增 `AgentDocumentGenerate = "agent.document_generate"` + 加 allTaskIDsList
//   2. seed_pricing_rules.sql INSERT qwen-long 计费规则行
//   3. dev 数据库手工跑 SQL（[[project_dev_deploy_migration_gap]]）
//   4. 移除本文件的 stub 标记，恢复真实 aiservice.Chat 调用（保留下面的 wiring 模板代码）
//
// 当前 stub 状态：FullTool 接口完整，Execute 返回明确错误，便于 LLM 不调用此工具。

import (
	"context"
	"encoding/json"
)

type documentGenerateTool struct {
	BaseTool
}

type documentGenerateInput struct {
	Prompt string `json:"prompt"`
	// Format is "markdown" or "plain"; defaults to "markdown" when empty.
	Format string `json:"format,omitempty"`
}

var _ FullTool = (*documentGenerateTool)(nil)

func (t *documentGenerateTool) Name() string { return "document_generate" }
func (t *documentGenerateTool) Description() string {
	return "[stub] Generate a long-form document. Requires aiservice task registration (planned for #12 billing-integration)."
}
func (t *documentGenerateTool) UserFacingName() string  { return "文档生成" }
func (t *documentGenerateTool) NarrationVerb() string   { return "生成" }
func (t *documentGenerateTool) IsReadOnly() bool        { return false }
func (t *documentGenerateTool) MaxResultSizeChars() int { return 50000 }

// IsEnabled 默认 false — 与 image_gen / bash_exec 同等处理；
// 待 #12 注册 taskID 后改回默认 true 或受新 cfg 字段控制。
func (t *documentGenerateTool) IsEnabled(_ ToolConfig) bool { return false }

// InputSchema returns the JSON Schema describing this tool's parameters,
// so the LLM receives a structured function-calling contract (not just prose).
func (t *documentGenerateTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"prompt": {"type": "string", "description": "Description of the document content to generate."},
			"format": {"type": "string", "description": "Optional output format hint (e.g. markdown, html)."}
		},
		"required": ["prompt"]
	}`)
}

func (t *documentGenerateTool) Execute(_ context.Context, input ToolInput) (ToolResult, error) {
	// 即使 IsEnabled=false 防护被绕过，Execute 也返回明确 error。
	var in documentGenerateInput
	// Stub tool: every failure stays soft — even a well-formed call must not
	// kill the run (tool-soft-error-sweep).
	if err := json.Unmarshal(input, &in); err != nil {
		return softToolError("document_generate", "invalid input: %v", err)
	}
	if in.Prompt == "" {
		return softToolError("document_generate", "prompt is required")
	}
	// Keep engineering detail (task registration, blocking issue) out of the
	// LLM-visible payload (T3 review P1); the stub status lives in code comments.
	return softToolError("document_generate", "此工具当前不可用，请勿重试，请改用其他工具完成任务")
}
