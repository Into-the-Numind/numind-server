# Agent 模式 Tool Registry — 技术设计

**Spec date**: 2026-05-21
**Feature ID**: agent-mode-tool-registry（#3/14）
**Track**: Standard
**Status**: DRAFT（S2，待 reviewer pass）
**架构蓝本**: `docs/agent-mode/architecture-v1.md` §4.2.2（ToolFactory）/ §4.2.3（Tool interface 36 方法）/ §4.2.4（v1 工具池）/ §8 数据模型 #10（tool_definition 表）

## §1 设计概览

### 1.1 目标

把 #2 minimal `Tool` interface（3 方法）扩展到完整 `FullTool` interface（蓝本 §4.2.3 共 36 方法），引入 `ToolFactory` 插件模式 + `AgentToolRegistry`（启动注册 / 运行时查找）+ 6 个 platform 工具实现。

### 1.2 与蓝本的命名对齐（修正 S1 草稿）

| S1 草稿名 | 蓝本 canonical | 决策 |
|-----------|----------------|------|
| `agent_tool` 表 | **`tool_definition`** (蓝本 §8.10) | 用蓝本名 `tool_definition` |
| `agent_tool_factory` 表 | 蓝本无此表 | 本 feature 新增（仅 read-only DDL 预埋；#10 加 CRUD），但**改名为 `tool_factory_registry`** 避免暗示与 tool_definition 的双重关系 |
| `agent.Tool` (#2 旧名) | **保留** | 重命名为 `agent.MinimalTool`（#2 单测内嵌用） |
| 新 36 方法 interface | **`agent.FullTool`** | 工厂 / Registry 都用此 interface |

### 1.3 关键不变量

1. **#2 现有代码零破坏**：`MinimalTool` 沿用 #2 接口；`runner_test.go` 现有 mock 用 BaseTool embedding 补默认值
2. **ToolFactory.Watch 在 v1 是 noop**：保留接口为 #10 configurator-ux 动态 CRUD 用
3. **bash_exec / image_gen 在 #3 是 stub**：Execute 返回特定 error，`IsEnabled(ToolConfig)` 返回 cfg.EnableSandbox 默认 false
4. **`AgentConfig` 蓝本类型未引入 #3 范围**：用轻量 `agent.ToolConfig struct{ EnableSandbox, EnableImageGen bool }` 替代
5. **AgentRunner.Run 签名变更**：`RunRequest.Tools []tool.BaseTool` → `RunRequest.ToolNames []string`；runner 内部通过 Registry.GetTool 装配
6. **tool_definition 表 seed**：PlatformToolFactory 启动时通过 INSERT IGNORE 把 6 工具元信息写入（运营动态启停在 #10）
7. **aiservice 唯一入口**：document_generate 走 aiservice.Chat；image_gen / bash_exec stub 阶段不绕过 aiservice
8. **prod 零影响**：止步 develop merge + dev container 部署

---

## §2 M1 数据模型

### 2.1 `tool_definition` 表 DDL（蓝本 §8.10）

```sql
CREATE TABLE IF NOT EXISTS tool_definition (
  id                         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tool_name                  VARCHAR(128)   NOT NULL                 COMMENT '工具标识符（全局唯一）',
  display_name               VARCHAR(128)   NOT NULL                 COMMENT 'Narration 层显示名称',
  description                TEXT           NOT NULL                 COMMENT '工具功能描述（LLM 选工具用）',
  tool_source                VARCHAR(16)    NOT NULL                 COMMENT '工具来源：platform/mcp/cli/webhook',
  risk_level                 VARCHAR(16)    NOT NULL DEFAULT 'safe'  COMMENT 'safe/moderate/dangerous',
  requires_sandbox           TINYINT(1)     NOT NULL DEFAULT 0,
  requires_tenant_whitelist  TINYINT(1)     NOT NULL DEFAULT 0,
  input_schema               JSON                                    COMMENT 'JSON Schema',
  output_schema              JSON,
  is_enabled                 TINYINT(1)     NOT NULL DEFAULT 1       COMMENT '运营开关',
  is_beta                    TINYINT(1)     NOT NULL DEFAULT 0,
  category                   VARCHAR(64)                             COMMENT '查询/生成/代码/多媒体/RAG',
  config_json                JSON                                    COMMENT '运行时附加配置（限流/quota 等，#10 用）',
  created_at                 DATETIME(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at                 DATETIME(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_tool_name (tool_name),
  KEY idx_source_enabled (tool_source, is_enabled),
  CONSTRAINT chk_td_source CHECK (tool_source IN ('platform','mcp','cli','webhook')),
  CONSTRAINT chk_td_risk CHECK (risk_level IN ('safe','moderate','dangerous'))
);
```

**与蓝本差异**：
- `tool_source` 用 VARCHAR + CHECK（兼容 MySQL 5.7+），而非蓝本 ENUM
- 加 `webhook` 入 CHECK 但 v1 不发出（蓝本 §4.2.2 标"未来"）
- 加 `config_json` 字段为 #10 预埋（蓝本未显式列）

### 2.2 `tool_factory_registry` 表 DDL（本 feature 新增，read-only #3）

```sql
CREATE TABLE IF NOT EXISTS tool_factory_registry (
  id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  factory_id        VARCHAR(64)    NOT NULL COMMENT '工厂唯一标识（如 platform-builtin / mcp-tencent-cloud）',
  source_type       VARCHAR(16)    NOT NULL COMMENT 'platform/mcp/cli/webhook',
  display_name      VARCHAR(128)   NOT NULL,
  config_json       JSON                    COMMENT '工厂启动配置（如 MCP server URL）',
  is_enabled        TINYINT(1)     NOT NULL DEFAULT 1,
  loaded_tools_count INT           NOT NULL DEFAULT 0 COMMENT '最近一次 LoadTools 注册的工具数',
  last_loaded_at    DATETIME(3),
  created_at        DATETIME(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at        DATETIME(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_factory_id (factory_id),
  CONSTRAINT chk_tfr_source CHECK (source_type IN ('platform','mcp','cli','webhook'))
);
```

**#3 范围**：仅建表 + read-only store 接口；启动时由 Registry 自动 seed 一行 `(factory_id='platform-builtin', source_type='platform', ...)`；写入路径 #10 才加。

### 2.3 Migration 文件

- Forward：`migrations/20260521_120000_create_tool_definition_and_factory_registry.sql`
- Rollback：`migrations/20260521_120000_create_tool_definition_and_factory_registry_rollback.sql`（`DROP TABLE IF EXISTS tool_factory_registry; DROP TABLE IF EXISTS tool_definition;`）

### 2.4 GORM Model

```go
// internal/pkg/model/tool_definition.go
package model

import (
    "time"
    "gorm.io/datatypes"
)

type ToolDefinition struct {
    ID                       uint64         `gorm:"primaryKey;autoIncrement"`
    ToolName                 string         `gorm:"size:128;not null;uniqueIndex"`
    DisplayName              string         `gorm:"size:128;not null"`
    Description              string         `gorm:"type:text;not null"`
    ToolSource               string         `gorm:"size:16;not null;index:idx_source_enabled,priority:1"`
    RiskLevel                string         `gorm:"size:16;not null;default:'safe'"`
    RequiresSandbox          bool           `gorm:"not null;default:false"`
    RequiresTenantWhitelist  bool           `gorm:"not null;default:false"`
    InputSchema              datatypes.JSON `gorm:"type:json"`
    OutputSchema             datatypes.JSON `gorm:"type:json"`
    IsEnabled                bool           `gorm:"not null;default:true;index:idx_source_enabled,priority:2"`
    IsBeta                   bool           `gorm:"not null;default:false"`
    Category                 string         `gorm:"size:64"`
    ConfigJSON               datatypes.JSON `gorm:"type:json"`
    CreatedAt                time.Time      `gorm:"type:datetime(3);autoCreateTime"`
    UpdatedAt                time.Time      `gorm:"type:datetime(3);autoUpdateTime"`
}

func (ToolDefinition) TableName() string { return "tool_definition" }

// internal/pkg/model/tool_factory_registry.go
type ToolFactoryRegistryRow struct {
    ID                uint64         `gorm:"primaryKey;autoIncrement"`
    FactoryID         string         `gorm:"size:64;not null;uniqueIndex"`
    SourceType        string         `gorm:"size:16;not null"`
    DisplayName       string         `gorm:"size:128;not null"`
    ConfigJSON        datatypes.JSON `gorm:"type:json"`
    IsEnabled         bool           `gorm:"not null;default:true"`
    LoadedToolsCount  int            `gorm:"not null;default:0"`
    LastLoadedAt      *time.Time     `gorm:"type:datetime(3)"`
    CreatedAt         time.Time      `gorm:"type:datetime(3);autoCreateTime"`
    UpdatedAt         time.Time      `gorm:"type:datetime(3);autoUpdateTime"`
}

func (ToolFactoryRegistryRow) TableName() string { return "tool_factory_registry" }
```

---

## §3 M2 Store 设计

### 3.1 Interfaces

```go
// internal/numind/store/tool_definition.go
package store

import (
    "context"
    "time"
    "numind-server/internal/pkg/model"
)

type IToolDefinitionStore interface {
    Upsert(ctx context.Context, def *model.ToolDefinition) error // INSERT IGNORE + Update if exists
    Get(ctx context.Context, toolName string) (*model.ToolDefinition, error)
    ListEnabled(ctx context.Context) ([]model.ToolDefinition, error)
    ListBySource(ctx context.Context, source string) ([]model.ToolDefinition, error)
    SetEnabled(ctx context.Context, toolName string, enabled bool) error // 运营开关（#10 用）
}

// internal/numind/store/tool_factory_registry.go
type IToolFactoryRegistryStore interface {
    Upsert(ctx context.Context, row *model.ToolFactoryRegistryRow) error
    List(ctx context.Context) ([]model.ToolFactoryRegistryRow, error)
    UpdateLoadStats(ctx context.Context, factoryID string, count int, loadedAt time.Time) error
}
```

`store.IStore` 接口加两个方法 `ToolDefinitions() IToolDefinitionStore` + `ToolFactoryRegistries() IToolFactoryRegistryStore`。

### 3.2 Upsert 关键实现（INSERT IGNORE 模式）

```go
func (s *toolDefinitionStore) Upsert(ctx context.Context, def *model.ToolDefinition) error {
    // GORM OnConflict + DoUpdates 等价于 INSERT ... ON DUPLICATE KEY UPDATE
    return s.db.WithContext(ctx).
        Clauses(clause.OnConflict{
            Columns: []clause.Column{{Name: "tool_name"}},
            DoUpdates: clause.AssignmentColumns([]string{
                "display_name", "description", "input_schema", "output_schema",
                "risk_level", "requires_sandbox", "category", "config_json", "updated_at",
            }),
        }).
        Create(def).Error
}
```

**注意不更新 `is_enabled` / `is_beta`**：运营在 #10 设的值不能被代码 seed 覆盖。

---

## §4 M3 FullTool Interface（蓝本 §4.2.3 verbatim 36 方法）

### 4.1 Interface 定义

完整 36 方法 interface 在 `internal/numind/biz/agent/tool_full.go`：

```go
package agent

import (
    "context"
    "encoding/json"
)

// FullTool 是 #3 引入的完整 Tool 接口，蓝本 §4.2.3。
// 共 36 方法，分 8 组：基础元数据 / 行为标志 / 来源标记 / 加载策略 /
// 输出控制 / 输入处理 / 权限控制 / 结果序列化 / Narration 层。
type FullTool interface {
    // ── 基础元数据（6） ──
    Name() string
    Aliases() []string
    SearchHint() []string
    Description() string
    Prompt() string
    InputSchema() json.RawMessage

    // ── 行为标志（6） ──
    IsEnabled(cfg ToolConfig) bool
    IsConcurrencySafe(input ToolInput) bool
    IsReadOnly() bool
    IsDestructive() bool
    InterruptBehavior() string // "cancel" | "wait" | "noop"
    IsSearchOrReadCommand() bool

    // ── 来源标记（4） ──
    IsMCP() bool
    IsCLI() bool
    MCPInfo() *MCPToolInfo
    CLIInfo() *CLIToolInfo

    // ── 加载策略（2） ──
    ShouldDefer() bool
    AlwaysLoad() bool

    // ── 输出控制（1） ──
    MaxResultSizeChars() int

    // ── 输入处理（3） ──
    BackfillObservableInput(input ToolInput) ToolInput
    ValidateInput(ctx context.Context, input ToolInput) error
    InputsEquivalent(a, b ToolInput) bool

    // ── 权限控制（3） ──
    CheckPermissions(ctx context.Context, input ToolInput) error
    GetPath(input ToolInput) string
    PreparePermissionMatcher(input ToolInput) PermissionMatcher

    // ── 执行 + 结果序列化（3） ──
    Execute(ctx context.Context, input ToolInput) (ToolResult, error)
    MapToolResultToBlock(result ToolResult) []ContentBlock
    ToAutoClassifierInput(input ToolInput) map[string]interface{}

    // ── Narration 层（8） ──
    UserFacingName() string
    GetActivityDescription(input ToolInput) string
    RenderToolUseMessage(input ToolInput) NarrationMessage
    RenderToolResultMessage(input ToolInput, result ToolResult) NarrationMessage
    RenderToolErrorMessage(input ToolInput, err error) NarrationMessage
    ShouldShowResultInNarration() bool
    NarrationVerb() string
    NarrationDetail(result ToolResult) string
}
```

### 4.2 配套类型（同文件）

```go
// ToolConfig：FullTool.IsEnabled 的轻量参数，替代蓝本 AgentConfig
// （AgentConfig 是 #10 引入的 agent_definition model，#3 范围未到）
type ToolConfig struct {
    EnableSandbox  bool
    EnableImageGen bool
    // 后续 feature 按需扩展
}

// ToolInput / ToolResult：通用 JSON 结构
type ToolInput  json.RawMessage
type ToolResult json.RawMessage

// PermissionMatcher：权限缓存匹配器，#6 permission-pipeline 用
type PermissionMatcher interface {
    Matches(other PermissionMatcher) bool
    Hash() string
}

// MCPToolInfo / CLIToolInfo：占位（MCP/CLI 在后续 feature）
type MCPToolInfo struct {
    ServerName string
    ToolName   string
}
type CLIToolInfo struct {
    Command string
    Args    []string
}

// ContentBlock / NarrationMessage：占位，#8 narration-layer 真实实装
type ContentBlock struct {
    Type    string // "text" | "image" | "document"
    Content string
}
type NarrationMessage struct {
    Verb   string
    Detail string
    Icon   string
}
```

### 4.3 BaseTool 嵌入结构（提供默认值，让 mock / impl 只覆盖必需字段）

```go
// BaseTool 提供 FullTool 36 方法中约 28 个非必需方法的默认实现。
// 工具 impl 嵌入 BaseTool 后只需重写关键的 ~8 方法（Name/Description/Execute 等）。
type BaseTool struct{}

// 默认值（impl 可重写）
func (BaseTool) Aliases() []string                                                       { return nil }
func (BaseTool) SearchHint() []string                                                    { return nil }
func (BaseTool) Prompt() string                                                          { return "" }
func (BaseTool) InputSchema() json.RawMessage                                             { return nil }
func (BaseTool) IsEnabled(_ ToolConfig) bool                                              { return true }
func (BaseTool) IsConcurrencySafe(_ ToolInput) bool                                       { return true }
func (BaseTool) IsReadOnly() bool                                                         { return true }  // 默认安全
func (BaseTool) IsDestructive() bool                                                      { return false }
func (BaseTool) InterruptBehavior() string                                                { return "cancel" }
func (BaseTool) IsSearchOrReadCommand() bool                                              { return false }
func (BaseTool) IsMCP() bool                                                              { return false }
func (BaseTool) IsCLI() bool                                                              { return false }
func (BaseTool) MCPInfo() *MCPToolInfo                                                    { return nil }
func (BaseTool) CLIInfo() *CLIToolInfo                                                    { return nil }
func (BaseTool) ShouldDefer() bool                                                        { return false }
func (BaseTool) AlwaysLoad() bool                                                         { return false }
func (BaseTool) MaxResultSizeChars() int                                                  { return 0 }
func (BaseTool) BackfillObservableInput(input ToolInput) ToolInput                        { return input }
func (BaseTool) ValidateInput(_ context.Context, _ ToolInput) error                       { return nil }
func (BaseTool) InputsEquivalent(a, b ToolInput) bool                                     { return string(a) == string(b) }
func (BaseTool) CheckPermissions(_ context.Context, _ ToolInput) error                    { return nil }
func (BaseTool) GetPath(_ ToolInput) string                                               { return "" }
func (BaseTool) PreparePermissionMatcher(_ ToolInput) PermissionMatcher                   { return nil }
func (BaseTool) MapToolResultToBlock(result ToolResult) []ContentBlock                    { return []ContentBlock{{Type: "text", Content: string(result)}} }
func (BaseTool) ToAutoClassifierInput(input ToolInput) map[string]interface{}              { return map[string]interface{}{"raw_input": string(input)} }
func (BaseTool) GetActivityDescription(_ ToolInput) string                                { return "" }
func (BaseTool) RenderToolUseMessage(_ ToolInput) NarrationMessage                        { return NarrationMessage{} }
func (BaseTool) RenderToolResultMessage(_ ToolInput, _ ToolResult) NarrationMessage       { return NarrationMessage{} }
func (BaseTool) RenderToolErrorMessage(_ ToolInput, _ error) NarrationMessage             { return NarrationMessage{} }
func (BaseTool) ShouldShowResultInNarration() bool                                        { return true }
func (BaseTool) NarrationDetail(_ ToolResult) string                                      { return "" }

// **必须 impl 重写**（无默认）：Name / Description / UserFacingName / NarrationVerb / Execute（共 5 方法）
```

实现示例（kb_search）：

```go
type kbSearchTool struct {
    BaseTool
    rag salesrag.SalesRAGBiz
}

func (t *kbSearchTool) Name() string             { return "kb_search" }
func (t *kbSearchTool) Description() string      { return "..." }
func (t *kbSearchTool) UserFacingName() string   { return "知识库检索" }
func (t *kbSearchTool) NarrationVerb() string    { return "检索" }
func (t *kbSearchTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) { ... }
```

仅 5 方法需要重写，其余 31 个由 BaseTool 提供默认值。

### 4.4 #2 兼容性：MinimalTool 保留 + adapter

```go
// MinimalTool 是 #2 引入的 3 方法接口（保留向后兼容）。
// 新代码应用 FullTool，旧 #2 mock 工具继续用 MinimalTool。
type MinimalTool interface {
    Name() string
    Description() string
    Run(ctx context.Context, input json.RawMessage) (json.RawMessage, error)
}

// MinimalToFullAdapter 把 MinimalTool 包装为 FullTool。
type MinimalToFullAdapter struct {
    BaseTool
    impl MinimalTool
}

func WrapMinimal(m MinimalTool) FullTool {
    return &MinimalToFullAdapter{impl: m}
}

func (a *MinimalToFullAdapter) Name() string                                              { return a.impl.Name() }
func (a *MinimalToFullAdapter) Description() string                                       { return a.impl.Description() }
func (a *MinimalToFullAdapter) UserFacingName() string                                    { return a.impl.Name() } // 用 Name 兜底
func (a *MinimalToFullAdapter) NarrationVerb() string                                     { return "执行" }
func (a *MinimalToFullAdapter) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
    out, err := a.impl.Run(ctx, json.RawMessage(input))
    return ToolResult(out), err
}
```

`einoToolAdapter`（#2）改造：接受 `FullTool`，把 MinimalTool 用 WrapMinimal 升级。

---

## §5 M4 ToolFactory 插件模式

### 5.1 接口（蓝本 §4.2.2）

```go
// internal/numind/biz/agent/factory.go
package agent

import "context"

type ToolFactory interface {
    // FactoryID 唯一标识（用于 tool_factory_registry.factory_id）
    FactoryID() string

    // Source 工具来源类型
    Source() string // "platform" | "mcp" | "cli" | "webhook"

    // DisplayName 给运营看的工厂名
    DisplayName() string

    // LoadTools 加载工厂内所有工具，返回 FullTool 列表 + 工具元信息（用于 seed tool_definition）
    LoadTools(ctx context.Context) ([]FullTool, []ToolMetadata, error)

    // Watch 监听工厂内工具变化（v1 noop；#10 dynamic CRUD 用）
    Watch(ctx context.Context, onChange func(diff ToolDiff)) error
}

// ToolMetadata 工厂报告给 tool_definition 的元信息
type ToolMetadata struct {
    ToolName         string
    DisplayName      string
    Description      string
    Source           string
    RiskLevel        string
    RequiresSandbox  bool
    RequiresTenantWhitelist bool
    InputSchema      json.RawMessage
    Category         string
}

type ToolDiff struct {
    Added   []ToolMetadata
    Removed []string // tool names
    Updated []ToolMetadata
}
```

### 5.2 PlatformToolFactory 实现

```go
// internal/numind/biz/agent/factory_platform.go
package agent

import (
    "context"
    "numind-server/internal/numind/biz/salesrag"
    "numind-server/internal/numind/store"
    "numind-server/internal/pkg/aiservice"
)

type platformToolFactory struct {
    rag      salesrag.SalesRAGBiz
    ds       store.IStore
    aiClient *aiservice.Client // 假设有；如无则用包级函数
    // 6 个工具实例化时的依赖
}

func NewPlatformToolFactory(rag salesrag.SalesRAGBiz, ds store.IStore) ToolFactory {
    return &platformToolFactory{rag: rag, ds: ds}
}

func (f *platformToolFactory) FactoryID() string  { return "platform-builtin" }
func (f *platformToolFactory) Source() string     { return "platform" }
func (f *platformToolFactory) DisplayName() string { return "平台内置工具" }

func (f *platformToolFactory) LoadTools(_ context.Context) ([]FullTool, []ToolMetadata, error) {
    tools := []FullTool{
        &kbSearchTool{rag: f.rag},
        &learnerDataQueryTool{ds: f.ds},
        &documentGenerateTool{},
        &imageGenTool{},          // stub
        &bashExecTool{},          // stub
        &getCurrentDateTool{},
    }
    metadata := []ToolMetadata{
        {ToolName: "kb_search", DisplayName: "知识库检索", Description: "...", Source: "platform", Category: "RAG"},
        {ToolName: "learner_data_query", DisplayName: "学员档案查询", Description: "...", Source: "platform", Category: "查询", RiskLevel: "moderate"},
        {ToolName: "document_generate", DisplayName: "文档生成", Description: "...", Source: "platform", Category: "生成"},
        {ToolName: "image_gen", DisplayName: "图像生成", Description: "[stub]...", Source: "platform", Category: "多媒体", RequiresSandbox: true},
        {ToolName: "bash_exec", DisplayName: "代码执行", Description: "[stub]...", Source: "platform", Category: "代码", RiskLevel: "dangerous", RequiresSandbox: true},
        {ToolName: "get_current_date", DisplayName: "当前日期", Description: "...", Source: "platform", Category: "查询"},
    }
    return tools, metadata, nil
}

func (f *platformToolFactory) Watch(_ context.Context, _ func(diff ToolDiff)) error {
    return nil // v1 noop
}
```

---

## §6 M5 AgentToolRegistry

```go
// internal/numind/biz/agent/registry.go
package agent

import (
    "context"
    "fmt"
    "sync"
    "time"

    "numind-server/internal/numind/store"
)

type AgentToolRegistry interface {
    // RegisterFactory 注册工厂（启动时调用）
    RegisterFactory(f ToolFactory) error

    // LoadAll 触发所有已注册工厂 LoadTools，seed tool_definition 表，建立 in-mem 索引
    LoadAll(ctx context.Context) error

    // GetTool 按名称查找工具（运行时由 AgentRunner 调用）
    GetTool(name string) (FullTool, bool)

    // ListEnabled 列出所有 enabled 工具（按 tool_definition.is_enabled 过滤）
    ListEnabled(ctx context.Context) ([]FullTool, error)

    // ListAllTools 不过滤的全部工具（仅 admin 用）
    ListAllTools() []FullTool
}

type agentToolRegistry struct {
    mu        sync.RWMutex
    factories []ToolFactory
    tools     map[string]FullTool // name → FullTool
    defStore  store.IToolDefinitionStore
    facStore  store.IToolFactoryRegistryStore
}

func NewAgentToolRegistry(defStore store.IToolDefinitionStore, facStore store.IToolFactoryRegistryStore) AgentToolRegistry {
    return &agentToolRegistry{
        tools:    make(map[string]FullTool),
        defStore: defStore,
        facStore: facStore,
    }
}

func (r *agentToolRegistry) RegisterFactory(f ToolFactory) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.factories = append(r.factories, f)
    return nil
}

func (r *agentToolRegistry) LoadAll(ctx context.Context) error {
    r.mu.Lock()
    defer r.mu.Unlock()

    for _, f := range r.factories {
        tools, metadata, err := f.LoadTools(ctx)
        if err != nil {
            return fmt.Errorf("LoadAll(%s): %w", f.FactoryID(), err)
        }

        // 1. seed tool_definition
        for i, m := range metadata {
            def := mdToModel(m) // helper
            if err := r.defStore.Upsert(ctx, def); err != nil {
                // log warn but continue
                continue
            }
            // 2. in-mem 索引
            r.tools[tools[i].Name()] = tools[i]
        }

        // 3. update tool_factory_registry
        _ = r.facStore.Upsert(ctx, &model.ToolFactoryRegistryRow{
            FactoryID:        f.FactoryID(),
            SourceType:       f.Source(),
            DisplayName:      f.DisplayName(),
            IsEnabled:        true,
            LoadedToolsCount: len(tools),
        })
        _ = r.facStore.UpdateLoadStats(ctx, f.FactoryID(), len(tools), time.Now())
    }
    return nil
}

func (r *agentToolRegistry) GetTool(name string) (FullTool, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    t, ok := r.tools[name]
    return t, ok
}

func (r *agentToolRegistry) ListEnabled(ctx context.Context) ([]FullTool, error) {
    defs, err := r.defStore.ListEnabled(ctx)
    if err != nil {
        return nil, err
    }
    r.mu.RLock()
    defer r.mu.RUnlock()
    var enabled []FullTool
    for _, d := range defs {
        if t, ok := r.tools[d.ToolName]; ok {
            enabled = append(enabled, t)
        }
    }
    return enabled, nil
}

func (r *agentToolRegistry) ListAllTools() []FullTool {
    r.mu.RLock()
    defer r.mu.RUnlock()
    out := make([]FullTool, 0, len(r.tools))
    for _, t := range r.tools {
        out = append(out, t)
    }
    return out
}
```

并发安全：`sync.RWMutex`；GetTool 是 RLock（读多写少）；race detector 测试覆盖。

---

## §7 M6 6 个 platform 工具实现

### 7.1 kb_search（蓝本 v1 第一优先）

```go
// internal/numind/biz/agent/tool_kb_search.go
package agent

import (
    "context"
    "encoding/json"
    "numind-server/internal/numind/biz/salesrag"
)

type kbSearchTool struct {
    BaseTool
    rag salesrag.SalesRAGBiz
}

type kbSearchInput struct {
    Query   string   `json:"query"`
    DocIDs  []uint   `json:"doc_ids,omitempty"` // 可选，空 = 全库
}

func (t *kbSearchTool) Name() string           { return "kb_search" }
func (t *kbSearchTool) Description() string {
    return "Search the knowledge base. Input: { query: string, doc_ids?: number[] }. Returns relevant document snippets."
}
func (t *kbSearchTool) UserFacingName() string { return "知识库检索" }
func (t *kbSearchTool) NarrationVerb() string  { return "检索" }
func (t *kbSearchTool) IsReadOnly() bool       { return true }
func (t *kbSearchTool) IsSearchOrReadCommand() bool { return true }
func (t *kbSearchTool) AlwaysLoad() bool       { return true } // 蓝本：核心工具

func (t *kbSearchTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
    var in kbSearchInput
    if err := json.Unmarshal(input, &in); err != nil {
        return nil, err
    }
    verdict, err := t.rag.Retrieve(ctx, in.Query, in.DocIDs) // 真实方法签名
    if err != nil {
        return nil, err
    }
    out, _ := json.Marshal(verdict)
    return ToolResult(out), nil
}
```

### 7.2 learner_data_query

```go
type learnerDataQueryTool struct {
    BaseTool
    ds store.IStore
}

type learnerDataQueryInput struct {
    UserID uint   `json:"user_id"` // 由 runner 在调用前注入（基于 ReactRun.UserID）
    Field  string `json:"field"`   // 可选：name/membership/sop_run_count 等
}

func (t *learnerDataQueryTool) Name() string            { return "learner_data_query" }
func (t *learnerDataQueryTool) Description() string     { return "Query the current learner's profile data." }
func (t *learnerDataQueryTool) UserFacingName() string  { return "学员档案" }
func (t *learnerDataQueryTool) NarrationVerb() string   { return "查询" }
func (t *learnerDataQueryTool) IsReadOnly() bool        { return true }

func (t *learnerDataQueryTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
    var in learnerDataQueryInput
    if err := json.Unmarshal(input, &in); err != nil {
        return nil, err
    }
    user, err := t.ds.Users().GetByID(ctx, in.UserID)
    if err != nil {
        return nil, err
    }
    // 脱敏：仅返回非敏感字段
    safe := map[string]interface{}{
        "username":   user.Username,
        "tier":       user.Tier,
        "created_at": user.CreatedAt,
    }
    out, _ := json.Marshal(safe)
    return ToolResult(out), nil
}
```

### 7.3 document_generate（aiservice.Chat qwen-long）

```go
type documentGenerateTool struct {
    BaseTool
}

func (t *documentGenerateTool) Name() string            { return "document_generate" }
func (t *documentGenerateTool) Description() string     { return "Generate a long-form document." }
func (t *documentGenerateTool) UserFacingName() string  { return "文档生成" }
func (t *documentGenerateTool) NarrationVerb() string   { return "生成" }
func (t *documentGenerateTool) IsReadOnly() bool        { return false }
func (t *documentGenerateTool) MaxResultSizeChars() int { return 50000 } // 长文档限制

func (t *documentGenerateTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
    // 调 aiservice.Chat (qwen-long model)，传 taskID 用于 billing
    // 具体实现：构造 ChatRequest, model="qwen-long", system prompt 文档生成模板
    // 限于篇幅 spec 不详写，S4 implementer 按 ai-service.md 规范实施
    return ToolResult(`{"status":"ok","content":"<generated doc placeholder>"}`), nil
}
```

### 7.4 image_gen stub

```go
type imageGenTool struct {
    BaseTool
}

func (t *imageGenTool) Name() string            { return "image_gen" }
func (t *imageGenTool) Description() string     { return "[stub] Generate an image. Requires sandbox/aiservice image entry, will activate in #4." }
func (t *imageGenTool) UserFacingName() string  { return "图像生成" }
func (t *imageGenTool) NarrationVerb() string   { return "生成" }
func (t *imageGenTool) IsEnabled(cfg ToolConfig) bool { return cfg.EnableImageGen } // 默认 false

func (t *imageGenTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
    return nil, errors.New("image_gen requires aiservice.ImageGenerate entry (planned for follow-up feature)")
}
```

### 7.5 bash_exec stub

```go
type bashExecTool struct {
    BaseTool
}

func (t *bashExecTool) Name() string            { return "bash_exec" }
func (t *bashExecTool) Description() string     { return "[stub] Execute shell command in sandbox. Requires #4 sandbox-integration." }
func (t *bashExecTool) UserFacingName() string  { return "代码执行" }
func (t *bashExecTool) NarrationVerb() string   { return "执行" }
func (t *bashExecTool) IsDestructive() bool     { return true }
func (t *bashExecTool) IsEnabled(cfg ToolConfig) bool { return cfg.EnableSandbox } // 默认 false

func (t *bashExecTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
    return nil, errors.New("bash_exec requires #4 sandbox-integration")
}
```

### 7.6 get_current_date（沿用 #1 Phase 0 V2 demo 逻辑）

```go
type getCurrentDateTool struct {
    BaseTool
}

func (t *getCurrentDateTool) Name() string            { return "get_current_date" }
func (t *getCurrentDateTool) Description() string     { return "Return today's date in ISO 8601 format (YYYY-MM-DD)." }
func (t *getCurrentDateTool) UserFacingName() string  { return "当前日期" }
func (t *getCurrentDateTool) NarrationVerb() string   { return "获取" }
func (t *getCurrentDateTool) IsReadOnly() bool        { return true }

func (t *getCurrentDateTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
    return ToolResult(fmt.Sprintf(`"%s"`, time.Now().UTC().Format("2006-01-02"))), nil
}
```

---

## §8 M7 AgentRunner 集成

### 8.1 RunRequest 签名变更

```go
// internal/numind/biz/agent/runner.go（修改）
type RunRequest struct {
    UserID     uint
    SessionID  string
    Input      string
    ToolNames  []string  // ← 新：按名查 registry（替代 #2 的 Tools []tool.BaseTool）
    Hooks      *RunHooks
}
```

### 8.2 runner.go 内部装配（替换 #2 简化代码）

```go
// agentRunner 加 registry 字段
type agentRunner struct {
    runStore store.IAgentRunStore
    registry AgentToolRegistry  // ← 新
    cancels  map[uint64]context.CancelFunc
    mu       sync.Mutex
}

func NewAgentRunner(runStore store.IAgentRunStore, registry AgentToolRegistry) AgentRunner {
    return &agentRunner{
        runStore: runStore,
        registry: registry,
        cancels:  make(map[uint64]context.CancelFunc),
    }
}

func (r *agentRunner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
    // ... existing Create DB / trace / abort ctx ...

    // 装配工具：按 ToolNames 从 Registry 查
    var einoTools []tool.BaseTool
    for _, name := range req.ToolNames {
        ft, ok := r.registry.GetTool(name)
        if !ok {
            log.Warnw("AgentRunner: tool not registered", "tool_name", name, "agent_run_id", run.ID)
            continue
        }
        // FullTool → Eino tool.BaseTool 适配（沿用 #2 einoToolAdapter，但接受 FullTool）
        einoTools = append(einoTools, adaptFullToEinoTool(ft))
    }

    einoAgent, err := react.NewAgent(queryCtx, &react.AgentConfig{
        ToolCallingModel: einoAdapter,
        ToolsConfig: compose.ToolsNodeConfig{Tools: einoTools},
        MaxStep:          30,
    })
    // ... rest of Run loop ...
}

// adaptFullToEinoTool 把 FullTool 包装为 Eino tool.InvokableTool。
func adaptFullToEinoTool(ft FullTool) tool.InvokableTool {
    return &fullToolEinoAdapter{ft: ft}
}

type fullToolEinoAdapter struct {
    ft FullTool
}

func (a *fullToolEinoAdapter) Info(_ context.Context) (*schema.ToolInfo, error) {
    return &schema.ToolInfo{
        Name: a.ft.Name(),
        Desc: a.ft.Description(),
        // ParamsOneOf: 从 a.ft.InputSchema() 转换；#3 简化用空 params
    }, nil
}

func (a *fullToolEinoAdapter) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
    result, err := a.ft.Execute(ctx, ToolInput(args))
    if err != nil {
        return "", err
    }
    return string(result), nil
}
```

### 8.3 biz.go 接入

```go
// biz struct 加 registry 字段
type biz struct {
    ...
    agentToolRegistry agent.AgentToolRegistry
    agentRunner       agent.AgentRunner
}

// NewBiz 末尾：salesRAGService / ds 都已初始化后
registry := agent.NewAgentToolRegistry(ds.ToolDefinitions(), ds.ToolFactoryRegistries())
_ = registry.RegisterFactory(agent.NewPlatformToolFactory(b.salesRAGService, ds))
if err := registry.LoadAll(context.Background()); err != nil {
    log.Warnw("AgentToolRegistry.LoadAll failed at startup", "error", err)
}
b.agentToolRegistry = registry
b.agentRunner = agent.NewAgentRunner(ds.AgentRuns(), registry)
```

**IBiz 加方法**：

```go
Agents() agent.AgentRunner             // 已有（#2）
AgentTools() agent.AgentToolRegistry   // 新增（#3）
```

---

## §9 M8 测试设计

| 测试文件 | 覆盖 |
|---------|------|
| `tool_definition_test.go` | Store CRUD + Upsert idempotent |
| `tool_factory_registry_test.go` | Store CRUD |
| `tool_full_test.go` | FullTool interface 编译期断言；BaseTool 默认值 28 个方法 |
| `factory_platform_test.go` | LoadTools 返回 6 工具 + 6 metadata；DisplayName/FactoryID 正确 |
| `registry_test.go` | RegisterFactory + LoadAll + GetTool + race detector（并发 GetTool + LoadAll） |
| `tool_kb_search_test.go` | mock SalesRAGBiz，验证 Execute 调 Retrieve 参数正确 |
| `tool_learner_data_query_test.go` | mock IStore.Users().GetByID，验证脱敏字段 |
| `tool_document_generate_test.go` | mock aiservice.Chat，验证 qwen-long model + taskID |
| `tool_image_gen_test.go` | Execute 返回 error；IsEnabled(cfg.EnableImageGen=false) → false |
| `tool_bash_exec_test.go` | 同上 |
| `tool_get_current_date_test.go` | 返回 ISO 8601 |
| `runner_test.go`（修改） | ToolNames 中含未注册工具 → log + skip；含已注册 → 调通 |

---

## §10 关键不变量汇总

| # | 不变量 | 验证 |
|---|--------|------|
| 1 | #2 兼容性 | MinimalTool 保留 + WrapMinimal adapter；#2 现有 runner_test 仍通过 |
| 2 | 36 方法完整 interface | tool_full.go 编译期断言 + BaseTool 默认值覆盖 28 个 |
| 3 | bash_exec/image_gen 默认禁用 | IsEnabled(cfg) 默认 cfg.EnableSandbox/EnableImageGen=false → false |
| 4 | ToolFactory.Watch v1 noop | factory_platform.go Watch 函数返回 nil |
| 5 | INSERT IGNORE seed 不破坏运营开关 | OnConflict.DoUpdates 不含 is_enabled / is_beta |
| 6 | Registry 并发安全 | sync.RWMutex + race detector 测试 |
| 7 | aiservice 唯一入口 | document_generate 走 aiservice.Chat；不裸 HTTP |
| 8 | tool_factory_registry 表 #3 read-only | store 接口 + #3 仅 Upsert(自启动) + List；CRUD 在 #10 |
| 9 | prod 零影响 | 不动 config_prod / 不 SSH prod / 不打 v* tag |

---

## §11 实施依赖图（送 S3）

```
M1 (DDL + Model) ───────┐
M3 (FullTool iface)     ├──→ M5 (Registry) ──┐
M4 (Factory iface)      │                    ├──→ M7 (Runner 集成) ──→ M8 (测试)
M6 (6 tools)            ┘                    │
M2 (Stores) ────────────────────────────────┘
```

并行机会（Phase 1, Tier 3 disjoint）：
- M1+M2 / M3 / M4 / M6 各组工具可分组并行

---

## §12 与蓝本一致性

| spec § | 蓝本 § | 备注 |
|--------|--------|------|
| §2.1 tool_definition DDL | §8.10 | 字段对齐；webhook 加 CHECK；config_json 字段为 #10 预埋 |
| §2.2 tool_factory_registry DDL | 蓝本无 | #3 新增（仅 DDL + read-only）|
| §4 FullTool 36 方法 | §4.2.3 | verbatim 36 方法 |
| §5 ToolFactory | §4.2.2 | 接口一致；Watch v1 noop |
| §7 6 platform tools | §4.2.4 v1 工具池 | 5 个对齐蓝本；get_current_date 是 #1 过渡工具 |

---

**Spec 完成。等待独立 reviewer 审。**
