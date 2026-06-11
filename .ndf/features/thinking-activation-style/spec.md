# 技术设计 Spec：thinking_style 注册表列 + 适配器多激活方式

date: 2026-06-11
feature: thinking-activation-style

## §1 设计目标
把"思考激活的 wire 方式"从适配器硬编码（只会 reasoning_effort）变为注册表数据驱动的枚举列 `ai_service.thinking_style`，零回归既有思考模型，并修复 agnes-2.0-flash。

## §2 数据模型变更

### 2.1 新列
`ai_service.thinking_style VARCHAR(32) NOT NULL DEFAULT ''`（置于 thinking_only 之后）。

枚举语义（仅在 `Thinking=true && supports_thinking=1 && thinking_only=0` 时生效）：
| 值 | 含义 | wire 行为 |
|----|------|----------|
| `''`（默认/legacy） | 等同 reasoning_effort（保今日行为） | `reasoning_effort:"medium"` |
| `reasoning_effort` | 显式 OpenAI 风格 | `reasoning_effort:"medium"` |
| `enable_thinking_kwarg` | Qwen/vLLM 风格 | `chat_template_kwargs:{"enable_thinking":true}` |
| `none` | 支持思考但不注入激活字段（罕见，预留） | 不发 |

> **关键不回归保证**：`''` ≡ reasoning_effort，与今日"所有 supports_thinking=1 && thinking_only=0 模型一律发 reasoning_effort"完全一致 → 无需回填既有行也不变行为。

### 2.2 列创建途径
- **GORM 模型**：`model/ai_service.go` 加 `ThinkingStyle string gorm:"size:32;default:''" json:"thinking_style"`（紧随 ThinkingOnly）。AutoMigrate 启动时建列（dev/prod 部署不跑 migration 文件，靠 AutoMigrate；见 project_dev_deploy_migration_gap）。
- **migration SQL**：`migrations/{ts}_add_thinking_style.sql` —— 幂等 `ADD COLUMN IF NOT EXISTS` + agnes 数据修正 UPDATE。作为权威记录 + 手工执行入口（AutoMigrate 不做数据修正）。

### 2.3 agnes 数据修正（migration 内 + S6 手工 dev 执行）
```sql
UPDATE ai_service
SET thinking_style='enable_thinking_kwarg', supports_thinking=1, thinking_only=0
WHERE model_key='agnes-2.0-flash';
```

## §3 代码契约

### 3.1 registry 读取链（store.go）
两处 raw SQL（`GetResolvedRoute` L308 / `GetResolvedRouteByModelKey` L415）SELECT 加 `s.thinking_style AS thinking_style`；对应 `rawRow` 加 `ThinkingStyle string gorm:"column:thinking_style"`；`resolvedRouteRow` 加字段；`ResolvedRoute`（registry.go）加 `ThinkingStyle string` 并在构造处（L449 等）填充。

### 3.2 adapter wire（adapter.go + dmxapi.go）
`oaiChatRequest` 加：
```go
ChatTemplateKwargs map[string]interface{} `json:"chat_template_kwargs,omitempty"`
```
`dmxapi.go buildOAIRequest` 思考分支重写（替换 L141-150）：
```go
if req.Thinking && route.SupportsThinking {
    if route.ThinkingOnly {
        meta.ResolvedReasoningEffort = "intrinsic"          // 不变
    } else {
        switch route.ThinkingStyle {
        case "enable_thinking_kwarg":
            oaiReq.ChatTemplateKwargs = map[string]interface{}{"enable_thinking": true}
            meta.ResolvedReasoningEffort = "enable_thinking_kwarg"
        case "none":
            // 支持思考但不注入；meta 留空
        default: // "reasoning_effort" 或 "" (legacy)
            oaiReq.ReasoningEffort = "medium"
            meta.ResolvedReasoningEffort = "medium"
        }
    }
}
```
Claude temp=1 分支（L158）**不动**——独立于 thinking_style，且 Claude 模型 thinking_style 为空/reasoning_effort，逻辑不变。

## §4 LLM 可观测性（ai-service.md 合规）
本改动不新增 LLM 调用入口，仅改请求体构造。`TraceMetadata.ResolvedReasoningEffort` 已是既有 trace 字段，新增 `"enable_thinking_kwarg"` 取值使思考激活方式在 Langfuse 可审计。无需新 trace topology。

## §5 不在本 spec
- admin 端表单/接口暴露 thinking_style（follow-up）。
- 其它 provider 的思考方式调整（仅 agnes 数据修正；其它模型靠 `''` 保持原状）。

## §6 验收映射
AC1→§3.2 enable_thinking_kwarg case；AC2→default case + omitempty；AC3→none case；AC4→thinking_only 分支不变；AC5→门控不变；AC6→§2.3；AC7→Claude temp=1 分支不动。
