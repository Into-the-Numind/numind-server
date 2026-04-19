# AI 服务统一化管理（AI Service Manager）— 技术设计 Spec

> 本 spec 基于 S1 proposal（`numind-server/proposals/ai-service-manager-proposal.md`）产出。
> 覆盖：数据模型、Gateway 架构、API 契约、迁移策略、Trace Topology、中间件行为、管理端 UI、PRD 覆盖证明。
> S2 gate 检查依据：§14 PRD 覆盖证明表。

**涉及仓库**：numind-server、numind-admin-web

---

## §1 架构概览

### 1.1 系统图（逻辑视角）

```
┌──────────────────────────────────────────────────────┐
│ biz 业务层（sop/salesrag/chatbot/monitor/...）       │
│   调用入口：ai.Chat / ai.Embed / ai.Rerank /         │
│            ai.OCR / ai.ASR (统一 Gateway)            │
└──────────────────────┬───────────────────────────────┘
                       │ taskID + request
                       ▼
┌──────────────────────────────────────────────────────┐
│ internal/pkg/aiservice/ （新增 Gateway 包）          │
│ ┌────────────────────────────────────────────────┐   │
│ │  Task Profile Resolver（读 DB → route 候选）   │   │
│ └──────────────────────┬─────────────────────────┘   │
│                        ▼                             │
│ ┌────────────────────────────────────────────────┐   │
│ │  Middleware Chain                              │   │
│ │  Tracing → Billing → Fallback → Retry → Adapter│   │
│ └──────────────────────┬─────────────────────────┘   │
│                        ▼                             │
│ ┌────────────────────────────────────────────────┐   │
│ │  Provider Adapters                             │   │
│ │  ali / volc / dmxapi / baidu / bailian / funasr│   │
│ └────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────┘
                       │
                       ▼
            外部 provider API + Langfuse + UsageRecord
```

### 1.2 数据流

1. 业务代码调 `ai.Chat(ctx, "sop.text", req)`
2. Gateway 读 Task Profile（含 requirements + default + fallback + allowed services）
3. Gateway 进入中间件链：
   - Tracing：创建 generation/span，异步 flush Langfuse
   - Billing：准备 UsageRecord context，读 pricing snapshot
   - Retry：失败重试（单层 retry，adapter 不再 retry）
   - Fallback：主服务失败切 fallback（最多 1 次跳转）
   - Adapter：调 provider API
4. Adapter 返回 → 逆序执行中间件（记 usage/close trace）
5. 返回业务层

### 1.3 保留不变的部分

- `user_model_preference` 表（C 端用户偏好）
- `/v1/llm/models` / `/v1/llm/preference` 用户端 API shape
- `ModelSelector.vue` + 关联 Pinia store
- `internal/pkg/llm/dmxapi_client.go`（作为 dmxapi adapter 的底层）
- `internal/numind/biz/llmrouter/`（保留运行，作 LLM 子能力 shim；S4 渐进迁移）

---

## §2 数据模型

### 2.1 现有表演进（schema migration）

**Migration 文件**：`migrations/20260416_000001_ai_service_manager.sql`

#### 2.1.1 `llm_provider` → `llm_provider` + 新列

保留表名，加列即可（向后兼容）：

```sql
ALTER TABLE llm_provider
  ADD COLUMN provider_type VARCHAR(20) NOT NULL DEFAULT 'llm'
    COMMENT 'llm | ocr | asr | file_service',
  ADD COLUMN supports_streaming TINYINT(1) DEFAULT 1,
  ADD INDEX idx_provider_type (provider_type);
```

#### 2.1.2 `llm_model` → 改名 `ai_service` + 扩展

```sql
-- 表改名
ALTER TABLE llm_model RENAME TO ai_service;

-- 新增列
ALTER TABLE ai_service
  ADD COLUMN service_type VARCHAR(20) NOT NULL DEFAULT 'llm'
    COMMENT 'llm | ocr | asr（区分能力大类）',
  ADD COLUMN capability_json JSON NOT NULL DEFAULT (JSON_OBJECT())
    COMMENT '按 service_type 存不同 schema 的能力字段',
  ADD COLUMN latency_tier VARCHAR(20) DEFAULT 'standard'
    COMMENT 'fast | standard | slow',
  ADD COLUMN quality_tier VARCHAR(20) DEFAULT 'standard'
    COMMENT 'basic | standard | pro | flagship',
  ADD COLUMN tags JSON DEFAULT (JSON_ARRAY())
    COMMENT '自由标签，如 ["chinese-optimized", "cheap"]',
  ADD COLUMN deprecated_at DATETIME DEFAULT NULL
    COMMENT '软删除时间；非 NULL 表示已下架',
  ADD INDEX idx_as_service_type (service_type),
  ADD INDEX idx_as_deprecated (deprecated_at);

-- 读兼容 VIEW（旧代码路径读 llm_model 仍能工作）
CREATE OR REPLACE VIEW llm_model AS
  SELECT id, model_key, display_name, is_thinking, base_model_id,
         supports_thinking, icon, sort_order, is_active,
         created_at, updated_at
  FROM ai_service
  WHERE service_type = 'llm' AND deprecated_at IS NULL;
-- ⚠ VIEW 仅只读使用。所有 INSERT/UPDATE/DELETE 走新 GORM model 直指 ai_service。
```

> **继承列说明**：`ai_service` 表除上面 ADD 的列外，还继承原 `llm_model` 的所有列：
> `id / model_key / display_name / is_thinking / base_model_id / supports_thinking / thinking_only / icon / sort_order / is_active / created_at / updated_at`。
> 其中 `thinking_only` 是 2026-04-13 独立 migration（`20260413_000001_add_thinking_only_to_llm_model.sql`）加入的 `TINYINT(1) DEFAULT 0`，对应 Go struct `model.AIService.ThinkingOnly bool`。`llm_model` 兼容 VIEW 目前**未**包含 `thinking_only`，因为 legacy 路径不读该字段；若 legacy 代码未来需要读，VIEW 需补列。

#### 2.1.3 `llm_model_provider` → 改名 `ai_service_route`

```sql
ALTER TABLE llm_model_provider RENAME TO ai_service_route;

ALTER TABLE ai_service_route
  ADD COLUMN pricing_unit VARCHAR(20) NOT NULL DEFAULT 'per_1m_tokens'
    COMMENT 'per_1m_tokens | per_call | per_second',
  ADD COLUMN price_per_call DECIMAL(10,6) DEFAULT NULL
    COMMENT 'OCR 类：元/次（null = 不适用）',
  ADD COLUMN price_per_second DECIMAL(10,6) DEFAULT NULL
    COMMENT 'ASR 类：元/秒（null = 不适用）';

-- 现有字段 input_price_per_mtok / output_price_per_mtok 保持不变，用于 LLM

CREATE OR REPLACE VIEW llm_model_provider AS
  SELECT r.id, r.model_id, r.provider_id, r.provider_model_id, r.priority,
         r.input_price_per_mtok, r.output_price_per_mtok, r.is_active,
         r.created_at, r.updated_at
  FROM ai_service_route r
  JOIN ai_service s ON s.id = r.model_id
  WHERE s.service_type = 'llm' AND s.deprecated_at IS NULL;
```

### 2.2 新表

#### 2.2.1 `task_profile`

```sql
CREATE TABLE task_profile (
    id              BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    task_id         VARCHAR(80) NOT NULL UNIQUE
                     COMMENT '如 sop.text / salesrag.embed',
    display_name    VARCHAR(100) NOT NULL,
    description     TEXT,
    service_type    VARCHAR(20) NOT NULL
                     COMMENT 'llm | ocr | asr（限定允许绑定的服务类型）',
    requirements    JSON NOT NULL DEFAULT (JSON_OBJECT())
                     COMMENT '能力需求，如 {"input_modalities":["text","image"],"min_context":8192}',
    default_service_id      BIGINT UNSIGNED NULL,
    user_selectable         TINYINT(1) DEFAULT 0
                     COMMENT 'C 端 ModelSelector 是否暴露此 profile；默认 0',
    extra_metadata  JSON DEFAULT (JSON_OBJECT())
                     COMMENT '逃生舱字段',
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_tp_service_type (service_type),
    CONSTRAINT fk_default_service
      FOREIGN KEY (default_service_id) REFERENCES ai_service(id)
      ON DELETE SET NULL
);
```

#### 2.2.2 `task_profile_service`（绑定 fallback + allowed）

```sql
CREATE TABLE task_profile_service (
    id                BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    task_profile_id   BIGINT UNSIGNED NOT NULL,
    service_id        BIGINT UNSIGNED NOT NULL,
    role              VARCHAR(20) NOT NULL
                       COMMENT 'fallback | allowed',
    priority          INT DEFAULT 0
                       COMMENT 'fallback 优先级（0 最高）；allowed 下用于排序展示',
    created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_profile_service_role (task_profile_id, service_id, role),
    INDEX idx_tps_profile_role (task_profile_id, role),
    CONSTRAINT fk_tps_profile FOREIGN KEY (task_profile_id) REFERENCES task_profile(id) ON DELETE CASCADE,
    CONSTRAINT fk_tps_service FOREIGN KEY (service_id) REFERENCES ai_service(id) ON DELETE CASCADE
);
```

#### 2.2.3 `ai_service_audit_log`

```sql
CREATE TABLE ai_service_audit_log (
    id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    actor_id      BIGINT UNSIGNED NOT NULL COMMENT '操作人 admin_user.id',
    actor_name    VARCHAR(100) NOT NULL,
    action        VARCHAR(50) NOT NULL
                   COMMENT 'service.create | service.update | service.deprecate | task.bind | pricing.update | capability.override',
    target_type   VARCHAR(20) NOT NULL COMMENT 'service | task_profile',
    target_id     BIGINT UNSIGNED NOT NULL,
    diff_json     JSON COMMENT '变更前/后的 diff',
    reason        TEXT COMMENT '可选原因（capability override 时必填）',
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_asal_actor_created (actor_id, created_at),
    INDEX idx_asal_target (target_type, target_id)
);
```

### 2.3 `UsageRecord` 扩展

现有 GORM model 在 `internal/pkg/model/usage.go`。新增字段：

```sql
ALTER TABLE usage_record
  ADD COLUMN service_type VARCHAR(20) DEFAULT NULL
    COMMENT 'llm | ocr | asr（null = 历史数据或非 AI 调用）',
  ADD COLUMN task_id VARCHAR(80) DEFAULT NULL
    COMMENT 'Task Profile id；null = 历史数据',
  ADD COLUMN unit VARCHAR(20) DEFAULT NULL
    COMMENT 'per_1m_tokens | per_call | per_second',
  ADD COLUMN call_count INT DEFAULT NULL,
  ADD COLUMN duration_seconds DECIMAL(10,3) DEFAULT NULL,
  ADD COLUMN pricing_input_snapshot DECIMAL(10,6) DEFAULT NULL,
  ADD COLUMN pricing_output_snapshot DECIMAL(10,6) DEFAULT NULL,
  ADD COLUMN pricing_call_snapshot DECIMAL(10,6) DEFAULT NULL,
  ADD COLUMN pricing_second_snapshot DECIMAL(10,6) DEFAULT NULL,
  ADD COLUMN is_estimated TINYINT(1) DEFAULT 0
    COMMENT '流式中断估算补记时为 1',
  ADD INDEX idx_task_created (task_id, created_at),
  ADD INDEX idx_ur_service_type (service_type);
```

**历史数据**：不 backfill（新字段 null）；跨版本按 feature 维度聚合仍可用，按 task_id 聚合仅新数据。

### 2.4 Seed 数据（同一 migration 内执行）

**新增 provider 行**（ali/volc/baidu/bailian/funasr）：

```sql
-- api_key 留空字符串；启动时由 aiservice.SyncProviderCredentials 从 config.ai_providers 读取填充
INSERT INTO llm_provider (name, display_name, base_url, api_key, provider_type, supports_streaming, is_active)
VALUES
  ('ali-dashscope', '阿里云百炼', 'https://dashscope.aliyuncs.com/compatible-mode/v1', '', 'llm', 1, 1),
  ('volc-ark', '火山方舟', 'https://ark.cn-beijing.volces.com/api/v3', '', 'llm', 1, 1),
  ('baidu-ocr', '百度 OCR', 'https://aip.baidubce.com/rest/2.0/ocr/v1', '', 'ocr', 0, 1),
  ('bailian-file', '阿里百炼文件服务', 'https://bailian.aliyuncs.com', '', 'file_service', 0, 1),
  ('funasr-local', '本地 FunASR', '', '', 'asr', 0, 1);  -- base_url 也由启动时同步
```

**现有 llm_model 补齐 service_type**（已 default='llm'，无需操作）。

**新增 ai_service 行**（OCR + ASR 服务）：

```sql
INSERT INTO ai_service (model_key, display_name, service_type, capability_json, is_active)
VALUES
  ('baidu-ocr-accurate', '百度 OCR 高精度含位置版', 'ocr',
    '{"image_formats":["jpg","png","bmp"],"max_resolution":4096,"max_file_size_mb":10,"capabilities":["ocr"]}', 1),
  ('funasr-paraformer', 'FunASR Paraformer', 'asr',
    '{"audio_formats":["wav","mp3","m4a"],"max_duration_sec":3600,"languages":["zh","en"],"realtime":false,"capabilities":["asr"]}', 1);
```

**Task Profile 种子**（14 条，详见 §5）— 通过 migration 插入。

### 2.5 Migration 顺序与部署约束

**⚠ DDL 非事务性警示**：MySQL 的 `ALTER TABLE` / `RENAME TABLE` / `CREATE VIEW` 不是事务的一部分，语句间存在可观察的时间窗口。因此本 migration 不适合"在线无缝执行"，必须按以下部署流程：

**部署流程（S6 要求短暂维护窗口，估计 2-3 分钟）**：
1. dev 环境完整演练整个 migration + rollback，用 `docker mysql:8` 重放验证
2. prod 部署时先停 numind-server 和 numind-admin 两个进程（停服 2-3 分钟）
3. 执行本 migration（下表）
4. 启动服务（Go 启动逻辑内置 provider 凭据 seed 同步，见下）
5. S6 人工验证 4 个关键路径（见 §12）

**Migration SQL 顺序（单个文件 `20260416_000001_ai_service_manager.sql`）**：

| # | 操作 | 原子分组 | 备注 |
|---|---|---|---|
| 1 | `ALTER llm_provider` 加列 | A | 无时间窗问题 |
| 2 | `ALTER llm_model` 加列（在 RENAME 前） | B | 先加列再改名可避免 rename 后再 alter |
| 3 | `ALTER llm_model_provider` 加列 | B | 同上 |
| 4 | `RENAME TABLE llm_model TO ai_service, llm_model_provider TO ai_service_route` | C | **原子 rename**：MySQL `RENAME TABLE a TO a', b TO b'` 是单语句原子操作 |
| 5 | `CREATE VIEW llm_model AS SELECT ... FROM ai_service WHERE ...` | C | 紧跟 rename，两条语句之间无对 llm_model 的业务流量（已停服） |
| 6 | `CREATE VIEW llm_model_provider AS ...` | C | 同上 |
| 7 | `CREATE TABLE task_profile` | D | 新表，无冲突 |
| 8 | `CREATE TABLE task_profile_service` | D | 同上 |
| 9 | `CREATE TABLE ai_service_audit_log` | D | 同上 |
| 10 | `ALTER usage_record` 加列 | D | 无时间窗问题 |
| 11 | `INSERT INTO llm_provider` 新 provider 行（api_key 留空字符串） | E | 启动时 Go 同步填充 |
| 12 | `INSERT INTO ai_service` OCR + ASR 行 | E | 静态数据 |
| 13 | `INSERT INTO task_profile` 14 条 | E | 静态数据 |
| 14 | `INSERT INTO task_profile_service` fallback/allowed 关系 | E | 静态数据 |

每个原子分组（A-E）按顺序执行。失败时按 Appendix A Rollback 倒序执行。

### 2.6 Seed 机制（凭据同步）

**问题**：migration 纯 SQL 无法读 config，provider 的 `api_key` 必须用非 SQL 手段填充。

**方案**：**Go 启动时 seed 同步**（新增 `internal/pkg/aiservice/seed.go`）。服务启动早期（DB 连接 ready、路由注册前）执行：

```go
// 伪代码
func SyncProviderCredentials(ctx, db, cfg) error {
    // 对 config.ai_providers 里声明的每个 provider：
    //   UPSERT llm_provider SET api_key = cfg.api_key WHERE name = ?
    //   （已存在则更新 api_key；不存在则插入）
    // 幂等、可重复执行、不破坏已有数据
}
```

- **启动失败不阻断服务**：seed 失败 log error，服务继续启动（运维靠 `/healthz/ai` 发现问题）
- **轮换流程**：改 config → 重启服务 → seed 自动同步新 key 到 DB → Gateway 从 DB 读生效
- **单一数据源保证**：config 只在启动时被 seed 读一次，之后所有 Gateway 调用都从 DB 读；避免"config 改了但 DB 没同步"的漂移

---

## §3 Gateway 包结构

### 3.1 目录

```
internal/pkg/aiservice/
├── ai.go                 # 对外入口（Chat/Embed/Rerank/OCR/ASR）
├── types.go              # 请求/响应 struct 定义
├── profile/
│   ├── profile.go        # Task Profile 加载、缓存
│   ├── constants.go      # 所有 taskID 字符串常量
│   └── capability.go     # Capability Matching 算法
├── registry/
│   ├── registry.go       # Service Registry 读写
│   └── cache.go          # In-memory 缓存（TTL=30s）
├── middleware/
│   ├── chain.go          # 中间件链执行器
│   ├── tracing.go        # Langfuse trace/generation/span
│   ├── billing.go        # UsageRecord 写入
│   ├── retry.go          # 单层重试
│   └── fallback.go       # Fallback 跳转
├── adapter/
│   ├── adapter.go        # Adapter interface
│   ├── ali.go            # 阿里云适配
│   ├── volc.go           # 火山适配
│   ├── dmxapi.go         # DMXAPI 适配（复用 internal/pkg/llm/dmxapi_client.go）
│   ├── baidu_ocr.go      # 百度 OCR 适配
│   ├── bailian_file.go   # 百炼文件适配
│   └── funasr.go         # FunASR 适配
├── audit/
│   └── audit.go          # ai_service_audit_log 写入
└── health/
    └── health.go         # /healthz/ai 实现
```

### 3.2 对外入口（`ai.go`）

```go
package aiservice

// 统一入口函数签名（与 internal/pkg/aiservice/ai.go 实际实现一致）
// 非流式调用返回 pointer（允许 nil 表示空响应），流式调用返回 channel。
func Chat(ctx context.Context, taskID string, req ChatRequest) (*ChatResponse, error)
func ChatStream(ctx context.Context, taskID string, req ChatRequest) (<-chan ChatChunk, error)
func Embed(ctx context.Context, taskID string, req EmbedRequest) (*EmbedResponse, error)
func Rerank(ctx context.Context, taskID string, req RerankRequest) (*RerankResponse, error)
func OCR(ctx context.Context, taskID string, req OCRRequest) (*OCRResponse, error)
func ASR(ctx context.Context, taskID string, req ASRRequest) (*ASRResponse, error)
```

**Chat 合入 Vision**：`ChatRequest.Messages` 允许 content 为 multipart（文本 + 图片 url/base64），业务层不区分 chat/vision。Task Profile 的 requirements 决定是否允许多模态。

**流式与非流式**：分两个方法（`Chat` / `ChatStream`），比 `Stream bool` 返回值更清晰。

### 3.3 Task Profile 常量包（`profile/constants.go`）

```go
package profile

const (
    SopText          = "sop.text"
    SopVision        = "sop.vision"
    ChatbotStream    = "chatbot.stream"
    SalesragIntent   = "salesrag.intent"
    SalesragChat     = "salesrag.chat"
    SalesragRerank   = "salesrag.rerank"
    SalesragEmbed    = "salesrag.embed"
    SalesragTagging  = "salesrag.tagging"
    SalesragProfile  = "salesrag.profile"
    SalesragChatstyle = "salesrag.chatstyle"
    MonitorBriefing  = "monitor.briefing"
    MonitorAnalyze   = "monitor.analyze"
    MonitorTranscribe = "monitor.transcribe"
    OcrBaidu         = "ocr.baidu"
)
```

业务层 `ai.Chat(ctx, profile.SopText, req)` 获得 IDE 补全 + 拼写检查。

### 3.4 Provider interface（按能力拆分，遵循 ISP）

> **命名说明（2026-04-17 spec 修订）**：S2 原 spec 用 `Adapter` / `ServiceSpec` 两个命名；S4 实施时在 `internal/pkg/aiservice/gateway.go` 统一为 **`Provider`** / **`*registry.ResolvedRoute`**。本节以实际代码为准。

```go
package aiservice

// 基础 interface：所有 provider 都实现
type Provider interface {
    Name() string                      // "ali" / "volc" / "baidu" / "dmxapi" / ...
    ProviderType() string              // "llm" / "ocr" / "asr"
    Capabilities() []string            // ["chat", "embed", "rerank"]
}

// 能力子接口：provider 按实际能力选择性实现
type ChatProvider interface {
    Provider
    Chat(ctx context.Context, route *registry.ResolvedRoute, req ChatRequest) (*ChatResponse, error)
    ChatStream(ctx context.Context, route *registry.ResolvedRoute, req ChatRequest) (<-chan ChatChunk, error)
}

type EmbedProvider interface {
    Provider
    Embed(ctx context.Context, route *registry.ResolvedRoute, req EmbedRequest) (*EmbedResponse, error)
}

type RerankProvider interface {
    Provider
    Rerank(ctx context.Context, route *registry.ResolvedRoute, req RerankRequest) (*RerankResponse, error)
}

type OCRProvider interface {
    Provider
    OCR(ctx context.Context, route *registry.ResolvedRoute, req OCRRequest) (*OCRResponse, error)
}

type ASRProvider interface {
    Provider
    ASR(ctx context.Context, route *registry.ResolvedRoute, req ASRRequest) (*ASRResponse, error)
}

// ResolvedRoute = Registry 把 task_profile + ai_service + ai_service_route
// + llm_provider 合并后的 call-ready 描述（含 provider 凭据）。详见
// internal/pkg/aiservice/registry/registry.go。
```

Gateway 在路由到 provider 时使用 **type assertion**：
```go
// 伪代码
func (g *Gateway) Chat(ctx context.Context, taskID string, req ChatRequest) (*ChatResponse, error) {
    route, _, err := g.registry.ResolveTask(ctx, taskID)
    if err != nil { return nil, err }
    p := g.providers[route.Provider.Name]
    chat, ok := p.(ChatProvider)
    if !ok {
        return nil, ErrAICapabilityMismatch  // 不会发生，除非 DB 数据被手改
    }
    return chat.Chat(ctx, route, req)
}
```

**好处**：baidu OCR provider 不需要实现 Chat/Embed 的 stub；新增能力类型（如 TTS）只需定义新 interface，现有 provider 不动。

Provider 不做 retry、不做 tracing、不做 billing（上层中间件负责），只做"把 Gateway 请求翻译成 provider API 调用并解析回 Gateway 响应"。

---

## §4 API 契约

### 4.1 用户端（保持不变 + 0 breaking）

| 端点 | 方法 | Request / Response | 实现变化 |
|---|---|---|---|
| `/v1/llm/models` | GET | 同现有 | 内部改从 `ai_service WHERE service_type='llm' AND deprecated_at IS NULL` 读 |
| `/v1/llm/preference` | GET/PUT | 同现有 | 完全不变 |

### 4.2 管理端（新增 + 保持现有兼容）

#### 保留现有 12 个 `/v1/admin/llm/*`
- 内部改为读写 `ai_service`（带 `service_type='llm'` 过滤）；response shape 不变
- 管理端老代码无需改动

#### 新增 `/v1/admin/ai/*`

**服务管理**

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/v1/admin/ai/services` | 列表，支持 `?service_type=llm\|ocr\|asr` + `?status=active\|deprecated` + 分页 |
| GET | `/v1/admin/ai/services/:id` | 详情（含 routes） |
| POST | `/v1/admin/ai/services` | 新增（body 含 service_type + capability_json + routes） |
| PUT | `/v1/admin/ai/services/:id` | 更新 |
| DELETE | `/v1/admin/ai/services/:id` | 软删除（写 deprecated_at） |
| POST | `/v1/admin/ai/services/:id/restore` | 恢复（所有 admin 可执行，必须填 reason + 写 audit log） |

**Task Profile 管理**

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/v1/admin/ai/tasks` | 列表 + 分页 |
| GET | `/v1/admin/ai/tasks/:id` | 详情（含 default/fallback/allowed） |
| PUT | `/v1/admin/ai/tasks/:id` | 更新 requirements + default + fallback + allowed；**触发 Capability Matching 校验**，不兼容返回 422 + 原因 |
| PUT | `/v1/admin/ai/tasks/:id?force=true` | 强制保存不兼容绑定（所有 admin 可执行）；body 必须含 `reason`；写入 audit log（action=`capability.override`）。采用 force 参数而非独立端点，保持 RESTful |

**能力元数据**

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/v1/admin/ai/capability-schema` | 返回按 service_type 分组的能力字段 schema，驱动管理端表单动态渲染 |
| POST | `/v1/admin/ai/services/:id/validate-against/:task_id` | 预检：判断某 service 是否满足某 task 的 requirements，返回 compatible + 原因 |

**审计日志**

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/v1/admin/ai/audit-logs` | 列表 + 筛选（`?target_type=` / `?actor_id=`） |

**健康检查**

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/healthz/ai` | 无鉴权。**Phase 1 实际实现**：返回 `{status, gateway_ready, adapters_loaded, adapter_count}`，仅反映 Gateway 单例是否初始化 + 已注册 adapter 列表。**Phase 2 待加**：按 provider 的最近 1 分钟错误率、Registry 缓存命中率 —— 需要滚动窗口计数器，Task-8 后独立实现（见代码注释 `healthz.go`） |

**Phase 1 响应示例**（字段名从实际 adapter `Name()` 取；按字母排序）：

```json
{
  "status": "ok",
  "gateway_ready": true,
  "adapters_loaded": ["aihubmix","ali","baidu_ocr","bailian_file","dmxapi","dmxapi-ssvip","funasr","volc"],
  "adapter_count": 8
}
```

> `adapter_count = 8` 因 `numind.go` 注册了 6 个 base adapter + 2 个 alias（`aihubmix` 和 `dmxapi-ssvip` 都指向 `dmxapi`），`AdapterNames()` 遍历 providers map 包含 alias。Gateway 未初始化时返回 HTTP 503 + `status: "degraded"` + `message` 字段。

### 4.3 Response 格式

统一走 `core.WriteResponse`（项目规范）。错误码采用项目惯用的**字符串 code**（`AIService.*` 命名空间），与 `Monitor.*`、`Credits.*` 等保持一致。

实际实现（`internal/pkg/errno/ai.go`）：

```go
var (
  ErrAIServiceNotFound                = &Errno{HTTP: 404, Code: "AIService.ServiceNotFound",          Message: "AI 服务不存在"}
  ErrAITaskNotFound                   = &Errno{HTTP: 404, Code: "AIService.TaskNotFound",             Message: "Task Profile 不存在"}
  ErrAICapabilityMismatch             = &Errno{HTTP: 422, Code: "AIService.CapabilityMismatch",       Message: "AI 服务能力不匹配，任务需求无法满足"}
  ErrAIFallbackExhausted              = &Errno{HTTP: 502, Code: "AIService.FallbackExhausted",        Message: "所有 AI 服务（含 fallback）均不可用"}
  ErrAIServiceDeprecated              = &Errno{HTTP: 410, Code: "AIService.ServiceDeprecated",        Message: "AI 服务已下架"}
  ErrAICapabilityOverrideRequiresReason = &Errno{HTTP: 400, Code: "AIService.OverrideRequiresReason", Message: "强制覆盖操作必须填写原因"}
  ErrAIServiceUnbound                 = &Errno{HTTP: 424, Code: "AIService.Unbound",                  Message: "Task Profile 未绑定服务"}
  ErrAIProviderTimeout                = &Errno{HTTP: 504, Code: "AIService.ProviderTimeout",          Message: "AI 服务调用超时"}
  ErrAIProviderError                  = &Errno{HTTP: 502, Code: "AIService.ProviderError",            Message: "AI 服务调用失败"}
  ErrAIRestoreRequiresReason          = &Errno{HTTP: 400, Code: "AIService.RestoreRequiresReason",    Message: "恢复操作必须填写原因"}
)
```

> **注 1**：原 S2 spec 把 code 写成数字 `41001`-`41006`，与项目字符串 code 惯例不一致。S4 实施时改为字符串 code，spec 2026-04-17 同步修订。
>
> **注 2（wire format 实测）**：`core.WriteResponse` 在 err != nil 分支**始终**写 `{"code": 1, "message": "...", "data": null}` —— 整数 `1` 作为错误哨兵，`Errno.Code` 字符串和附加 `data` 都被 **drop**。意思是前端收到的 `error.code` 是整数 `1`，**没有** `"AIService.CapabilityMismatch"` 字符串可匹配。这是项目级 wire format 限制，会导致：
> - 前端只能用 `message` 文本粗略区分业务错误（脆弱）
> - `incompatible_bindings` 等 error-side 详情无法到达前端（见下方 422 示例说明）
>
> 这是一个**已知运行时限制**，不是本功能的 bug；需要独立改 `core.WriteResponse` 让它在 err 时也透出 `Errno.Code` 和 error data（作为独立 tech debt 处理）。

### 4.4 关键端点 Response 示例

**`GET /v1/admin/ai/services/:id`**
```json
{
  "code": 0, "message": "ok",
  "data": {
    "id": 42,
    "model_key": "deepseek-v3",
    "display_name": "DeepSeek V3",
    "service_type": "llm",
    "capability_json": {
      "input_modalities": ["text"], "output_modalities": ["text"],
      "context_window": 65536, "max_output_tokens": 8192,
      "capabilities": ["chat"],
      "features": {"tool_use": true, "json_mode": true, "streaming": true}
    },
    "latency_tier": "standard", "quality_tier": "pro",
    "tags": ["chinese-optimized"],
    "deprecated_at": null,
    "routes": [
      {"id": 101, "provider_id": 3, "provider_name": "dmxapi",
       "provider_model_id": "deepseek-v3-2-251201",
       "priority": 0, "pricing_unit": "per_1m_tokens",
       "input_price_per_mtok": 1.0, "output_price_per_mtok": 4.0,
       "is_active": true}
    ]
  }
}
```

**`PUT /v1/admin/ai/tasks/:id`（绑定不兼容时的错误响应，HTTP 422）**

**实际 wire format（受 `core.WriteResponse` 限制）：**
```json
{
  "code": 1,
  "message": "AI 服务能力不匹配，任务需求无法满足",
  "data": null
}
```

**设计意图（当前无法实现 —— 依赖独立修 `core.WriteResponse`）：**
```json
{
  "code": "AIService.CapabilityMismatch",
  "message": "...",
  "data": {
    "incompatible_bindings": [
      {"role": "default", "service_id": 42, "service_name": "deepseek-v3",
       "reasons": ["缺少输入模态: image", "缺少特性: vision"]}
    ]
  }
}
```

> **影响**：`controller/v1/admin_ai/task_profile.go UpdateTask` 把 `IncompatibleBindings` 传给 `core.WriteResponse(c, err, gin.H{"incompatible_bindings": ...})`，但 `err != nil` 分支强制 `data: null`，这个详情在 wire 上丢失。管理端 TaskEdit 的"force override"确认对话框现在**无法通过响应详情触发**——仅能通过 HTTP 422 + message 文本粗略判断。记入独立 tech debt（见 manifest）。

**`POST /v1/admin/ai/services/:id/validate-against/:task_id`**
```json
{
  "code": 0, "message": "ok",
  "data": {
    "compatible": false,
    "reasons": ["缺少输入模态: image"],
    "task_requirements": {...}, "service_capabilities": {...}
  }
}
```

### 4.5 管理端前端契约（numind-admin-web）

前端 API client 添加在 `src/api/ai.ts`（复用 `src/api/llm.ts` 已有的 axios 基础）。TypeScript types 镜像后端 response shape，在 `src/types/ai.ts`。

---

## §5 Task Profile 设计

### 5.1 14 个 Profile Requirements 定义

| task_id | service_type | requirements JSON | 默认 service | fallback |
|---|---|---|---|---|
| `sop.text` | llm | `{"input_modalities":["text"],"min_context":8192,"features":["tool_use","streaming"]}` | deepseek-v3 | qwen-plus |
| `sop.vision` | llm | `{"input_modalities":["text","image"],"min_context":8192,"features":["streaming","vision"]}` | qwen-vl | doubao-seed-1-8 |
| `chatbot.stream` | llm | `{"input_modalities":["text"],"min_context":8192,"features":["streaming"]}` | qwen-plus | deepseek-v3 |
| `salesrag.intent` | llm | `{"input_modalities":["text"],"min_context":4096,"features":["json_mode"]}` | qwen-turbo | — |
| `salesrag.chat` | llm | `{"input_modalities":["text"],"min_context":16384,"features":["streaming"]}` | deepseek-v3 | qwen-plus |
| `salesrag.rerank` | llm | `{"capability":"rerank"}` | qwen3-rerank | — |
| `salesrag.embed` | llm | `{"capability":"embedding","dimension":1024}` | qwen-embedding-v4 | doubao-embedding（⚠ fallback 必须 dimension=1024 同维，否则不兼容；若仅有的备选维度不同，fallback 留空） |
| `salesrag.tagging` | llm | `{"input_modalities":["text"],"features":["json_mode"]}` | qwen-turbo | — |
| `salesrag.profile` | llm | `{"input_modalities":["text","image"],"features":["streaming","vision"]}` | qwen-vl | doubao-seed-1-8 |
| `salesrag.chatstyle` | llm | `{"input_modalities":["text","image"],"features":["streaming","vision"]}` | qwen-vl | doubao-seed-1-8 |
| `monitor.briefing` | llm | `{"input_modalities":["text"],"min_context":16384}` | deepseek-v3 | qwen-plus |
| `monitor.analyze` | llm | `{"input_modalities":["text"],"features":["json_mode"]}` | deepseek-v3 | qwen-turbo |
| `monitor.transcribe` | asr | `{"audio_formats":["wav","mp3","m4a"],"max_duration_sec":3600}` | funasr-paraformer | — |
| `ocr.baidu` | ocr | `{"image_formats":["jpg","png","bmp"],"max_resolution":4096}` | baidu-ocr-accurate | — |

### 5.2 Capability Matching 算法

**输入**：Task Profile requirements（JSON）+ Service capability_json + service_type
**输出**：`(compatible bool, reasons []string)`

**字段语义约定（避免歧义）**：
- `requirements.xxx`（task 侧）= "task 将对服务提出的最严苛值/需要服务具备的能力"
- `svc.capability_json.xxx`（服务侧）= "服务能支持到的上限/能提供的能力"
- **兼容条件**：对容量类字段（context / max_resolution / max_duration_sec / dimension），`req ≤ svc`；对布尔/枚举能力（modalities / features / capability），`req ⊆ svc`

**伪代码**：

```
function Match(req, svc) -> (bool, reasons):
    reasons = []

    // 1. service_type 必须一致
    if req.service_type != svc.service_type:
        return false, ["service_type mismatch"]

    // 2. LLM 类检查
    if req.service_type == "llm":
        // 模态：task 要求的每个模态必须被服务支持（子集关系）
        for m in (req.input_modalities or []):
            if m not in svc.input_modalities:
                reasons.append("缺少输入模态: " + m)

        // 容量类：task 上限 ≤ svc 上限
        if req.min_context and svc.context_window < req.min_context:
            reasons.append("上下文窗口不足（需要 ≥" + req.min_context + "）")

        // 特性开关
        for f in (req.features or []):
            if not svc.features.get(f, false):
                reasons.append("缺少特性: " + f)

        // 能力大类（chat / embedding / rerank / vision）
        if req.capability:
            if req.capability not in (svc.capabilities or []):
                reasons.append("不支持能力: " + req.capability)

        // Embedding 特有：维度必须完全匹配（向量库依赖）
        if req.capability == "embedding" and req.dimension:
            if svc.dimension != req.dimension:
                reasons.append("embedding 维度不匹配（需要 " + req.dimension + "，服务提供 " + svc.dimension + "）")

    // 3. OCR 类
    if req.service_type == "ocr":
        // 格式：task 要求的格式必须全被支持（子集关系）
        for fmt in (req.image_formats or []):
            if fmt not in svc.image_formats:
                reasons.append("不支持图像格式: " + fmt)
        // 容量：task 上限 ≤ svc 上限
        if req.max_resolution and svc.max_resolution < req.max_resolution:
            reasons.append("服务分辨率上限不足（需要 ≥" + req.max_resolution + "）")
        if req.max_file_size_mb and svc.max_file_size_mb < req.max_file_size_mb:
            reasons.append("服务文件大小上限不足")

    // 4. ASR 类
    if req.service_type == "asr":
        for fmt in (req.audio_formats or []):
            if fmt not in svc.audio_formats:
                reasons.append("不支持音频格式: " + fmt)
        if req.max_duration_sec and svc.max_duration_sec < req.max_duration_sec:
            reasons.append("服务音频时长上限不足")
        for lang in (req.languages or []):
            if lang not in svc.languages:
                reasons.append("不支持语言: " + lang)

    return len(reasons) == 0, reasons
```

**Service capability_json schema（按 service_type）**：

```json
// service_type=llm
{
  "input_modalities": ["text", "image"],
  "output_modalities": ["text"],
  "context_window": 32768,
  "max_output_tokens": 8192,
  "capabilities": ["chat", "embedding", "rerank", "vision"],
  "dimension": 1024,                    // embedding 类服务需填
  "features": {
    "tool_use": true, "json_mode": true,
    "streaming": true, "vision": true,
    "thinking": false
  }
}

// service_type=ocr
{
  "image_formats": ["jpg", "png", "bmp"],
  "max_resolution": 4096,
  "max_file_size_mb": 10,
  "capabilities": ["ocr"]
}

// service_type=asr
{
  "audio_formats": ["wav", "mp3", "m4a"],
  "max_duration_sec": 3600,
  "languages": ["zh", "en"],
  "realtime": false,
  "capabilities": ["asr"]
}
```

同一份 schema 同时驱动：管理端表单渲染、管理端 Capability Matching 保存时校验、C 端 ModelSelector 预过滤。**代码实现位置**：`aiservice/profile/capability_schema.go` 单一数据源。

**Capability Matching 调用时机**：
- 管理端保存 Task Profile 时（PUT，不兼容返回 HTTP 422）
- 管理端 C 端 ModelSelector 列表的 `allowed_service_ids` 过滤（保存时已校验，此处仅做服务类型/下架过滤）

**不在运行时重复调用**（§6.5 决策）：信任保存时校验结果，避免每次 Gateway 调用的冗余开销。如果 DBA 手工改 DB 导致 binding 不一致，运行时可能出错，由运营承担风险。

### 5.3 ModelSelector 集成

仅两个 profile `user_selectable=1`：`chatbot.stream`、`salesrag.chat`。

用户端 `/v1/llm/models` 返回的模型列表为这两个 profile 的 `allowed_service_ids ∩ service_type='llm'` 的并集。现有 `user_model_preference.feature` 字段（chatbot/sop）映射为 taskID。

---

## §6 中间件详细行为

### 6.1 执行顺序（请求进入→返回）

```
Request → Tracing(start) → Billing(prepare) → Fallback(wrap) → Retry(wrap) → Adapter.Call → ↩
                                                                                             ← Response / Error
        ← Tracing(end)   ← Billing(write)   ← Fallback(done) ← Retry(done)  ←
```

**关键：Fallback 外层，Retry 内层。**
- Retry 只作用于**单个 service**的一次调用尝试（最多 1 次重试 = 每 service 2 次 upstream 调用）
- Fallback 切换到备用 service 后，**备用 service 不再 retry**（避免级联放大调用）
- **调用次数上限**：主 service 2 次（1 次初始 + 1 次 retry）+ 备用 service 1 次 = 最多 **3 次 upstream 调用**
- 此处修订 S1 proposal 中"最多 2 次"的表述为"最多 3 次"，并对应更新验收标准单测

### 6.2 Tracing 中间件

- 入口：若 ctx 已有 trace → 创建 span 嵌套；否则创建 trace + generation
- 类型映射：
  - LLM chat/vision/embed → `CreateGeneration`（含 model + input + output + token usage）
  - Rerank → `CreateSpan`（含 query + doc count）
  - OCR → `CreateSpan`（含 image size + output text length）
  - ASR → `CreateSpan`（含 audio duration + output text length）
- 关键 metadata：`task_id` / `service_id` / `service_name` / `user_id` / `feature_ref`
- Error：失败必调 `SetOutput({"error": err.Error()})` + `End`
- **容错硬规则**：所有 Langfuse SDK 调用 `context.WithTimeout(2s)` + 错误吞掉（log warn 不 propagate）。Langfuse 挂掉不阻塞主请求

### 6.3 Billing 中间件

- 入口：读 Task Profile + Service + Route，从 context 拿 user_id
- 准备 UsageRecord：`task_id`、`service_type`、`service_id`、`unit`、`pricing_*_snapshot`（按 unit 选相应字段）
- 出口：根据 adapter 返回的 usage（token / call_count / duration_seconds）填充对应字段
- 流式中断：在 ctx.Done() 触发时用字符估算 tokens（`chars / 2` 粗估）+ `is_estimated=1`
- 写入：同步写 DB（失败 log error 但不阻塞 response；确保业务不因记账失败而失败）

### 6.4 Retry 中间件

- 触发条件：adapter 返回 `ErrAIProviderTimeout` / `ErrAIProviderError`（5xx 或网络错误）
- 策略：单次重试，指数退避 200ms + 抖动；重试计数通过 ctx 传递防止嵌套
- 非重试错误：`ErrAICapabilityMismatch` / 4xx / context.Canceled
- **流式调用的 Retry 约束（硬规则）**：流式响应的 Retry **仅在首个 chunk 到达前失败**时触发。一旦 Gateway 已向业务层下发任何 chunk，Retry 不再执行（避免半条 A 响应 + 完整 B 响应的数据损坏）。实现细节：`ChatStream` 中 adapter 的返回 channel 吐出第一个值之前计失败可重试；之后失败必须向业务层传播错误
- **Fallback 旁路**：当 ctx 中存在 `skip_retry=true`（由 Fallback 中间件注入）时，Retry 中间件直接透传，不触发重试。此机制保证总调用次数可控（见 §6.5）

### 6.5 Fallback 中间件

- 触发条件：主 service Retry 后仍失败（已执行 2 次 upstream 调用），且 Task Profile 有非空 fallback 列表
- 策略：按 priority 取第一个 fallback service → 调 adapter（内层 Retry 中间件对 fallback service **不生效**，通过 ctx 标志关闭）→ 失败不级联下一个 fallback
- **硬规则**：最多 1 次 fallback 跳转。fallback service 上 Retry 中间件被旁路（无重试），即 fallback service 仅 1 次 upstream 调用
- **总调用上限**：主 2 次 + fallback 1 次 = 3 次 upstream 调用（全链路）
- fallback 失败直接返回 `ErrAIFallbackExhausted`
- Trace：在 fallback service 的 generation 的 metadata 里标记 `fallback_from_service_id`
- **运行时 Capability Matching 不重复校验**（信任保存时的静态校验；若 DBA 手工改数据导致不一致，由运营承担风险；见 §13 决策）

---

## §7 Langfuse Trace Topology

### 7.1 Trace 边界（一次业务请求 = 一个 trace）

| 业务场景 | Trace 创建点 | Trace 名称 |
|---|---|---|
| SOP 节点执行 | `biz/sop/executor.go` 入口 | `sop-execute-node` |
| ChatBot 流式 | `biz/chatbot/stream.go` 入口 | `chatbot-stream` |
| SalesRAG 问答 | `biz/salesrag/salesrag.go:1550` | `salesrag-chat`（保留现有） |
| SalesRAG 入库 | `biz/salesrag/service/pipeline.go` | `salesrag-ingest` |
| Monitor 简报 | `biz/monitor/briefing.go:27` | `monitor-briefing`（保留现有） |
| Monitor 分析 | `biz/monitor/analyzer.go` 入口 | `monitor-analyze`（独立入口场景） |

### 7.2 每次 Gateway 调用 = 一个 generation/span 嵌套到父 trace

所有 14 个 Task Profile 的调用都通过 Tracing 中间件生成 generation/span，嵌套到父 trace。

### 7.3 Metadata 约定

```json
{
  "task_id": "sop.text",
  "service_id": 42,
  "service_name": "deepseek-v3",
  "provider": "dmxapi",
  "user_id": 1001,
  "feature_ref": {"sop_id": 55, "node_id": 3},
  "fallback_from_service_id": null
}
```

---

## §8 迁移策略

### 8.1 17 个调用点迁移路径

| # | 调用点 | 现状 | 迁移到 | 预估工时 |
|---|---|---|---|---|
| 1 | SOP executor | llmrouter.StreamChat | `ai.ChatStream(SopText/SopVision)` | 0.3d |
| 2 | ChatBot stream | llmrouter.StreamChat | `ai.ChatStream(ChatbotStream)` | 0.2d |
| 3 | SalesRAG intent | dmxapi 直调 | `ai.Chat(SalesragIntent)` | 0.2d |
| 4 | SalesRAG chat | dmxapi 直调 | `ai.ChatStream(SalesragChat)` | 0.3d |
| 5 | SalesRAG rerank | dmxapi.Rerank | `ai.Rerank(SalesragRerank)` | 0.2d |
| 6 | SalesRAG embed | ali/volc 直调 | `ai.Embed(SalesragEmbed)` | 0.3d |
| 7 | SalesRAG tagging | dmxapi 直调 | `ai.Chat(SalesragTagging)` | 0.2d |
| 8 | SalesRAG profile | volc.StreamChatWithModel + ali.QianwenVisionStream | `ai.ChatStream(SalesragProfile)` | 0.5d |
| 9 | SalesRAG chatstyle | dmxapi + ali.QianwenVisionStream | `ai.ChatStream(SalesragChatstyle)` | 0.3d |
| 10 | Monitor briefing | dmxapi.ChatCompletion | `ai.Chat(MonitorBriefing)` | 0.2d |
| 11 | Monitor analyze | dmxapi.ChatCompletion | `ai.Chat(MonitorAnalyze)` | 0.2d |
| 12 | Monitor transcribe | http.Post 直调 FunASR | `ai.ASR(MonitorTranscribe)` | 0.5d |
| 13 | Baidu OCR | http.Post 直调 | `ai.OCR(OcrBaidu)` | 0.5d |

**总计迁移工时** ~3.9 天（在 S4 编码 5-7 天预算内）。

### 8.2 灰度策略

- 调用点密集模块（SalesRAG 7 条）：每条独立 commit，迁完一条跑回归（lint + 手触发一次业务 + Langfuse 确认 trace）
- 调用点 ≤2 模块（OCR / ASR / Monitor）：一次性切换，保留回滚 commit
- **`llmrouter` 包终态（明确）**：本期保留、不删除、不改名。所有原公有方法签名保持；内部调用改为转发到 `aiservice` 层；SOP/ChatBot 的现有代码路径仍通过 `llmrouter.StreamChat` 访问（兼容零 breaking）。S7 前不做拆除决策，**作为独立 feature 在 Phase 2 评估**（拆除或保留为 LLM-only shim）

### 8.3 老 billing 关闭

迁移前每个调用点的老封装层 billing 写入关闭（通过 ctx 传递 `skip_legacy_billing=true` 标志），防止双记账。一次迁移一个确保不漏。

**工时补充**：为老封装层（ali/volc/baidu/dmxapi/bailian）的相关函数加 ctx flag 读取逻辑，估计额外 **0.5-1 天**（未计入 §8.1 的 3.9 天）。S3 task plan 必须单列一条 task："为老封装层加 billing skip flag"，作为 §8.1 迁移的前置步骤。

---

## §9 错误处理

### 9.1 错误分类

| 错误码 | 严重性 | 用户可见 | 处理 |
|---|---|---|---|
| `ErrAICapabilityMismatch` | 配置错误 | 管理员可见（403） | 保存时阻止 |
| `ErrAIProfileNotFound` | 编程错误 | 内部 | log error，业务 500 |
| `ErrAIServiceDeprecated` | 运营错误 | 管理员可见 | 调用时走 fallback |
| `ErrAIProviderTimeout` / `Error` | 外部错误 | 对用户脱敏 | Retry 中间件处理 |
| `ErrAIFallbackExhausted` | 严重 | 用户提示"AI 服务暂时不可用" | 告警 |

### 9.2 日志规范

- error 以上级别：用 zap；必含 `task_id` / `service_id` / `user_id`
- fallback 触发：warn 级别
- Langfuse 失败：warn（不影响主流程）

---

## §10 管理端 UI（numind-admin-web）

### 10.1 路由

```
/ai-service
  ├── /services              # 服务列表
  ├── /services/:id/edit     # 服务编辑
  ├── /tasks                 # Task Profile 列表
  ├── /tasks/:id/edit        # Task Profile 编辑
  └── /audit-logs            # 审计日志
```

### 10.2 关键组件

- `<ServiceTable>`：DataTable 封装（遵守 UI 硬规则 1）；列出 ai_service；筛选 service_type/status
- `<ServiceForm>`：按 service_type 动态渲染能力字段；blur 验证（硬规则 3）
- `<TaskBindingEditor>`：选择 default/fallback/allowed services，实时调后端 `/validate-against/` 显示兼容性灯；不兼容下拉项 disabled + tooltip
- `<ConfirmModal>`：删除/下架前二次确认（硬规则 4）
- 4 状态处理：所有异步视图都有 loading skeleton / empty + CTA / error + retry / success（硬规则 2）

### 10.3 capability schema 驱动表单

前端启动时一次性拉 `/v1/admin/ai/capability-schema`，缓存到 Pinia store。`<ServiceForm>` 根据 service_type 读对应 schema 渲染字段（避免硬编码字段）。

---

## §11 配置变更（config_*.yaml）

### 11.1 移出 config 的项

```yaml
# 删除以下字段（已迁到 Registry）
# ali:
#   text:
#     model: "qwen-plus"         # ← 删除
#   vision:
#     model: "qwen3-vl-flash-..."  # ← 删除
# volc:
#   model: "glm-4-7-251222"        # ← 删除
```

### 11.2 保留在 config 的项

```yaml
ai_providers:                   # 新增字段，存凭据
  ali:
    api_key: "${ALI_API_KEY}"
    base_url: "https://dashscope.aliyuncs.com/compatible-mode/v1"
  volc:
    api_key: "${VOLC_API_KEY}"
    base_url: "https://ark.cn-beijing.volces.com/api/v3"
  dmxapi:
    api_key: "${DMXAPI_API_KEY}"
    base_url: "https://www.dmxapi.cn/v1"
  baidu:
    api_key: "${BAIDU_OCR_API_KEY}"
    secret_key: "${BAIDU_OCR_SECRET}"
  bailian:
    api_key: "${BAILIAN_API_KEY}"
    workspace_id: "${BAILIAN_WORKSPACE_ID}"
  funasr:
    base_url: "${FUNASR_URL}"    # 本地服务无 API key

langfuse:
  enabled: true
  base_url: "..."                # 遗留内网 IP 问题记入 S7 tech debt
  public_key: "..."
  secret_key: "..."
```

**向后兼容**：Seed migration 从 config 读取 api_key 写入 `llm_provider.api_key`，之后 Gateway 从 DB 读（单一数据源）；config 的 api_key 作为 bootstrap 使用一次后实际不再读（但保留占位便于运维看到）。

---

## §12 验证策略（S5 入口 gate）

根据 `.claude/rules/ndf-enforcement.md §规则 10`，S3 plan 必须有一个"S5 验证策略"task。此处先声明：

- **验证方式**：主力 biz 层 unit test + dev 环境手测（不用 Playwright，因本功能无 C 端 UI 改动）
- **关键用户路径**：
  1. SOP 执行节点（dev 环境触发 1 次，Langfuse 确认 trace）
  2. ChatBot 问答（dev 环境触发 1 次）
  3. SalesRAG 问答 + 入库（dev 环境触发 1 次）
  4. 卡片生成（非 AI，但回归测）
  5. Monitor 简报（手动触发 1 次）
  6. OCR 一次（用测试图片）
  7. ASR 一次（用测试视频）
  8. 管理端 CRUD：新增/编辑/下架一个服务；新建/改绑一个 Task Profile；触发一次 Capability Matching 拒绝
  9. Fallback 场景：手动改 provider base_url 为无效值，确认走 fallback
- **回归保护**：Gateway 中间件 unit test 是持久化保障；业务流程 unit test 若覆盖则更好
- **额外 unit test 要求**：
  - Fallback 最多 1 跳上限（强制主失败 → 断言总 upstream ≤ 3：主 2 次 + 备 1 次）
  - 流式 Retry 首 chunk 约束（模拟 adapter 吐出 1 个 chunk 后失败 → 断言不触发 retry）
  - `go test -race ./internal/pkg/aiservice/...` 通过（Gateway cache + 中间件并发安全）

---

## §13 Decisions 列表

### 13.1 架构与入口
| 决策 | 选择 | 理由 |
|---|---|---|
| Gateway 目录位置 | `internal/pkg/aiservice/` | 与 `internal/pkg/llm/` 并存 |
| 入口形态 | 多 method（`ai.Chat/Embed/Rerank/OCR/ASR`） | Go 类型安全 |
| Chat 与 Vision 合并入口 | `ai.Chat`（Messages multipart） | OpenAI 风格 |
| 流式与非流式 | 拆 `Chat` / `ChatStream` 两方法 | 返回值类型不同 |
| Adapter interface 拆分 | 按能力小 interface + type assertion（ISP） | adapter 不需为无关能力写 stub |
| `llmrouter` 包命运 | 本期保留、方法签名不变；Phase 2 评估 | 避免 breaking |

### 13.2 数据与存储
| 决策 | 选择 | 理由 |
|---|---|---|
| Task Profile 数量 | 14 | §5.1 详表 |
| service_type 分类 | llm / ocr / asr | 覆盖现有全部 AI 调用 |
| capability 存储 | JSON 列 | 维度差异大，规模 < 100 条不需索引 |
| `salesrag.profile` vs `chatstyle` 是否合并 | 不合并 | 尽管 requirements 相同，两者业务语义不同；管理员可能希望独立绑不同模型做 A/B |
| UsageRecord 策略 | 扩 4 个 nullable pricing snapshot 列（非 JSON） | GORM 类型安全 + 查询友好 |
| UsageRecord 历史数据 | 不 backfill | §2.3 |
| VIEW 使用 | 仅读兼容 + GORM `TableName()` 指向新表 | 避开 MySQL updatable view 陷阱 |
| FK `ON DELETE` 语义 | 软删为主，`ON DELETE SET NULL` 仅防 DBA 手工硬删意外 | 业务上用 `deprecated_at`，biz 层在软删时显式清 default_service_id |
| `task_profile_service.role` 设计 | UNIQUE (profile, service, role)；允许同 service 既是 fallback 又是 allowed | fallback 本应是 allowed 的子集，保存时 biz 层强制 `fallback ⊆ allowed` |

### 13.3 中间件行为
| 决策 | 选择 | 理由 |
|---|---|---|
| 中间件顺序 | `Tracing → Billing → Fallback → Retry → Adapter` | Fallback 外，Retry 内；fallback 服务不再 retry |
| 最大 upstream 调用数 | 3（主 2 + 备 1） | 平衡容灾与成本 |
| 流式 Retry | 仅在首 chunk 到达前触发 | 防止数据损坏 |
| Billing 写入策略 | 同步写 DB；失败 log 不 block | 权衡一致性与可用性；异步队列作为 Phase 2 |
| 流式中断 token 估算 | `chars / 2` 粗估 + is_estimated=1 | 接受 ±30% 误差作为本期 trade-off；Phase 2 可接入精确 tokenizer |
| 运行时 Capability Matching | 不重复校验（信任保存时校验） | 避免冗余性能开销；DBA 手工改数据导致不一致由运营承担 |
| Registry cache | 写操作（create/update/deprecate）触发 local invalidate + TTL 30s 兜底 | 失效速度优先；30s 是兜底上限 |
| pricing 读取时机 | Gateway 入口读，快照写入 UsageRecord | in-flight 用旧价 |

### 13.4 运营与权限
| 决策 | 选择 | 理由 |
|---|---|---|
| Rate limit | 本期不做专用中间件 | S5 暴露再加 Phase 2 |
| 多租户 | 单租户形态，Registry 全站共享 | 项目现状 |
| 超管角色 | **本期不引入**，所有 admin 可执行 override/restore，强制填 reason + audit | admin_user 表无 role 字段；引入分级作为独立 feature |
| audit log | task_profile / service / pricing / override 全记 | 减少运营风险 |
| 部署策略 | 需短暂停服维护窗口（2-3 分钟） | DDL 非事务性，避免在线时间窗损坏 |

### 13.5 Prompt 管理（遗留决策）
| 决策 | 选择 | 理由 |
|---|---|---|
| Prompt 是否纳入 Task Profile | **否** | 各 biz 模块继续自管 prompt（优先 langfuse.FetchPrompt，fallback 硬编码）；Task Profile 只管"选哪个服务"，不管"怎么提问" |

---

## §14 PRD 覆盖证明

### 14.1 用户故事 → spec 映射

| 用户故事 | spec 对应章节 |
|---|---|
| 管理员新增/下架服务 | §4.2 `POST/DELETE /v1/admin/ai/services` + §10.2 ServiceForm |
| 管理员编辑能力字段 | §4.2 `PUT` + §2.1.2 capability_json + §10.3 schema 驱动 |
| 管理员绑定 task-service（default/fallback/allowed） | §4.2 Task Profile 管理 + §2.2.1-2.2.2 + §10.2 TaskBindingEditor |
| 不兼容绑定被拒 + 原因 | §5.2 Capability Matching 算法 + §4.3 `ErrAICapabilityMismatch` + §10.2 TaskBindingEditor 兼容性灯 |
| 服务使用量/费用/错误率可见 | §7 Langfuse trace + §4.2 `GET /healthz/ai` |
| pricing 修改不影响历史账单 | §2.3 pricing_*_snapshot + §6.3 Billing 中间件语义 |
| 开发者只通过 Gateway 调用 AI | §3 目录 + §3.2 入口 |
| 开发者一个清晰入口 | §3.3 constants.go |
| fallback 对业务透明 | §6.5 Fallback 中间件 |
| 运营按任务维度看成本 | §7.3 task_id metadata + §2.3 usage_record.task_id |

### 14.2 验收标准 → spec 映射

| 验收标准 | spec 对应 | 如何验证 |
|---|---|---|
| 17 个调用点走 Gateway | §8.1 迁移表 | S5 grep 检查 |
| 无 AI 裸 http | §9.2 日志 + S5 grep | grep |
| 业务代码不 import provider 包 | §3.1 目录隔离 | grep import |
| Langfuse 覆盖 | §6.2 + §7 | S5 Langfuse 控制台 count |
| error 有 generation/span error | §6.2 | 人工测 |
| Token/call/second 正确进 UsageRecord | §6.3 | S5 测各 1 条 |
| 4 个管理页可用 | §10.1 + §10.2 | 手测 |
| 新增服务不重启 | §3.2 Registry cache TTL=30s | 手测 |
| 能力不匹配保存被拒 | §5.2 + §4.2 | 手测 + unit test |
| pricing 改不影响历史 | §6.3 snapshot | 手测 |
| 关键操作二次确认 | §10.2 ConfirmModal | 手测 |
| 主服务挂自动 fallback | §6.5 | 人工模拟 |
| Langfuse 挂不中断 | §6.2 硬规则 | unit test |
| Fallback 最多 1 跳 | §6.5 硬规则 | unit test |
| 现有 15 API shape 不变 | §4.1 + §4.2 保留段 | 契约测试 |
| ModelSelector 无需改动 | §1.3 + §5.3 | 手测 |
| SOP/ChatBot/SalesRAG 回归通过 | §12 验证策略 | 手测 |
| 中间件每类 1 unit test | §6 各子节 | 代码 assert |
| Billing 覆盖 3 种 service_type | §6.3 | 代码 assert |
| Provider adapter 每个 1 roundtrip | §3.4 | 代码 assert |
| Capability matching 兼容/不兼容各 1 条单测 | §5.2 | 代码 assert |

### 14.3 边界情况 → spec 映射

| 边界情况 | spec 对应 |
|---|---|
| default_service 被下架 | §6.5 fallback 自动切；§5.2 Capability Matching |
| 并发主挂 | §6.5 + §6.4 retry 隔离 |
| 客户端中断流式 | §6.3 is_estimated=1 |
| 管理员删正在用的服务 | §2.1.2 deprecated_at 软删 + §6.5 fallback |
| 能力字段不全 | §5.2 保守默认 false |
| pricing=0 | §6.3 允许，写 0 cost |
| ASR 超长 | §5.1 requirements.max_duration_sec + §5.2 校验 |

### 14.4 权限规则 → spec 映射

| 权限 | spec 对应 |
|---|---|
| C 端用户不可访问 admin | §4.2 路径前缀 `/v1/admin/ai/*` 走 admin_token 中间件 |
| 管理员操作 `/v1/admin/ai/*` | §4.2 |
| 强制 override 不兼容绑定 | §4.2 `PUT ...?force=true` + body.reason + audit log（所有 admin 可执行，本期不设超管分级；admin_user 表现有 schema 无 role 字段，后期若需细粒度权限作为独立 feature 开） |
| 软删恢复 | §4.2 `/restore` + reason + audit（同上权限模型） |

---

## §15 S2 Review 修订记录

| 修订 | 严重性 | 处理 |
|---|---|---|
| 中间件顺序矛盾导致最多 4 次调用 | P0 | ✅ §6.1 改为 Fallback 外 + Retry 内；§6.5 明确总 3 次上限 |
| OCR/ASR 不等式方向语义不清 | P0 | ✅ §5.2 加"字段语义约定" + 每行加说明注释 |
| Embedding dimension 校验缺失 | P0 | ✅ §5.2 添加 dimension 检查 + §5.1 embed 行加 fallback 警告 |
| Migration RENAME + VIEW 时间窗 | P0 | ✅ §2.5 改为需维护窗口 + RENAME TABLE 原子化 |
| Seed 读 config 机制未定义 | P0 | ✅ §2.6 新增"Go 启动时 seed 同步"方案 |
| 超管角色在 admin_user 表中不存在 | P0 | ✅ §4.2 去掉超管概念，改为"所有 admin + reason + audit" |
| Adapter 单一大 interface 违反 ISP | P1 | ✅ §3.4 拆为 ChatAdapter/EmbedAdapter 等按能力小 interface |
| 流式 Retry 可能造成数据损坏 | P1 | ✅ §6.4 加"仅首 chunk 到达前"硬规则 |
| API 缺 response shape | P1 | ✅ §4.4 补 3 个关键端点示例 |
| UsageRecord 宽表 vs JSON | P1 | ✅ §13.2 记为显式决策（GORM 类型安全优先） |
| Billing 同步写 DB 瓶颈 | P1 | ✅ §13.3 记为显式决策（异步作为 Phase 2） |
| Registry cache TTL=30s 无依据 | P1 | ✅ §13.3 改为"写触发 invalidate + 30s 兜底" |
| salesrag.profile vs chatstyle 合并 | P1 | ✅ §13.2 记为显式决策（独立便于 A/B） |
| FK ON DELETE SET NULL 冗余 | P1 | ✅ §13.2 记为显式决策（防 DBA 手工硬删） |
| task_profile_service UNIQUE 歧义 | P1 | ✅ §13.2 记为显式决策（biz 层强制 fallback ⊆ allowed） |
| llmrouter 终态不明 | P1 | ✅ §8.2 明确 Phase 2 再评估 |
| 老 billing ctx flag 工时遗漏 | P1 | ✅ §8.3 明确额外 0.5-1 天 |
| 运行时重复 Capability 校验冗余 | P1 | ✅ §6.5 改为"不重复校验" |
| `chars / 2` token 估算精度 | P2 | ✅ §13.3 接受 ±30% 作为本期 trade-off |
| Langfuse prompt 管理空白 | P2 | ✅ §13.5 明确"不纳入 Task Profile" |
| Race test 未规划 | P2 | ✅ §12 加 `go test -race` 要求 |
| idx/errno/MySQL 版本等细节 | P2 | ⏩ S3 task 备注中补 |
| OCR/ASR capability_json 缺 capabilities 字段（§2.4 与 migration 不一致） | P1 | ✅ 2026-04-15 Task 1a fix：§2.4 seed SQL 补加 `"capabilities":["ocr"]` / `"capabilities":["asr"]`，与 migration 保持一致 |
| Rollback DROP INDEX/COLUMN 不幂等，service_type 误删 dev 已有数据 | P1 | ✅ 2026-04-15 Task 1a fix：rollback.sql 改为 PROCEDURE 条件 DDL；service_type 列 COMMENT 含 tag `ai-service-manager:v1`，rollback 精确检查 tag 后才删 |
| 索引命名缺表别名前缀（违反 database.md 约定） | P2 | ✅ 2026-04-15 Task 1a fix：migration/rollback/spec 中 idx_service_type→idx_as_service_type、idx_deprecated→idx_as_deprecated、idx_profile_role→idx_tps_profile_role、idx_actor_created→idx_asal_actor_created、idx_target→idx_asal_target |
| Runbook `<PROD_DB_PASS>` 硬编码占位符 | P2 | ✅ 2026-04-15 Task 1a fix：改为 `"$PROD_DB_PASS"` 环境变量引用 |

---

## Appendix A：Rollback 脚本（migration 失败时）

```sql
-- 按倒序执行
DELETE FROM task_profile_service;
DELETE FROM task_profile;
DELETE FROM ai_service WHERE service_type IN ('ocr','asr');

DROP TABLE IF EXISTS ai_service_audit_log;
DROP TABLE IF EXISTS task_profile_service;
DROP TABLE IF EXISTS task_profile;

-- 恢复 usage_record
ALTER TABLE usage_record
  DROP COLUMN is_estimated, DROP COLUMN pricing_second_snapshot,
  DROP COLUMN pricing_call_snapshot, DROP COLUMN pricing_output_snapshot,
  DROP COLUMN pricing_input_snapshot, DROP COLUMN duration_seconds,
  DROP COLUMN call_count, DROP COLUMN unit, DROP COLUMN task_id,
  DROP COLUMN service_type;

-- 恢复 ai_service_route → llm_model_provider
DROP VIEW IF EXISTS llm_model_provider;
ALTER TABLE ai_service_route
  DROP COLUMN price_per_second, DROP COLUMN price_per_call, DROP COLUMN pricing_unit;
ALTER TABLE ai_service_route RENAME TO llm_model_provider;

-- 恢复 ai_service → llm_model
DROP VIEW IF EXISTS llm_model;
ALTER TABLE ai_service
  DROP INDEX idx_as_deprecated, DROP INDEX idx_as_service_type,
  DROP COLUMN deprecated_at, DROP COLUMN tags, DROP COLUMN quality_tier,
  DROP COLUMN latency_tier, DROP COLUMN capability_json, DROP COLUMN service_type;
ALTER TABLE ai_service RENAME TO llm_model;

-- 恢复 llm_provider
DELETE FROM llm_provider WHERE provider_type IN ('ocr','asr','file_service');
ALTER TABLE llm_provider
  DROP INDEX idx_provider_type,
  DROP COLUMN supports_streaming, DROP COLUMN provider_type;
```
