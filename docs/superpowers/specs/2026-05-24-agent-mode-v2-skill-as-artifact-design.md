# agent-mode-v2-skill-as-artifact — 技术设计

**关联**：[S0 需求卡](../../../requirements/agent-mode-v2-skill-as-artifact.md) · [S1 提案+PRD](../../../proposals/agent-mode-v2-skill-as-artifact-proposal.md) · NDF Standard track

**日期**：2026-05-24
**状态**：S2 设计（待 reviewer 审）

---

## §0 ADR — 应对 S0 留的 6 个未决项 + S1 P1 修复

| # | 决策项 | 决策 | 理由 |
|---|---|---|---|
| ADR-1 | 数据迁移触发时机 | **独立 CLI 命令**：`cmd/numind migrate-skill-from-agent`（不走 AutoMigrate）| AutoMigrate 启动时自动跑风险大（dev/qa/prod 容器启动失败 = 服务雪崩）；CLI 可重入 + 可 dry-run + 可手动 rollback。CI/CD 通过 `/deploy-dev` 工作流跑一次性 migration step |
| ADR-2 | Skill body 长度 | DB 列：`MEDIUMTEXT`（16MB 上限）；前端软限：50KB 警告；硬限：**200KB** 阻止保存（含 frontmatter）| 50KB 覆盖 99% 用例（普通 Skill 5-20KB）；200KB 防止误粘贴 Word 文档 |
| ADR-3 | Markdown 编辑器 | **CodeMirror 6**（@codemirror/lang-markdown + @codemirror/lang-yaml + 自定义 frontmatter 语法）| 已在 v1 #10 configurator-ux 评估过；轻量（~40KB gzip）；Vue 3 集成成熟；frontmatter 高亮支持好 |
| ADR-4 | Skill vs SkillTemplate 关系 | **分表保留**：v1 `skill_template` 不动（平台预置 10 模板）；新 `skill` 表 `source_type` 枚举 `generated`/`custom`/`imported_from_template`/`imported_from_marketplace`（marketplace v2 #3 用）；从 SkillTemplate 派生时复制 body 到 skill 行 + `source_template_id` 引用 | 平台模板与租户 Skill 生命周期不同（平台模板由 Numind 运营管，租户 Skill 由父账户自管）；分表语义清晰 |
| ADR-5 | frontmatter 字段单列存储 | **混合**：`name VARCHAR(100)` / `description VARCHAR(300)` / `when_to_use VARCHAR(500)` 单列（便于索引和搜索）；`allowed_tools JSON`（数组结构）；`body_md MEDIUMTEXT` 单独列；frontmatter 原始 YAML 不持久化（每次保存时由后端从单列重组生成）| 单列查询走索引；JSON 走 GORM 序列化；原始 YAML 不存避免双源真相 |
| ADR-6 | Skill 命名唯一性 | **不强制 DB 唯一**；前端在装载页提示"已存在同名 Skill"；列表页支持按 name 排序 | B 端机构可能多人配同名（"销售训练 v1" / "销售训练 v2"）；强制唯一会引入命名冲突错误体验差 |
| ADR-7 | 草稿态 | **不做**（S1 P1-2 修复）| MVP 范围收紧；草稿需要新增字段 + 双状态管理 + 与 version 系统协调，复杂度爆炸 |
| ADR-8 | AC-8 语义边界 | **本期 AC-8 仅验 DB 状态层**（skill 表内容回滚正确），Agent 对话行为推迟至 v2 #2（S1 P1-1 修复）| runtime 没动，本期不可能验"Agent 行为"维度 |
| ADR-9 | binding 软删级联 | DELETE skill → trigger 级联 UPDATE agent_skill_binding SET is_active=0 | 软删保留审计痕迹；v2 #2 runtime 用 binding 时按 is_active=1 过滤 |
| ADR-10 | 数据迁移幂等性 | 用 INSERT...SELECT...WHERE NOT EXISTS 子查询 + skill 行用 INSERT IGNORE | 跑两次不重复派生；rollback SQL 删 binding 不删 skill（保留新创建的资产） |
| ADR-11 | frontmatter parser | Go: `gopkg.in/yaml.v3` + 自实现首行 `---` 检测；TypeScript: 新增 `js-yaml` 依赖（web-v3 当前未装，npm i 在 T11 spike 时一并加）| Go 端已有依赖；前端 js-yaml 是行业标准 + 9KB gzip 轻量 |
| ADR-12 | Skill body 渲染 | 前端展示用 **`marked`**（v1 已用 marked@17.0.3）+ DOMPurify 防 XSS | 复用现有依赖（reviewer 修正：项目用 marked 不是 markdown-it）|
| ADR-13 | **v2 包路径** | 新建 `numind-server/internal/numind/biz/skill/artifact/` **子包**：v2 所有代码（service / binding / frontmatter / migration / versioning）放这里；v1 `biz/skill/*.go`（service / versioning / skill_builder / questionnaire / templates / student_query / constants）**完全不动**，避免命名冲突和语义混淆 | v1 `biz/skill/service.go` 是 agent_definition 业务编排，v2 `biz/skill/artifact/service.go` 是独立 Skill 资产 CRUD——分包让两者语义清晰；S4 实施时所有 import 用 `.../biz/skill/artifact` 路径 |
| ADR-14 | **errno 复用策略** | 新增 v2 专属错误码（带 `Artifact` 中缀）：`ErrSkillArtifactNotFound` / `ErrSkillArtifactBodyTooLarge` / `ErrSkillArtifactVersionNotFound` / `ErrSkillArtifactBindingExists`；v1 `ErrSkillNotFound` 等保留不动（仍指 agent_definition） | 避免与 v1 errno 语义冲突；前端可按 errno Code 字符串区分（如 `"ResourceNotFound.SkillArtifact"` vs `"ResourceNotFound.Skill"`）|
| ADR-15 | **数据迁移实现位置** | **Go CLI 命令**（非 SQL stored procedure）：`cmd/numind/migrate_skill_artifact.go` 用 GORM 事务逐行处理 `agent_definition`，对每行 `INSERT skill RETURNING id` → 用该 id 直接 `INSERT binding`，**用 Go 变量传递新 skill_id 替代 SQL JOIN**。彻底避免 reviewer P0-2 指出的 same-second 创建 JOIN race | Go 控制流比 SQL 更可靠 + 可单元测试 + 跨 MySQL/SQLite 兼容（不依赖 SIGNAL/procedure）|

---

## §1 数据模型设计

### 1.1 三张新表

#### `skill`

```sql
CREATE TABLE skill (
  id                  INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  parent_user_id      INT UNSIGNED NOT NULL COMMENT '所属父账户 ID（租户隔离）',
  name                VARCHAR(100) NOT NULL COMMENT 'Skill 名称（同租户内可重名，前端提示）',
  description         VARCHAR(300) NOT NULL DEFAULT '' COMMENT '简短描述（卡片/列表展示）',
  when_to_use         VARCHAR(500) NOT NULL DEFAULT '' COMMENT '何时使用（描述触发场景，v2 #2 runtime 会注入 system prompt）',
  allowed_tools       JSON NOT NULL DEFAULT (JSON_ARRAY()) COMMENT '允许的工具白名单 []string，v2 #2 调用时临时合并到 Agent 工具白名单',
  body_md             MEDIUMTEXT NOT NULL COMMENT 'Skill 主体 Markdown 内容',
  source_type         ENUM('generated','custom','imported_from_template','imported_from_marketplace') NOT NULL DEFAULT 'custom' COMMENT '来源类型',
  source_template_id  INT UNSIGNED NULL COMMENT '若 source_type=imported_from_template，引用 skill_template.id',
  version             INT UNSIGNED NOT NULL DEFAULT 1 COMMENT '当前版本号，每次编辑 +1',
  is_active           TINYINT(1) NOT NULL DEFAULT 1 COMMENT '软删标记',
  created_by          INT UNSIGNED NOT NULL COMMENT '创建者 user_id（一般等于 parent_user_id，预留多人协作）',
  created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  KEY idx_skill_parent_active (parent_user_id, is_active, updated_at DESC),
  KEY idx_skill_source_template (source_template_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

#### `skill_history`

```sql
CREATE TABLE skill_history (
  id          INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  skill_id    INT UNSIGNED NOT NULL COMMENT '关联 skill.id',
  version     INT UNSIGNED NOT NULL COMMENT '版本号快照',
  snapshot    JSON NOT NULL COMMENT '完整 skill 行快照（name+description+when_to_use+allowed_tools+body_md+source_type+source_template_id）',
  created_by  INT UNSIGNED NOT NULL COMMENT '触发版本的用户 ID',
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uk_skill_version (skill_id, version),
  KEY idx_history_skill_created (skill_id, created_at DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

#### `agent_skill_binding`

```sql
CREATE TABLE agent_skill_binding (
  id          INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  agent_id    INT UNSIGNED NOT NULL COMMENT '关联 agent_definition.id',
  skill_id    INT UNSIGNED NOT NULL COMMENT '关联 skill.id',
  sort_order  SMALLINT NOT NULL DEFAULT 0 COMMENT '排序（用户拖拽顺序）',
  is_active   TINYINT(1) NOT NULL DEFAULT 1 COMMENT '软删（卸载时置 0）',
  bound_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  unbound_at  DATETIME NULL COMMENT '卸载时间（is_active=0 时填）',
  UNIQUE KEY uk_agent_skill (agent_id, skill_id) COMMENT '同一 agent 不能装载同一 skill 两次（含已卸载，复装时改 is_active=1）',
  KEY idx_binding_agent_active_sort (agent_id, is_active, sort_order),
  KEY idx_binding_skill_active (skill_id, is_active)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

### 1.2 v1 `agent_definition` 表 deprecated 字段

不删字段，加 column comment + 代码侧 `// Deprecated: ` 注释：

```sql
ALTER TABLE agent_definition
  MODIFY COLUMN generated_skill_body TEXT NOT NULL COMMENT '【v2 已废弃】v1 嵌入式 skill body；v2 #2 接管 runtime 后改读 skill 表，v2 #1 期间双读保留',
  MODIFY COLUMN custom_skill_body TEXT NOT NULL COMMENT '【v2 已废弃】v1 高级模式 skill body；同上',
  MODIFY COLUMN tool_flags JSON NOT NULL COMMENT '【v2 已废弃】v1 Agent 级工具白名单；v2 #2 后改用 skill.allowed_tools 合并';
```

### 1.3 索引设计

| 索引 | 用途 | EXPLAIN 验证场景 |
|---|---|---|
| `skill.idx_skill_parent_active` | 父账户 Skill 列表分页 | `SELECT ... FROM skill WHERE parent_user_id=? AND is_active=1 ORDER BY updated_at DESC LIMIT 20` |
| `skill.idx_skill_source_template` | 平台模板派生计数 | `SELECT COUNT(*) FROM skill WHERE source_template_id=?` |
| `skill_history.uk_skill_version` | rollback 时按版本号查快照 | `SELECT snapshot FROM skill_history WHERE skill_id=? AND version=?` |
| `agent_skill_binding.idx_binding_agent_active_sort` | Agent 编辑器加载已装载 Skill 列表 | `SELECT ... FROM binding WHERE agent_id=? AND is_active=1 ORDER BY sort_order` |
| `agent_skill_binding.idx_binding_skill_active` | DELETE Skill 时查级联影响 + Skill 装载 Agent 数统计 | `SELECT COUNT(*) FROM binding WHERE skill_id=? AND is_active=1` |

### 1.4 FK 约束

为简化设计与对齐项目惯例（v1 agent_mode 系列大多不加硬 FK），不加 FOREIGN KEY 约束。完整性由 biz 层维护。**例外**：`skill_history.skill_id` ON DELETE 实际不会触发（skill 软删不物理删），但代码层 service.DeleteSkill 不级联删 history（保留审计）。

---

## §2 数据迁移设计

### 2.1 Migration SQL（双文件）

**Forward**：`migrations/20260526_120000_create_skill_tables.sql`

```sql
-- 1. 创建 3 张新表（DDL 见 §1.1）
CREATE TABLE skill ...
CREATE TABLE skill_history ...
CREATE TABLE agent_skill_binding ...

-- 2. agent_definition 字段 deprecated 注释（不改类型不删字段）
ALTER TABLE agent_definition MODIFY COLUMN ... (见 §1.2)
```

**Forward 数据迁移**：**Go CLI 代码**（ADR-15 修订，替代 SQL stored procedure）

迁移逻辑放在 `numind-server/cmd/numind/migrate_skill_artifact.go`，由 `numind migrate-skill-from-agent` 子命令调用。核心算法（伪代码）：

```go
// cmd/numind/migrate_skill_artifact.go
func RunMigration(ctx context.Context, db *gorm.DB, dryRun bool, batchSize int) (*MigrationStats, error) {
    stats := &MigrationStats{}
    return stats, db.Transaction(func(tx *gorm.DB) error {
        var offset int
        for {
            // 1. 批量取未迁移的 active agent_definition（用 LEFT JOIN 排除已有 binding 的）
            var agents []model.AgentDefinition
            err := tx.Raw(`
                SELECT ad.* FROM agent_definition ad
                LEFT JOIN agent_skill_binding b ON b.agent_id = ad.id AND b.is_active = 1
                WHERE ad.is_active = 1 AND b.id IS NULL
                ORDER BY ad.id LIMIT ? OFFSET ?
            `, batchSize, offset).Scan(&agents).Error
            if err != nil { return err }
            if len(agents) == 0 { break }

            for _, ad := range agents {
                if dryRun {
                    stats.WouldMigrate++
                    continue
                }
                // 2. 派生 skill（用 ad 字段决定 body 和 source_type）
                bodyMd := ad.GeneratedSkillBody
                sourceType := "generated"
                if ad.AdvancedMode == 1 {
                    bodyMd = ad.CustomSkillBody
                    sourceType = "custom"
                }
                allowedTools, _ := json.Marshal(ad.ToolFlags)
                skill := model.Skill{
                    ParentUserID: ad.ParentUserID,
                    Name:         ad.Name + " 的默认技能",
                    Description:  ad.Description,
                    WhenToUse:    "从 v1 Agent 迁移派生，未指定使用场景",
                    AllowedTools: datatypes.JSON(allowedTools),
                    BodyMd:       bodyMd,
                    SourceType:   sourceType,
                    Version:      1,
                    IsActive:     1,  // GORM default:true bool 陷阱由 §1.1 注意；这里显式 Create + Select("*") 兜底
                    CreatedBy:    ad.CreatedBy,
                    CreatedAt:    ad.CreatedAt,
                    UpdatedAt:    ad.UpdatedAt,
                }
                if err := tx.Select("*").Create(&skill).Error; err != nil {
                    return fmt.Errorf("RunMigration: create skill for agent %d: %w", ad.ID, err)
                }
                // 3. 立即用 skill.ID 写 binding（彻底避免 SQL JOIN race —— reviewer P0-2 修复）
                binding := model.AgentSkillBinding{
                    AgentID:   ad.ID,
                    SkillID:   skill.ID,
                    SortOrder: 0,
                    IsActive:  1,
                    BoundAt:   ad.UpdatedAt,
                }
                if err := tx.Select("*").Create(&binding).Error; err != nil {
                    return fmt.Errorf("RunMigration: create binding for agent %d: %w", ad.ID, err)
                }
                // 4. 写 history v1
                snapshot, _ := json.Marshal(skill)
                history := model.SkillHistory{
                    SkillID:   skill.ID,
                    Version:   1,
                    Snapshot:  datatypes.JSON(snapshot),
                    CreatedBy: ad.CreatedBy,
                    CreatedAt: ad.CreatedAt,
                }
                if err := tx.Create(&history).Error; err != nil {
                    return fmt.Errorf("RunMigration: create history for skill %d: %w", skill.ID, err)
                }
                stats.Migrated++
            }
            offset += len(agents)
            if len(agents) < batchSize { break }
        }
        // 5. Assert：迁移后 distinct(active binding.agent_id) == count(active agent_definition where original skill embedded)
        if !dryRun {
            var activeAgentCount, activeBindingAgentCount int64
            tx.Model(&model.AgentDefinition{}).Where("is_active = 1").Count(&activeAgentCount)
            tx.Raw("SELECT COUNT(DISTINCT agent_id) FROM agent_skill_binding WHERE is_active = 1").Scan(&activeBindingAgentCount)
            if activeAgentCount != activeBindingAgentCount {
                return fmt.Errorf("RunMigration: assert failed — active_agents=%d distinct_active_bindings=%d", activeAgentCount, activeBindingAgentCount)
            }
        }
        return nil
    })
}
```

**Forward Migration SQL** 只负责建表（DDL）：`migrations/20260526_120000_create_skill_tables.sql`（保留 §1.1 的 CREATE TABLE 部分 + §1.2 的 ALTER TABLE comment）。**不含**数据迁移 SQL（数据迁移完全走 Go CLI）。

**Rollback**：Go CLI 也实现 `--rollback` flag

```go
func RunRollback(ctx context.Context, db *gorm.DB) error {
    return db.Transaction(func(tx *gorm.DB) error {
        // 1. 找出 migrated 的 binding 记录（用 source_type 标识 + skill 名后缀双重过滤，避免误删用户自建）
        var bindingIDs []uint
        tx.Raw(`
            SELECT b.id FROM agent_skill_binding b
            INNER JOIN skill s ON s.id = b.skill_id
            WHERE s.source_type IN ('generated','custom')
              AND s.name LIKE '% 的默认技能'
              AND s.version = 1
              AND NOT EXISTS (SELECT 1 FROM skill_history h WHERE h.skill_id = s.id AND h.version > 1)
        `).Scan(&bindingIDs)
        // 2. 删 binding（硬删，因为是迁移产物）
        if err := tx.Delete(&model.AgentSkillBinding{}, bindingIDs).Error; err != nil {
            return err
        }
        // 3. 默认保留 skill 行（手动取消注释才删；安全网）
        // tx.Where("source_type IN ('generated','custom') AND name LIKE '% 的默认技能' AND version = 1").Delete(&model.Skill{})
        return nil
    })
}
```

### 2.2 独立 CLI 命令（ADR-1）

```go
// cmd/numind/main.go 增加子命令
// 用法: numind migrate-skill-from-agent [--dry-run] [--batch-size 100]
```

- `--dry-run`：跑 SELECT 但不 INSERT，输出预计派生数
- `--batch-size`：分批处理（默认 100 agent/批，事务隔离），避免单事务过长
- 实际逻辑：调 store 层执行 SQL（参考 `internal/numind/store/skill.go.MigrateFromAgentDefinitions`）

### 2.3 部署集成

`scripts/cicd/release.sh`（numind-server）在 docker pull + 替换容器前**手动 SSH 跑一次性 CLI**：

```bash
docker exec numind-server-dev /app/numind migrate-skill-from-agent
```

⚠ 限定在 dev 环境（prod 不在本期范围）。

---

## §3 biz/skill/artifact 子包设计

> **ADR-13 P0 修复**：v1 `biz/skill/` 已有 14 个文件（service.go / versioning.go / skill_builder.go 等），是 agent_definition 的业务编排。v2 代码下沉到 `biz/skill/artifact/` **子包**，命名清晰且零文件冲突。

### 3.1 包结构

```
numind-server/internal/numind/biz/skill/
├── (v1 文件全部保留不动 — service.go / versioning.go / skill_builder.go / questionnaire.go / templates.go / student_query.go / constants.go / errors.go 等)
└── artifact/                    # 【v2 新增子包】
    ├── service.go               # Skill 资产 CRUD + 装载查询编排
    ├── binding.go               # Agent-Skill 装载关系操作
    ├── frontmatter.go           # Markdown ↔ frontmatter struct 双向解析
    ├── versioning.go            # version 自增 + history 快照写入
    ├── store.go                 # 数据访问接口（mock 友好）
    ├── service_test.go          # 单测
    ├── binding_test.go
    ├── frontmatter_test.go      # 含 fuzz test
    └── versioning_test.go
```

数据迁移代码不在 biz 包，单独在 `cmd/numind/migrate_skill_artifact.go`（ADR-15，被 CLI 调用）。

### 3.2 核心接口签名

```go
// service.go
package artifact  // 注意：package 名为 artifact，import 路径为 ".../biz/skill/artifact"

import (
    "context"
    "github.com/zhiyuchen/numind-server/internal/pkg/model"
)

type Service struct {
    store     IStore
    versioning *Versioning
}

type CreateRequest struct {
    Name           string   `json:"name" binding:"required,min=1,max=100"`
    Description    string   `json:"description" binding:"max=300"`
    WhenToUse      string   `json:"when_to_use" binding:"max=500"`
    AllowedTools   []string `json:"allowed_tools"`
    BodyMd         string   `json:"body_md" binding:"required,max=204800"` // 200KB 硬限
    SourceType     string   `json:"source_type" binding:"omitempty,oneof=custom generated imported_from_template"`
    SourceTemplateID *uint  `json:"source_template_id,omitempty"`
}

func (s *Service) Create(ctx context.Context, parentUserID uint, req CreateRequest) (*model.Skill, error)
func (s *Service) List(ctx context.Context, parentUserID uint, page, pageSize int) ([]model.Skill, int64, error)
func (s *Service) Get(ctx context.Context, parentUserID, skillID uint) (*model.Skill, error)
func (s *Service) Update(ctx context.Context, parentUserID, skillID uint, req CreateRequest) (*model.Skill, error)
func (s *Service) Delete(ctx context.Context, parentUserID, skillID uint) error
func (s *Service) ListHistory(ctx context.Context, parentUserID, skillID uint) ([]model.SkillHistory, error)
func (s *Service) Restore(ctx context.Context, parentUserID, skillID, version uint) (*model.Skill, error)
func (s *Service) ListBoundAgents(ctx context.Context, parentUserID, skillID uint) ([]model.AgentDefinition, error)

// binding.go
type BindingService struct {
    store IStore
}

func (b *BindingService) Attach(ctx context.Context, parentUserID, agentID, skillID uint, sortOrder int) error
func (b *BindingService) Detach(ctx context.Context, parentUserID, agentID, skillID uint) error
func (b *BindingService) Reorder(ctx context.Context, parentUserID, agentID uint, skillIDs []uint) error
func (b *BindingService) ListByAgent(ctx context.Context, parentUserID, agentID uint) ([]model.Skill, error)

// frontmatter.go
type Frontmatter struct {
    Name         string   `yaml:"name"`
    Description  string   `yaml:"description,omitempty"`
    WhenToUse    string   `yaml:"when_to_use,omitempty"`
    AllowedTools []string `yaml:"allowed_tools,omitempty"`
}

func Parse(content string) (fm Frontmatter, body string, err error)  // 解析 markdown+frontmatter
func Serialize(fm Frontmatter, body string) (string, error)           // 反向生成
```

### 3.3 frontmatter 解析规则

```text
---
name: 销售数据分析师
description: 分析销售数据并生成日报
when_to_use: 用户上传 CSV/Excel 文件并要求"分析"或"日报"时
allowed_tools:
  - web_search
  - bash_exec
---

# 销售数据分析师

你是一名擅长 ...
（这部分是 body_md）
```

**算法**：
1. 仅识别**首行** `---`（去 trim 空白）作为 frontmatter 起始；后续 `---` 一律是 markdown ruler
2. 找到首行 `---` 后向下找下一个 `---`（单独占一行），作为 frontmatter 结束
3. 中间内容用 `yaml.Unmarshal` 解析；结束行之后是 body_md
4. 若首行非 `---`，整篇都是 body_md，Frontmatter 为零值
5. 解析失败时返回 error；service 层 fallback：保留 raw content 进 body_md，frontmatter 字段为空

---

## §4 API 契约

### 4.1 用户端 `/v1/skills/*`

完整路由表在 [§4.4](#44-router-注册位置)。

#### POST `/v1/skills` — 创建 Skill

**Request**：
```json
{
  "name": "销售数据分析师",
  "description": "分析销售数据并生成日报",
  "when_to_use": "用户上传 CSV/Excel 文件...",
  "allowed_tools": ["web_search","bash_exec"],
  "body_md": "# 销售数据分析师\n你是...",
  "source_type": "custom"
}
```

**Response 200**：
```json
{ "code": 0, "message": "ok", "data": { "id": 123, "version": 1, ...full skill row } }
```

**错误码**（**ADR-14 修订**：用 `SkillArtifact` 前缀避免与 v1 ErrSkill* 冲突，errno Code 用 string 不是数字）：
- 400 `errno.ErrBind` — 参数校验失败
- 403 `errno.ErrPermissionDenied` — 子账户访问
- 413 `errno.ErrSkillArtifactBodyTooLarge`（Code: `"InvalidParameter.SkillArtifactBodyTooLarge"`，HTTP 413）— body_md > 200KB
- 404 `errno.ErrSkillArtifactNotFound`（Code: `"ResourceNotFound.SkillArtifact"`）
- 404 `errno.ErrSkillArtifactVersionNotFound`（Code: `"ResourceNotFound.SkillArtifactVersion"`）
- 409 `errno.ErrSkillArtifactBindingExists`（Code: `"Conflict.SkillArtifactBindingExists"`）— 重复装载
- 422 `errno.ErrSkillArtifactFrontmatterInvalid`（Code: `"BizError.SkillArtifactFrontmatterInvalid"`）— frontmatter 解析失败但可保留 raw

#### GET `/v1/skills?page=1&page_size=20&search=xxx&sort=updated_at_desc`

**Response 200**：
```json
{
  "code": 0,
  "data": {
    "list": [ { "id": 123, "name": "...", "version": 1, "bound_agent_count": 3, ... }, ... ],
    "total": 42
  }
}
```

`bound_agent_count` 由 join `agent_skill_binding` 计算（每个 Skill 装载到的活跃 Agent 数）。

#### GET `/v1/skills/:id`

**Response 200**：完整 Skill 行 + `bound_agents` 数组（id + name + icon_url）

#### PUT `/v1/skills/:id`

**Request** 同 POST。**Response 200**：`{ ...new skill, version: old+1 }`

#### DELETE `/v1/skills/:id`

软删 `is_active=0`；同步级联 `agent_skill_binding.is_active=0`（事务）。

**Response 200**：`{ "affected_bindings": 3 }`

#### GET `/v1/skills/:id/history`

**Response 200**：
```json
{
  "list": [ { "version": 5, "created_at": "...", "diff_summary": "..." }, ... ]
}
```

`diff_summary` 由 backend 算（"修改了 description / body_md（+12 行 -3 行）"）；diff 算法用 `sergi/go-diff` 计算 body_md 行级 diff，简短总结。

#### POST `/v1/skills/:id/restore/:version`

行为：创建新版本（`version = current_version + 1`，内容来自指定 history 快照）；返回新 skill 行。

#### GET `/v1/skills/:id/agents`

**Response 200**：装载该 Skill 的 Agent 列表（id + name + icon_url）

### 4.2 用户端 `/v1/agents/:id/skills/*`

#### POST `/v1/agents/:id/skills`

**Request**：`{ "skill_id": 123, "sort_order": 2 }`

**Response 200**：`{ "binding_id": 456, "agent_id": ..., "skill_id": ..., "sort_order": 2 }`

#### DELETE `/v1/agents/:id/skills/:skill_id`

软删 binding。

#### PUT `/v1/agents/:id/skills/reorder`

**Request**：`{ "skill_ids": [123, 456, 789] }`（数组顺序即新 sort_order 0,1,2...）

### 4.3 鉴权与错误

所有端点：
- middleware `user_token` 强制 JWT
- biz 层第一句 `if user.ParentUserID != nil { return errno.ErrPermissionDenied }`
- 资源查询 `WHERE parent_user_id = jwt.userID` 强制；不存在或跨租户均返回 404 `errno.ErrSkillNotFound`

### 4.4 router 注册位置

[numind-server/internal/numind/router.go](../../../internal/numind/router.go) — 在 `/v1/agent/skills/*` 路由组（v1 #5 已注册）**下方**新增独立 group `/v1/skills/*` 和 `/v1/agents/:id/skills/*`。**重要**：不要与 v1 `/v1/agent/skills/*` 混淆——v1 路径是 v1 #5 skill builder 路由，本期保留不动；新增的是顶层 `/v1/skills/*`。

---

## §5 前端组件设计

### 5.1 路由

[numind-web-v3/src/router/index.ts](../../../../numind-web-v3/src/router/index.ts) 新增：

```typescript
{
  path: '/config/skills',
  component: SkillList,
  meta: { requiresParent: true }
},
{
  path: '/config/skills/new',
  component: SkillEditor,
  props: { mode: 'create' },
  meta: { requiresParent: true }
},
{
  path: '/config/skills/:id',
  component: SkillDetail,
  meta: { requiresParent: true }
},
{
  path: '/config/skills/:id/edit',
  component: SkillEditor,
  props: { mode: 'edit' },
  meta: { requiresParent: true }
},
{
  path: '/config/skills/:id/history',
  component: SkillHistory,
  meta: { requiresParent: true }
}
```

**注**：`requiresParent: true` 与 v1 configurator-relocate 守卫一致（S1 P2-1）。

### 5.2 ConfigLayout 菜单扩展

[numind-web-v3/src/views/config/ConfigLayout.vue](../../../../numind-web-v3/src/views/config/ConfigLayout.vue) 在 "AI 助手" tab 旁新增 "我的技能" tab。

### 5.3 SkillEditor 双向同步逻辑

```typescript
// SkillEditor.vue 核心 logic
const rawContent = ref('')        // 编辑器原始字符串（含 frontmatter）
const frontmatterForm = ref<Frontmatter>({...})  // 右侧表单 model
const bodyMd = ref('')            // body 部分

// 编辑器变化 → 解析 → 更新表单 (debounce 300ms)
watch(rawContent, debounce(() => {
  const parsed = parseFrontmatter(rawContent.value)
  if (parsed.ok) {
    frontmatterForm.value = parsed.frontmatter
    bodyMd.value = parsed.body
    parseError.value = null
  } else {
    parseError.value = parsed.error  // 显示警告，保留 raw
  }
}, 300))

// 表单变化 → 反向生成 → 更新编辑器
function onFormChange() {
  rawContent.value = serializeFrontmatter(frontmatterForm.value, bodyMd.value)
}

// 保存时拆分发送
async function onSave() {
  await api.createSkill({
    ...frontmatterForm.value,
    body_md: bodyMd.value  // 仅 body 部分，不含 frontmatter（后端单列存）
  })
}
```

### 5.4 Agent 编辑器扩展

[numind-web-v3/src/views/config/agents/AgentEdit.vue](../../../../numind-web-v3/src/views/config/agents/AgentEdit.vue) — 在现有"工具开关"区块**上方**插入新区块 `<SkillBindingPanel :agent-id="agentId" />`。

新组件 `SkillBindingPanel.vue` 自包含装载/排序/卸载逻辑。

### 5.5 Pinia store

[numind-web-v3/src/stores/skill.ts](../../../../numind-web-v3/src/stores/skill.ts) — 经典 setup 风格：

```typescript
export const useSkillStore = defineStore('skill', () => {
  const list = ref<Skill[]>([])
  const total = ref(0)
  const loading = ref(false)
  const currentSkill = ref<Skill | null>(null)
  const history = ref<SkillHistoryItem[]>([])

  async function fetchList(page = 1, pageSize = 20, search = '') { ... }
  async function create(data: CreateSkillRequest) { ... }
  async function update(id: number, data: CreateSkillRequest) { ... }
  async function remove(id: number) { ... }
  async function fetchHistory(id: number) { ... }
  async function restore(id: number, version: number) { ... }

  return { list, total, loading, currentSkill, history,
           fetchList, create, update, remove, fetchHistory, restore }
})
```

### 5.6 API layer

[numind-web-v3/src/api/skill.ts](../../../../numind-web-v3/src/api/skill.ts) — 仅薄包装：

```typescript
export const getSkills = (params: ListParams) => request.get<ListResp>('/v1/skills', { params })
export const createSkill = (data: CreateSkillRequest) => request.post<Skill>('/v1/skills', data)
// ... 全部 11 端点
```

---

## §6 测试策略

### 6.1 Go 单元测试

| 文件 | 覆盖 |
|---|---|
| `service_test.go` | Skill CRUD / 父账户隔离 / 子账户 403 / source_type 校验 |
| `binding_test.go` | Attach/Detach/Reorder / 同 agent 重复 attach / 卸载后复装 |
| `frontmatter_test.go` | Parse/Serialize 100+ case + 1 fuzz target（含 body 含 `---` / 空 fm / 特殊字符 / UTF-8 / 极长 body） |
| `migration_test.go` | dry-run 计数正确 / 实际迁移幂等性 / rollback SQL 不删 skill |
| `versioning_test.go` | version +1 / history snapshot 完整 / restore 创建新版本 |

### 6.2 Playwright E2E

| 文件 | 覆盖 AC |
|---|---|
| `e2e/skill-crud.spec.ts`（新）| AC-1 / AC-3 / AC-7（前端层）/ AC-12（指令型） |
| `e2e/skill-version-history.spec.ts`（新）| AC-8 |
| `e2e/skill-delete-cascade.spec.ts`（新）| AC-9 |
| `e2e/skill-binding.spec.ts`（新）| AC-10 |
| `e2e/skill-permission.spec.ts`（新）| AC-6（前端层） |
| `e2e/agent-student.spec.ts`（**回归 - 不改**）| AC-5 |

### 6.3 数据库验证

| 类型 | 内容 |
|---|---|
| Migration assert | SIGNAL SQLSTATE 在数量不匹配时报错（§2.1） |
| EXPLAIN | AC-11 索引命中（手动执行 + 记录在 S5 验收文档） |

---

## §7 跨仓库 API 契约（多仓库硬条件）

### 7.1 后端契约

OpenAPI 风格摘要（不写完整 swagger，关键字段对齐）：

```yaml
/v1/skills:
  post:
    request: { name, description, when_to_use, allowed_tools[], body_md, source_type, source_template_id? }
    response: { id, parent_user_id, name, ..., version: 1, created_at, updated_at }
  get:
    query: { page, page_size, search?, sort? }
    response: { list[{id, name, description, version, bound_agent_count, updated_at}], total }

/v1/skills/{id}:
  get: response: { ...skill, bound_agents[{id, name, icon_url}] }
  put: 同 POST
  delete: response: { affected_bindings: int }

/v1/skills/{id}/history:
  get: response: { list[{version, created_at, diff_summary, created_by}] }
/v1/skills/{id}/restore/{version}:
  post: response: { ...new skill row, version: old+1 }
/v1/skills/{id}/agents:
  get: response: { list[{agent_id, name, icon_url}] }

/v1/agents/{id}/skills:
  post: request: { skill_id, sort_order? } response: { binding_id, ... }
/v1/agents/{id}/skills/{skill_id}:
  delete
/v1/agents/{id}/skills/reorder:
  put: request: { skill_ids[] }
```

### 7.2 前端约定

- 所有 API 调用必经 `src/api/request.ts` 拦截器
- 错误码 `errno.ErrSkillBodyTooLarge` (业务码 50001 待 errno 包定义) → 前端识别 + 友好提示"内容超长，请压缩到 200KB 以内"
- `errno.ErrSkillNotFound` (业务码 50002) → toast 显示
- 404 → 一般用 toast，特殊路径（详情页直接 404）→ redirect /config/skills

---

## §8 风险再评估（基于设计深度）

S1 7 个风险经过 S2 设计后状态：

| # | 风险 | 设计层缓解 | 残留风险等级 |
|---|---|---|---|
| R1 数据迁移破坏 v1 | §2.1 双文件 + assert + dry-run CLI + 幂等 SQL | 低 |
| R2 frontmatter 歧义 | §3.3 仅识别首行 `---` 规则明确 | 极低 |
| R3 编辑器选型 | ADR-3 拍板 CodeMirror 6 | 极低 |
| R4 v2 #2 兼容 | §1.2 deprecated 字段不删 + v2 #2 dual-read 设计已预留 | 低 |
| R5 配置者混淆 | §5.2 ConfigLayout 文案规范 + 首次引导卡 | 中（产品风险，S5 用 /qa 验） |
| R6 命名冲突 | ADR-6 不强制唯一 + 装载页提示 | 极低 |
| R7 v2 #2 延期 | 串行交付约定（外部协调） | 低 |

新发现风险（S2 引入）：

| # | 风险 | 缓解 |
|---|---|---|
| R8 CodeMirror 6 与 Vue 3 集成的 reactivity 边界 | S4 前端 task 拆出独立 spike（半天）评估 codemirror-bundle vue3 wrapper / 自封装；spike 失败回退 monaco-editor |
| R9 MySQL DELIMITER + procedure 跨环境兼容性 | Forward SQL 的 assert procedure 在 SQLite / TiDB 等替代环境可能失败；S4 实施时 procedure 写成可降级版本（procedure 不存在则跳过 assert，依赖外层 CI 校验） |

---

## §9 后续阶段输入

### 给 S3 plan 的输入

每个 task 应包含：
- T01 `model + migration DDL`：3 张表 model（artifact.Skill / artifact.SkillHistory / artifact.AgentSkillBinding）+ AutoMigrate 注册 + migration SQL（仅建表，不含数据迁移）
- T02 `biz/skill/artifact/frontmatter`：Parse + Serialize + 单测 + fuzz test
- T03 `biz/skill/artifact/service`：Skill CRUD（不含 history）
- T04 `biz/skill/artifact/versioning`：版本管理 + history 写入
- T05 `biz/skill/artifact/binding`：装载/卸载/排序
- T06 `cmd/numind/migrate_skill_artifact`：迁移 CLI 命令 + dry-run + rollback 模式 + 单测（参考 §2.1 Go 代码）
- T07 `controller + router`：11 端点 controller + router 注册（注意：在 `/v1/agent/skills/*` 旧路径**下方**新增 `/v1/skills/*` + `/v1/agents/:id/skills/*`，不混淆）
- T08 `errno`：新增 ErrSkillArtifactNotFound / ErrSkillArtifactBodyTooLarge / ErrSkillArtifactVersionNotFound / ErrSkillArtifactBindingExists / ErrSkillArtifactFrontmatterInvalid（ADR-14；不复用 v1 ErrSkill*）
- T09 `frontend api + store`：src/api/skill.ts + src/stores/skill.ts
- T10 `frontend SkillList.vue`：列表页 + 分页 + 搜索
- T11 `frontend SkillEditor.vue`：编辑器 + frontmatter 双向同步 + CodeMirror 6 集成（含 spike）
- T12 `frontend SkillDetail.vue + SkillHistory.vue`
- T13 `frontend SkillBindingPanel.vue`：嵌入 AgentEdit.vue 的装载面板
- T14 `frontend router + ConfigLayout`：路由注册 + 菜单 tab
- T15 `E2E spec`：5 个 spec 文件
- T16 `Go unit + fuzz test`
- T17 `S5 验证策略 + 文档`

**S5 验证策略 task（S3 必含）**：
- 验证方式：**Playwright E2E 主**（功能验收）+ **Go unit + fuzz test 辅**（频繁触达路径） + **DB EXPLAIN 手动**（性能）
- 理由：UI 交互密集、需要长期回归保护，gstack `/qa` 是一次性不留代码不合适
- 关键路径：5 个 view 全 CRUD + binding 拖拽 + version 回滚 + permission 403 + agent-student 回归

### 给 reviewer 的输入

- 重点审 §2.1 migration SQL 正确性（forward 数量校验 + rollback 安全性）
- 重点审 §3.3 frontmatter 解析规则（边界 case 是否漏）
- 重点审 §4 API 契约完整性（11 个端点 request/response 字段是否齐）
- 重点审 §1.1 schema 索引是否最优
- 验证 §0 ADR 是否完全回应 S0 留的 6 个未决项 + S1 P1 修复
