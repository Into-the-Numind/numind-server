# 实施计划：thinking-activation-style

feature: thinking-activation-style | repo: numind-server | 依赖链：T1 → T2 → T3（串行）；T4 为验证策略

## T1 — schema：GORM 模型字段 + migration SQL
- **描述**：`model/ai_service.go` 在 `ThinkingOnly` 后加 `ThinkingStyle string gorm:"size:32;default:''" json:"thinking_style"`。新建 `migrations/{ts}_add_thinking_style.sql`：幂等 `ALTER TABLE ai_service ADD COLUMN thinking_style VARCHAR(32) NOT NULL DEFAULT '' AFTER thinking_only`（带存在性 guard）+ agnes 数据修正 UPDATE（thinking_style='enable_thinking_kwarg', supports_thinking=1, thinking_only=0）。
- **涉及文件**：`internal/pkg/model/ai_service.go`、`migrations/{ts}_add_thinking_style.sql`
- **验收**：`go build ./...` 通过；migration SQL 幂等（IF NOT EXISTS 或 information_schema guard）；字段 tag 正确。
- **独立性**：加结构体字段 + 新 SQL 文件，编译通过，字段暂未被读取——可独立验证。

## T2 — registry 读取路径
- **描述**：store.go 两处 raw SQL（GetResolvedRoute、GetResolvedRouteByModelKey）SELECT 加 `s.thinking_style AS thinking_style`；两处 `rawRow` 加 `ThinkingStyle string gorm:"column:thinking_style"`；`resolvedRouteRow` 加字段并在两处构造填充；`registry.go` `ResolvedRoute` 加 `ThinkingStyle string` 并在构造处（~L449）填充。store_test：断言 thinking_style 从 DB 读到 ResolvedRoute（含空值默认 ''）。
- **涉及文件**：`internal/pkg/aiservice/registry/store.go`、`internal/pkg/aiservice/registry/registry.go`、`internal/pkg/aiservice/registry/store_test.go`
- **验收**：`go test ./internal/pkg/aiservice/registry/...` 通过；ResolvedRoute.ThinkingStyle 正确反映 DB 列。
- **独立性**：读取链端到端，ThinkingStyle 到达 ResolvedRoute 但适配器尚未消费，编译+测试通过——可独立验证。

## T3 — adapter wire 发射
- **描述**：`adapter.go` `oaiChatRequest` 加 `ChatTemplateKwargs map[string]interface{} json:"chat_template_kwargs,omitempty"`。`dmxapi.go buildOAIRequest` 重写思考分支（spec §3.2）：thinking_only→intrinsic（不变）；否则 switch thinking_style：enable_thinking_kwarg→chat_template_kwargs.enable_thinking=true；none→不发；default(""/reasoning_effort)→reasoning_effort="medium"。`dmxapi_thinking_test.go` 加 AC1-AC5、AC7 wire-body 断言。
- **涉及文件**：`internal/pkg/aiservice/adapter/adapter.go`、`internal/pkg/aiservice/adapter/dmxapi.go`、`internal/pkg/aiservice/adapter/dmxapi_thinking_test.go`
- **验收**：`go test ./internal/pkg/aiservice/adapter/...` 通过；各 thinking_style 路径 wire body 与 AC 一致；既有 reasoning_effort/intrinsic/Claude-temp 测试不回归。
- **独立性**：功能闭环 + 测试，可独立验证。

## T4 — S5 验证策略（Rule 10）
- **方式**：后端 Go 单测为主（dmxapi_thinking_test.go + store_test.go），无 UI 改动不做 E2E/Playwright。dev 部署后用 agnes-2.0-flash 实跑一次 SOP/对话确认返回 reasoning_content（人工/AI 取证，非持久化测试）。
- **理由**：纯后端请求体构造逻辑，单测能精确断言 wire body 的每条 thinking_style 分支（持久化回归保护）；无前端交互可验。dev 实跑作为端到端 sanity（agnes 真实返回思考内容），属一次性验证。
- **关键路径**：① 各 thinking_style 枚举值 → 正确 wire 字段（单测）；② agnes-2.0-flash dev 实跑 → reasoning_content 非空（dev sanity）；③ 既有思考模型（如某 reasoning_effort 模型）回归不变（单测）。
- **回归保护诚实声明**：单测提供持久回归保护；dev 实跑是一次性，agnes 未来改动需手动重跑。功能不涉及支付/权限高风险，单测覆盖足够。

## 依赖图（无环）
T1 →（字段就绪）→ T2 →（ResolvedRoute.ThinkingStyle 就绪）→ T3。T4 为文档，无代码依赖。
