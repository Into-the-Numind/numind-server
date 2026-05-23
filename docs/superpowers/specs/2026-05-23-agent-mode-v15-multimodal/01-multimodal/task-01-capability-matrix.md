# Task 1.1: Capability Matrix 重构（DB 字段 + 路由 helper）

## 概要

扩展 `ai_service.capability_json` 字段为强类型 capability matrix（accepts_image/pdf/audio_inline + max_inline_size + preferred_format），seed 5 新模型 + 现有 10+ 模型的 capability，在 `biz/aiservice` 提供三个 helper API（`CanAcceptModality` / `ResolveFallbackBehavior` / `GetCapabilities`），带 5min in-memory cache + admin PATCH 接口。本 task 是 task 1.3/1.4 的硬前置，所有多模态路由决策都依赖这个数据源。

## 依赖

- **前置依赖**：无（独立可启动）
- **被依赖**：
  - Task 1.3（buildAgentInput capability-aware 路由）— 调 `CanAcceptModality`
  - Task 1.4（Tool gating）— 调 `GetCapabilities().AcceptsImageInline`
  - Task 1.5（Runtime 错误剥离）— 调 `ResolveFallbackBehavior`
- **可并行**：Task 1.2（attachment 双模态固化，无 capability 数据依赖）

## 输入 / 输出契约

### DB Schema 扩展

`ai_service.capability_json` 字段已存在（JSON），结构化为：

```json
{
  "accepts_image_inline": true,
  "accepts_pdf_inline": false,
  "accepts_audio_inline": false,
  "max_inline_size_bytes": 20971520,
  "supports_vision_tool_calling": true,
  "preferred_image_format": "base64"
}
```

无需新增列；增加 GORM struct tag + 强类型解析。Migration 是数据 backfill SQL（UPDATE 现有行 + INSERT 新模型）。

### Go 类型签名

```go
// internal/pkg/aiservice/capability/types.go
type Capabilities struct {
    AcceptsImageInline       bool   `json:"accepts_image_inline"`
    AcceptsPDFInline         bool   `json:"accepts_pdf_inline"`
    AcceptsAudioInline       bool   `json:"accepts_audio_inline"`
    MaxInlineSizeBytes       int64  `json:"max_inline_size_bytes"`
    SupportsVisionToolCalling bool  `json:"supports_vision_tool_calling"`
    PreferredImageFormat     string `json:"preferred_image_format"` // "base64" | "url"
}

type MediaType string
const (
    MediaImage MediaType = "image"
    MediaPDF   MediaType = "pdf"
    MediaAudio MediaType = "audio"
)

type FallbackPolicy string
const (
    FallbackInline   FallbackPolicy = "inline"        // 原生 inline
    FallbackToText   FallbackPolicy = "to_text"       // 走 text_fallback
    FallbackToOCROnly FallbackPolicy = "to_ocr_only"  // 只用 OCR
    FallbackReject   FallbackPolicy = "reject"        // 拒绝
)

// internal/pkg/aiservice/capability/api.go
func GetCapabilities(modelKey string) (*Capabilities, error)
func CanAcceptModality(modelKey string, mediaType MediaType) (bool, error)
func ResolveFallbackBehavior(modelKey string, mediaType MediaType) FallbackPolicy
func InvalidateCache(modelKey string)  // admin 编辑后调用
```

### Admin API

```
PATCH /v1/admin/ai-services/:id
Body: { "capability_json": { ...上述 6 字段... } }
Response: { code: 0, data: { capabilities: {...} } }
```

成功后自动 `InvalidateCache(modelKey)`。

## 设计要点

### 路由 helper 决策矩阵

`ResolveFallbackBehavior(modelKey, mediaType)`：

| AcceptsInline | mediaType | 返回 |
|---|---|---|
| true | image | `FallbackInline` |
| false | image | `FallbackToText`（用 vision_description + ocr_text） |
| false | pdf | `FallbackToOCROnly`（只用 ocr_text） |
| false | audio | `FallbackReject`（V1.5 无音频 fallback） |

### Cache 策略

- 5min TTL in-memory `sync.Map`（key = modelKey, value = `*cacheEntry{caps, expiresAt}`）
- `GetCapabilities` 内：先查 cache → miss/expired → 查 DB → 写 cache
- `InvalidateCache(modelKey)` 删 entry
- 默认值 fallback：DB 查不到 modelKey 时返回保守默认（`accepts_*_inline=false, MaxInlineSizeBytes=0`）+ error，调用方决定 reject/降级

### Capability 默认值（DB 查不到时）

```go
var defaultConservative = Capabilities{
    AcceptsImageInline:       false,
    AcceptsPDFInline:         false,
    AcceptsAudioInline:       false,
    MaxInlineSizeBytes:       0,
    SupportsVisionToolCalling: false,
    PreferredImageFormat:     "base64",
}
```

### 边界 case

- **modelKey 不存在**：返回 `ErrModelNotFound` + conservative defaults，调用方按 `to_text` 处理
- **capability_json 字段空 / 解析失败**：日志 WARN + conservative defaults，不抛错（向后兼容旧记录）
- **并发 cache 写入**：`sync.Map` 天然安全；多协程同时 miss 不去重（接受多次 DB 查询，可接受成本）
- **DB 写 capability 时 default:true 陷阱**：参考 `.claude/rules/database.md §6` — admin PATCH 用 `db.Updates(map[string]any{...})` map 形式，不用 struct 形式

## 实施步骤

### Step 1：DB Migration（`numind-server/migrations/`）

新文件 `20260524_010000_capability_matrix_seed.sql`：

- UPDATE 已有 11 个模型行的 `capability_json`：
  - `qwen3-vl-flash-2026-01-22`, `qwen-vl-plus`, `doubao-seed-1-8-251228` → `accepts_image_inline=true, max=20MB, base64`
  - `qwen-long` → `accepts_pdf_inline=true, max=100MB`
  - 其他文本模型 → 全 false
- INSERT 5 个新模型 stub（`mimo-v2-5-pro`, `kimi-k2-5`, `kimi-k2-6`, `glm-5-1`, `minimax-m2-7`, `qwen-3-7-max`）到 `ai_service`，capability 按 README 第 10 节标注
- Idempotent：用 `INSERT ... ON DUPLICATE KEY UPDATE`

### Step 2：types + helper 实现（`numind-server/internal/pkg/aiservice/capability/`）

- 新建 `types.go` — 定义 `Capabilities` / `MediaType` / `FallbackPolicy`
- 新建 `api.go` — 实现 3 个公共函数 + cache 逻辑
- 新建 `cache.go` — `sync.Map` + TTL entry struct
- 查 DB：通过 `aiservice/registry` 包已有的 `LookupServiceByModelKey(modelKey)` 复用（避免裸 GORM 查询）

### Step 3：单元测试（`numind-server/internal/pkg/aiservice/capability/api_test.go`）

测试矩阵：6 个 model × 4 个 media type = 24 case，包括：
- `qwen3-vl-flash` × image → `FallbackInline`
- `qwen3-vl-flash` × pdf → `FallbackToOCROnly`（VL 模型不接 PDF 文件）
- `glm-5-1` × image → `FallbackToText`
- `qwen-long` × pdf → `FallbackInline`
- `qwen-long` × image → `FallbackToText`
- `not-exist-model` × image → `ErrModelNotFound` + `FallbackToText`
- Cache hit / miss / invalidate
- 并发 GetCapabilities 100 goroutine 无 race

### Step 4：Admin API（`numind-server/internal/numind/controller/admin_aiservice.go`）

- 找到现有 `UpdateAIService` handler（或 PATCH endpoint）
- 加 capability_json 字段绑定 + validation（serialize/deserialize 走 Capabilities struct）
- 写入后调 `capability.InvalidateCache(modelKey)`
- 路由注册：确认 `admin_router.go` 已有 PATCH `/v1/admin/ai-services/:id`（应该已有，本 task 不新增 endpoint，只扩展 body）

### Step 5：lint + go test

- `cd numind-server && task lint`
- `cd numind-server && go test ./internal/pkg/aiservice/capability/...`

## 验证策略（S5）

### 单元测试（必须）

- `api_test.go` 覆盖 24 case 矩阵（见 Step 3）
- Cache TTL 测试用 `time.Sleep` 或可注入 clock
- 目标 coverage > 85%

### 集成测试（推荐）

- `internal/pkg/aiservice/capability/integration_test.go`（连接 dev MySQL）：
  - 跑 migration → 验证 5 个新模型行存在
  - 调 `GetCapabilities("glm-5-1")` 验证字段正确反序列化
  - PATCH admin API 改 capability → 调 `GetCapabilities` 立即看到新值（cache invalidate 生效）

### 手动 dev 验证

1. 跑 migration：`sshpass -p "$DEV_SSH_PASS" ssh "$DEV_SSH_USER@$DEV_SSH_HOST" "mysql -e 'source migrations/20260524...'"`
2. curl admin API：
   ```bash
   curl -X PATCH "$DEV_API_URL/v1/admin/ai-services/<id>" \
        -H "Authorization: Bearer $admin_token" \
        -d '{"capability_json":{"accepts_image_inline":false,...}}'
   ```
3. 启动 server 本地 → 在 Go REPL / 临时 main 调 `capability.GetCapabilities("glm-5-1")` 看返回

**不需要 gstack /qa**：本 task 无前端 UI 改动，纯后端 + DB。

## 工期估算

- **总工期**：1-1.5 工作日
- **分项**：
  - DB migration + seed SQL：0.2d
  - types + helper + cache 实现：0.4d
  - 单元测试（24 case + concurrent + cache）：0.3d
  - Admin API 扩展 + 路由确认：0.2d
  - lint + 集成测试 + dev 验证：0.2d

## 风险 / 待决策项

- **R1：5 个新模型的 capability 标注准确性** — 需要用户确认（context.md 第 10 节已标注，但 `supports_vision_tool_calling` / `max_inline_size_bytes` 真实值需查 provider 官方文档，待 task 1.3 实测前敲定）。**建议**：先按 README 假设值 seed，task 1.5 联调时若发现实际不匹配再 PATCH 修正
- **R2：cache TTL 5min 是否合适** — 多 server 实例时 admin PATCH 后其他实例最多 5min 才生效。**建议**：V1.5 先接受这个延迟；V2 加 Redis pub/sub 通知或缩到 1min
- **R3：`preferred_image_format` 字段当前是否使用** — 暂时只 reserve，task 1.3 默认全用 base64。**待 task 1.3 决策**：若发现某 provider 要 URL 模式，再启用此字段路由
- **R4：MaxInlineSizeBytes 是否在 task 1.3 强制 check** — 本 task 只存储字段，**实际 size check 由 task 1.3/1.5 实现**（避免本 task 范围爆炸）
- **R5：`db.Updates` map 形式必须用** — 实施时若有 `IsActive` 等 default:true bool 字段同时改动，必须用 map 形式（参考 database.md §6），admin PATCH handler 必须在 code review 时双查
