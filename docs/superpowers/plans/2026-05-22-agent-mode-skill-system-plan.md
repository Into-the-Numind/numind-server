# Agent 模式 Skill 系统 — Task Plan

> NDF v2 S3 plan | Feature: agent-mode-skill-system | #5/14
> 上游：S2 spec `docs/superpowers/specs/2026-05-22-agent-mode-skill-system-design.md`

## §1 任务总览

12 个 M task + 1 个 S5 验证策略 task（task#13）。预估总工时：1-2 天（autopilot 持续推进）。

| # | Task 名 | 主要文件（写/改） | 依赖 | 估时 |
|---|---|---|---|---|
| M1 | DB migration SQL（3 表 + rollback + seed）| `migrations/20260522_*.sql` × 8 | — | 30 min |
| M2 | GORM models（3 个）| `internal/pkg/model/agent_definition.go` + history + template | M1 | 30 min |
| M3 | Store interfaces + datastore wire + IStore 扩展 | `internal/numind/store/agent_definition.go` + skill_template.go + store.go 扩展 | M2 | 60 min |
| M4 | biz/skill 子包基础（constants + errors + questionnaire struct + errno） | `internal/numind/biz/skill/{constants,errors,questionnaire}.go` + `internal/pkg/errno/skill.go` | — | 30 min |
| M5 | skill_builder.Build + 12 Q 题映射单测 | `internal/numind/biz/skill/skill_builder.go` + `_test.go` | M4 | 60 min |
| M6 | versioning（WriteHistorySnapshot + Restore + computeChangesSummary）+ 单测 | `internal/numind/biz/skill/versioning.go` + `_test.go` | M3 + M4 | 45 min |
| M7 | templates 子模块 + 10 个内置模板 seed SQL | `internal/numind/biz/skill/templates.go` + `migrations/20260522_220300_seed_skill_template.sql` | M3 | 45 min |
| M8 | service.go（9 个业务方法）+ 单测 | `internal/numind/biz/skill/service.go` + `_test.go` | M3, M4, M5, M6 | 90 min |
| M9 | Controller + Router 注册 + 集成测试 9 端点 | `internal/numind/controller/v1/agent/skill.go` + router.go 改 + `_integration_test.go` | M8 | 90 min |
| M10 | Hook 信号传播改造（HookActionRegistry + adapter Pre/PostToolCall + runner 注入）+ 单测 | `internal/numind/biz/agent/{hooks,adapter_full_to_eino,runner}.go` + `_test.go` | — | 60 min |
| M11 | Runner Skill 注入（WithSkillStore option + Run 装配 system prompt + adapter systemPrompt + RunRequest/RunResult 字段）+ 单测 | `internal/numind/biz/agent/{runner,adapter}.go` + `_test.go` | M3, M8, M10 | 60 min |
| M12 | biz.go wire + helper.go AutoMigrate 注册 + 最终 `go test -race ./...` PASS | `internal/numind/biz.go` + `internal/numind/helper.go` | M1-M11 | 30 min |
| **M13** | **S5 验证策略**（独立 task） | spec doc 补 | — | 30 min |

总估时（**P2-6 修复 — 估时口径统一**）：
- **单 agent 串行**：≈ 12-14 小时
- **autopilot 多并行 wall clock**：≈ 6.5 小时（Wave 3 三并 + Wave 6 二并）

§5 Wave 表是 wall clock 估算（autopilot 高度并行假设）。

## §2 任务详情

### M1 — DB migration SQL

**目标**：建 3 张表 + 3 张 rollback + 1 个 seed + 1 个 seed rollback = 8 个 SQL 文件。

**文件清单**（命名严格按 `database.md §1`：`YYYYMMDD_HHMMSS_description.sql`）：

```
migrations/20260522_220000_create_agent_definition.sql
migrations/20260522_220000_create_agent_definition_rollback.sql
migrations/20260522_220100_create_agent_definition_history.sql
migrations/20260522_220100_create_agent_definition_history_rollback.sql
migrations/20260522_220200_create_skill_template.sql
migrations/20260522_220200_create_skill_template_rollback.sql
migrations/20260522_220300_seed_skill_template.sql                    （此文件由 M7 补充内容）
migrations/20260522_220300_seed_skill_template_rollback.sql           （此文件由 M7 补充内容）
```

**M1 范围**（P1-5 修复 — 边界明确）：M1 创建 **6 个 DDL 文件**（3 建表 + 3 rollback）。**seed/seed_rollback 2 个文件由 M7 创建**，M1 不预创建空文件占位。

M1 实际产出文件：
```
migrations/20260522_220000_create_agent_definition.sql
migrations/20260522_220000_create_agent_definition_rollback.sql
migrations/20260522_220100_create_agent_definition_history.sql
migrations/20260522_220100_create_agent_definition_history_rollback.sql
migrations/20260522_220200_create_skill_template.sql
migrations/20260522_220200_create_skill_template_rollback.sql
```

M7 实际产出文件：
```
migrations/20260522_220300_seed_skill_template.sql
migrations/20260522_220300_seed_skill_template_rollback.sql
```

**SQL 内容**：S2 §2.1 / §2.2 / §2.3 已给完整 DDL，直接抄。

**rollback 文件**：每个文件用 `DROP TABLE IF EXISTS xxx;` 单行。

**单独的 Go 测试**：M1 不写 Go 测试（migration 是 SQL）。

**验收**：
- 6 个 DDL 文件存在（P1-5 修复 — seed 2 个由 M7）
- DDL 正确含主键、UNIQUE INDEX、KEY 约束
- rollback 文件能干净回滚

**commit message**：`feat(agent-skill): M1 DB schema migration SQL — agent_definition + history + skill_template`

---

### M2 — GORM models

**目标**：建 3 个 model + 配套单测覆盖 default:true bool Create 踩坑。

**文件清单**：
- `internal/pkg/model/agent_definition.go`（含 `AgentDefinition` struct + `TableName()`）
- `internal/pkg/model/agent_definition_history.go`
- `internal/pkg/model/skill_template.go`

**model 内容**：S2 §2.5 已给完整 Go struct，直接抄。

**单测**：`internal/pkg/model/agent_definition_test.go`：
- `TestAgentDefinition_TableName_returnsAgentDefinition`
- `TestAgentDefinition_CreateIsActiveFalse_persists`（database.md §6 pattern — capture wantActive + UpdateColumn fixup；用 in-memory SQLite via newTestDB(t)；DB DEFAULT TRUE 会复现 bug，需手动 fixup）

**单测样例**（database.md §6 范式）：

```go
func TestAgentDefinition_CreateIsActiveFalse_persists(t *testing.T) {
    db := newTestDB(t)
    db.AutoMigrate(&model.AgentDefinition{})

    ad := &model.AgentDefinition{
        ParentUserID: 100,
        Name:         "Test",
        IsActive:     false,
    }
    wantActive := ad.IsActive
    require.NoError(t, db.Create(ad).Error)
    if !wantActive && ad.IsActive {
        require.NoError(t, db.Model(ad).UpdateColumn("is_active", false).Error)
        ad.IsActive = false
    }
    assert.False(t, ad.IsActive, "agent.IsActive should be false")

    var row model.AgentDefinition
    require.NoError(t, db.First(&row, ad.ID).Error)
    assert.False(t, row.IsActive, "DB row should have is_active=false")
}
```

**验收**：
- 3 个 model 编译过
- 单测 PASS
- `go vet ./internal/pkg/model/...` exit 0

**commit message**：`feat(agent-skill): M2 GORM models + default:true bool Create fixup test`

---

### M3 — Store interfaces + datastore wire + IStore 扩展

**目标**：实现 IAgentDefinitionStore + ISkillTemplateStore + 把 `AgentDefinitions()` / `SkillTemplates()` 加到 IStore + datastore 实现。

**文件清单**：
- `internal/numind/store/agent_definition.go`（新建，含 interface + impl + Tx 变体）
- `internal/numind/store/skill_template.go`（新建）
- `internal/numind/store/store.go`（改：IStore 接口扩展 + datastore 实现添加两方法）

**Store 实现关键点**（S2 §3 详细）：
- `Create` 含 default:true bool fixup（UpdateColumn 两步法）
- `Update` 用 `db.Save()`
- `SoftDelete` 用 `UpdateColumn("is_active", false)` + 写 history（DB Transaction 由 service 层包）
- 所有 Tx 变体：`CreateTx(tx *gorm.DB, m *model.AgentDefinition) error` 等

**单测**：`internal/numind/store/agent_definition_test.go`：
- TestStore_Create_persists
- TestStore_Create_isActiveFalseFixup
- TestStore_Update_SaveHandlesZeroBool
- TestStore_GetByID_softDeletedExcluded
- TestStore_GetByIDIncludeInactive_returnsInactive
- TestStore_ListByParent_filtersOnParentUserID
- TestStore_SoftDelete_marksInactive
- TestStore_WriteHistory_unique
- TestStore_ListHistory_includesSoftDeleted
- TestStore_MaxVersion_returnsLatest
- TestStore_GetHistoryByVersion_returnsSnapshot

测试用 in-memory SQLite via newTestDB(t)，AutoMigrate 三表。

**SkillTemplate store 单测**（同文件或独立 `_test.go`）：
- TestStore_TemplateList_returnsActiveOnly
- TestStore_TemplateGetByID

**验收**：
- IStore 扩展两方法
- datastore 实现两方法
- 11 单测 PASS（agent_definition）+ 2 单测 PASS（skill_template）
- 覆盖率目标 ≥80%

**commit message**：`feat(agent-skill): M3 Store interfaces + datastore wire + IStore extension`

---

### M4 — biz/skill 子包基础

**目标**：建 biz/skill 子包目录 + 3 个最基础文件（不含 LLM 调用）+ errno 包扩展。

**文件清单**：
- `internal/numind/biz/skill/constants.go`（PlatformBasePrompt + PlatformSafetyFooter 常量）
- `internal/numind/biz/skill/errors.go`（domain error wrap helper，如果需要的话，可省略）
- `internal/numind/biz/skill/questionnaire.go`（QuestionnaireAnswers struct + helper 函数 taskTypeDisplay / materialTypeDisplay / styleDisplay）
- `internal/pkg/errno/skill.go`（9 个 errno 常量，包括 ErrSkillVersionConflict；P2-1：500 级错误复用 `errno.InternalServerError`，本包不重复定义 ErrDBOperationFailed）

**questionnaire.go 内容**（S2 §4.1 + §4.2 helper 函数）：

```go
package skill

type QuestionnaireAnswers struct {
    Q6  []string `json:"q6,omitempty"`
    Q7  []string `json:"q7,omitempty"`
    Q8  int      `json:"q8,omitempty"`
    Q9  string   `json:"q9,omitempty"`
    Q10 string   `json:"q10,omitempty"`
    Q11 string   `json:"q11,omitempty"`
    Q12 string   `json:"q12,omitempty"`
}

// helper: 代码常量映射为中文 prompt 用语
func taskTypeDisplay(t string) string {
    switch t {
    case "analyze_data":      return "分析数据 / 报表"
    case "generate_content":  return "生成文字内容"
    case "answer_questions":  return "回答问题 / 答疑"
    case "make_plan":         return "帮助制定计划"
    case "grade_assignment":  return "批改 / 评分学员作业"
    default:                  return t
    }
}

func materialTypeDisplay(m string) string {
    switch m {
    case "text":  return "文字（笔记、日报、复盘）"
    case "csv":   return "Excel / CSV 数据表格"
    case "image": return "图片（截图、海报）"
    case "none":  return "不需要上传"
    default:      return m
    }
}

func styleDisplay(s string) string {
    switch s {
    case "friendly":     return "亲切活泼的风格"
    case "professional": return "专业严谨的风格"
    case "encouraging":  return "鼓励陪伴的风格"
    default:             return s
    }
}
```

**单测**：`internal/numind/biz/skill/questionnaire_test.go`：
- TestQuestionnaireAnswers_unmarshalOmitsEmpty
- TestQuestionnaireAnswers_unmarshalIgnoresUnknownField（演进兼容）
- Test{taskType/materialType/style}Display 三个 enum mapping

**验收**：
- 子包编译过
- errno 包 9 常量定义
- 单测 PASS

**commit message**：`feat(agent-skill): M4 biz/skill base package + errno constants + questionnaire types`

---

### M5 — skill_builder.Build

**目标**：实现 12 Q 题 → SKILL.md 组装算法 + 单测。

**文件清单**：
- `internal/numind/biz/skill/skill_builder.go`
- `internal/numind/biz/skill/skill_builder_test.go`

**实现**：S2 §4.2 已给完整伪代码，按此实现 Build()。

**单测覆盖**（12+ 用例）：

```go
TestBuild_MinimalRequiredFields_succeeds          // Q1+Q3+Q6+Q7+Q12 全填
TestBuild_MissingQ6_returnsErrSkillBuilderFailed   // Q6 空数组
TestBuild_MissingQ7_returnsErrSkillBuilderFailed
TestBuild_MissingQ12_returnsErrSkillBuilderFailed
TestBuild_OptionalQ10Q11_includedWhenSet
TestBuild_OptionalQ10Q11_skippedWhenEmpty
TestBuild_Q6MultipleSelection_rendersAll
TestBuild_Q12_friendly_rendersStyle
TestBuild_Q12_professional_rendersStyle
TestBuild_Q12_encouraging_rendersStyle
TestBuild_BodyContainsRoleAndDescriptionFromAd     // ad.Name + ad.Description
TestBuild_NilQuestionnaireAnswers_returnsErr       // ad.QuestionnaireAnswers 为 nil
TestBuild_InvalidJSON_returnsParseError
TestBuild_OutputContainsAllRequiredSections        // 角色定义 / 任务类型 / 输入材料类型 / 语气风格
```

**P2-2 修复 — nil/empty QuestionnaireAnswers 行为对齐**：
- `len(ad.QuestionnaireAnswers) == 0` → 跳过 unmarshal，qa 为零值 → Q6/Q7/Q12 校验失败 → 返回 `ErrSkillBuilderFailed.SetMessage("questionnaire.q6 required")`
- `len > 0` 但 JSON parse fail → 返回 fmt.Errorf("Build: parse questionnaire: %w", err)
- `TestBuild_NilQuestionnaireAnswers_returnsErrSkillBuilderFailed` 期望 `ErrSkillBuilderFailed`（不是 parse error）

**验收**：
- 14 个单测 PASS
- 覆盖率 ≥85%
- Build 输出含所有期望段落
- 必填校验返回 ErrSkillBuilderFailed.SetMessage("...")

**commit message**：`feat(agent-skill): M5 skill_builder Build + 14 questionnaire mapping tests`

---

### M6 — versioning.go

**目标**：实现 WriteHistorySnapshot + Restore + computeChangesSummary + 单测。

**文件清单**：
- `internal/numind/biz/skill/versioning.go`
- `internal/numind/biz/skill/versioning_test.go`

**实现**：S2 §4.4 已给伪代码 + computeChangesSummary 5 个分支。

**单测覆盖**：

```go
TestWriteHistorySnapshot_persistsCompleteRow
TestWriteHistorySnapshot_uniqueConstraintEnforced  // 同 agent_id+version 写两次 → 报错
TestComputeChangesSummary_firstPublish              // prev=nil
TestComputeChangesSummary_advancedModeToggle
TestComputeChangesSummary_softDelete                // is_active 0→1 或 1→0
TestComputeChangesSummary_restoreFromVersion        // restoreSourceVersion > 0
TestComputeChangesSummary_questionnaireChange       // 一般修改，含 Q 编号列表
TestComputeChangesSummary_truncatedTo200Chars       // 超长截断
TestRestore_returnsNewVersion_oldRetained           // 用 sqlite 集成测
TestRestore_versionNotFound_returnsErr
TestRestore_snapshotIsCompleteCopy                  // 验证 ad 的所有字段从 snapshot 恢复
```

**测试 helper（P1-2 修复）**：`newTestDB(t)` 需 AutoMigrate `&model.AgentDefinition{}` + `&model.AgentDefinitionHistory{}` 两表，让 UNIQUE 约束 + WriteHistory 操作能在 SQLite 真实运行。

**依赖修正（P1-2 修复）**：M6 真正依赖 `M2 + M4`（不依赖 M3 — versioning.go 直接操 `*gorm.DB` 与 model，不走 store interface）。 修订依赖图见 §3。M6 可提前到 **Wave 3 与 M5 并行**。

**验收**：
- 11 单测 PASS
- 覆盖率 ≥85%

**commit message**：`feat(agent-skill): M6 versioning WriteHistorySnapshot + Restore + computeChangesSummary`

---

### M7 — templates 子模块 + 10 内置模板 seed

**目标**：实现 templates.go（thin wrapper around ISkillTemplateStore）+ 编写 10 模板 seed SQL。

**文件清单**：
- `internal/numind/biz/skill/templates.go`（薄包装，主要逻辑在 store）
- `migrations/20260522_220300_seed_skill_template.sql`
- `migrations/20260522_220300_seed_skill_template_rollback.sql`

**seed SQL outline**（S2 §8 已给主键题答，M7 写完整 JSON）：

```sql
INSERT INTO skill_template (id, name, description, icon_url, category_tags, questionnaire_answers, default_tool_flags, display_order, is_active, created_at, updated_at) VALUES
(1, '学员爆款分析师', '帮你分析小红书笔记，找出爆款规律', '/icons/template-01.png',
 JSON_ARRAY('小红书运营', '数据分析'),
 JSON_OBJECT('q6', JSON_ARRAY('analyze_data'), 'q7', JSON_ARRAY('text', 'image'), 'q8', 800, 'q9', 'no_web_search', 'q10', '', 'q11', '这个问题超出我的能力范围，建议你去问老师', 'q12', 'encouraging'),
 JSON_OBJECT('code_sandbox', true, 'media_processing', true, 'web_search', false),
 10, 1, NOW(), NOW()),
(2, '周度复盘报告助手', ...),
... 共 10 行
;
```

**rollback**：`DELETE FROM skill_template WHERE id BETWEEN 1 AND 10;`

**P2-5 修复 — 测试运行环境**：seed SQL 用 MySQL `JSON_ARRAY` / `JSON_OBJECT` 函数，仅在 dev/prod 真实 MySQL DB 跑（部署时通过 migration 系统执行）。**单测不跑 seed SQL** —— templates_test.go 用 store mock 或 SQLite + 手工 Create 单条 SkillTemplate 行做测试输入。

**单测**：`internal/numind/biz/skill/templates_test.go`：
- TestTemplates_List_returnsActiveOnly (via store mock)
- TestTemplates_GetByID_returnsTemplate

**验收**：
- seed SQL 含 10 行
- rollback SQL 正确
- 单测 PASS

**commit message**：`feat(agent-skill): M7 templates module + 10 builtin template seed SQL`

---

### M8 — service.go（biz 编排核心）

**目标**：实现 Service interface 的 9 个方法（Create / Get / List / Patch / SoftDelete / ListHistory / Restore / AdvancedToggle / ListTemplates）+ 单测。

**文件清单**：
- `internal/numind/biz/skill/service.go`
- `internal/numind/biz/skill/service_test.go`

**实现要点**：
- service struct 持有 `store.IStore`（P0-3）
- 所有 mutation 方法用 `s.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {...})`
- 每方法第一步：`s.requireParentAccount(ctx, userID)`（P1-2 — 通过 store.Users() 查 ParentUserID）
- Create/Patch 调 `skill_builder.Build` + Tx 内 store.CreateTx/UpdateTx + WriteHistorySnapshot
- AdvancedToggle 在事务内：拷贝 generated → custom + advanced_mode=1 + WriteHistorySnapshot
- Restore 在事务内：读 snapshot + 应用到 ad + WriteHistorySnapshot（含 "从 v{N} 恢复" summary）
- SoftDelete 在事务内：UpdateColumn is_active=false + version+1 + WriteHistorySnapshot

**单测**（约 25 个）：

```go
TestService_Create_succeeds
TestService_Create_childAccount_returns403
TestService_Create_skillBuilderFails_returns422
TestService_Create_isActiveFalse_persists                   // GORM bool fixup
TestService_Create_writesHistoryV1
TestService_Get_returnsActive
TestService_Get_returnsSoftDeleted                            // P1-2 detail not filter
TestService_Get_otherUserAgent_returns404
TestService_List_filtersOnParentUserID
TestService_List_includeInactiveFlag
TestService_Patch_succeeds
TestService_Patch_rejectsAdvancedModeChange
TestService_Patch_rejectsParentUserIDChange
TestService_Patch_rejectsIsActiveChange
TestService_Patch_writesHistory
TestService_Patch_questionnaireChange_rebuildsBody
TestService_SoftDelete_idempotent
TestService_SoftDelete_writesHistory
TestService_ListHistory_includesSoftDeleted
TestService_Restore_succeeds
TestService_Restore_versionNotFound_returns404
TestService_Restore_createsNewVersion
TestService_AdvancedToggle_succeeds
TestService_AdvancedToggle_alreadyAdvanced_returns422
TestService_AdvancedToggle_copiesGeneratedToCustom
TestService_AdvancedToggle_preservesQuestionnaireAnswers
TestService_ListTemplates_succeeds
```

**测试用 helper**（P1-1 修复 — 明确 user 表初始化）：

`newTestService(t)` 实现细节：
```go
func newTestService(t *testing.T) (Service, *gorm.DB) {
    db := newTestDB(t)
    // P1-1 修复：AutoMigrate 含 user 表
    require.NoError(t, db.AutoMigrate(
        &model.User{},                    // requireParentAccount 需要查 user 表
        &model.AgentDefinition{},
        &model.AgentDefinitionHistory{},
        &model.SkillTemplate{},
    ))
    // 种子父账户 (id=100) + 子账户 (id=200, parent=100)
    parent := &model.User{ID: 100, Username: "parent-test"}                       // ParentUserID nil = 父账户
    require.NoError(t, db.Create(parent).Error)
    childParent := uint(100)
    child := &model.User{ID: 200, Username: "child-test", ParentUserID: &childParent}
    require.NoError(t, db.Create(child).Error)

    return NewService(newTestStore(t, db)), db
}
```

`newTestStore(t, db)` 实现 `store.IStore` 用 SQLite 真实 DB（不 mock 整个 interface）。

**验收**：
- ≈ 27 单测 PASS
- 覆盖率 ≥85%
- `go test -race` 无 data race

**commit message**：`feat(agent-skill): M8 service.go — 9 methods + ~27 unit tests`

---

### M9 — Controller + Router + 集成测试

**目标**：实现 9 个 HTTP handler + 注册到 router + 集成测试。

**文件清单**：
- `internal/numind/controller/v1/agent/skill.go`（新建目录 + controller）
- `internal/numind/router.go`（改：注册 agent 子 group）
- `internal/numind/controller/v1/agent/skill_integration_test.go`（含 httptest 集成）

**Controller 实现**：S2 §5 已给方法签名，每方法实现：
1. `c.GetUint("userID")` 获取 JWT user
2. Gin binding（参数验证）
3. `core.WriteResponse(c, err, data)` 统一响应

**Router 注册**：S2 §5 已给代码片段。需在 router.go 找到 user_token middleware 的 group 加入 agent 子 group。

**集成测试**（9 handler × ~3 case = 27 个测试）：

```go
TestController_CreateSkill_201
TestController_CreateSkill_401_noToken
TestController_CreateSkill_403_childAccount
TestController_CreateSkill_400_invalidName
TestController_CreateSkill_422_missingQ6
TestController_ListSkills_200_filtersOnParent
TestController_GetSkill_200
TestController_GetSkill_404_notFound
TestController_GetSkill_404_otherUserAgent
TestController_PatchSkill_200
TestController_PatchSkill_422_advancedModeChange
TestController_DeleteSkill_200_idempotent
TestController_DeleteSkill_404_notFound
TestController_ListHistory_200_includesAllVersions
TestController_RestoreSkill_200_createsNewVersion
TestController_RestoreSkill_404_versionNotFound
TestController_AdvancedToggle_200
TestController_AdvancedToggle_422_alreadyAdvanced
TestController_ListTemplates_200
TestController_ListTemplates_401_noToken
```

集成测试用 `httptest.NewRecorder()` + gin test engine + in-memory SQLite。

**集成测试 testSetup**（P1-4 修复 — 明确 AutoMigrate 清单）：

```go
func newTestEngine(t *testing.T) (*gin.Engine, *gorm.DB) {
    db := newTestDB(t)
    require.NoError(t, db.AutoMigrate(
        &model.User{},                      // user_token middleware 查 user
        &model.AgentDefinition{},
        &model.AgentDefinitionHistory{},
        &model.SkillTemplate{},
    ))
    // 种子父账户 100 + 子账户 200
    parent := &model.User{ID: 100, Username: "parent"}
    require.NoError(t, db.Create(parent).Error)
    cp := uint(100)
    child := &model.User{ID: 200, Username: "child", ParentUserID: &cp}
    require.NoError(t, db.Create(child).Error)
    // 种子模板 1 行（其他 9 行不强制）
    tpl := &model.SkillTemplate{ID: 1, Name: "Test Template", IsActive: true}
    require.NoError(t, db.Create(tpl).Error)

    // wire 完整栈
    ds := newTestStore(t, db)
    svc := skill.NewService(ds)
    ctrl := agentctrl.NewSkillController(svc)
    engine := gin.New()
    // mock user_token middleware（注入 JWT userID 直接进 ctx）
    engine.Use(func(c *gin.Context) {
        uid := c.GetHeader("X-Test-UserID")
        if uid != "" {
            id, _ := strconv.Atoi(uid)
            c.Set("userID", uint(id))
        }
        c.Next()
    })
    // 注册 routes
    agentGroup := engine.Group("/v1/agent")
    {
        skills := agentGroup.Group("/skills")
        skills.POST("", ctrl.Create)
        // ... 9 端点
    }
    return engine, db
}
```

请求时 `req.Header.Set("X-Test-UserID", "100")` 模拟父账户登录；`X-Test-UserID: 200` 模拟子账户（用于 403 测试）；无 header = 401 测试。

**验收**：
- 9 个 handler 编译过
- Router 注册成功
- ≈ 20 集成测试 PASS
- 覆盖率 controller ≥75%

**commit message**：`feat(agent-skill): M9 controller + router + 20 integration tests`

---

### M10 — Hook 信号传播改造

**目标**：扩展 hooks.go 加 HookActionRegistry + adapter Pre/PostToolCall 改造 + runner.Run 注入路径 + 单测。

**文件清单**：
- `internal/numind/biz/agent/hooks.go`（改：加 HookActionRegistry struct + RunHooks.Registry 字段）
- `internal/numind/biz/agent/adapter_full_to_eino.go`（改：PreToolCall + PostToolCall 后 Record）
- `internal/numind/biz/agent/runner.go`（改：在 effectiveHooks 装配后注入 Registry）
- `internal/numind/biz/agent/hooks_test.go`（加测试）
- `internal/numind/biz/agent/adapter_full_to_eino_test.go`（加测试）

**改动详情**：S2 §6.3.1-3 已给完整代码片段。

**单测**：

```go
// hooks_test.go 新增
TestHookActionRegistry_RecordLastAction
TestHookActionRegistry_RecordOverwrites
TestHookActionRegistry_Reset
TestHookActionRegistry_ConcurrentRecord_raceSafe         // -race 验证 100 goroutines 并发 Record/LastAction

// adapter_full_to_eino_test.go 新增
TestAdapter_PreToolCallStop_recordsToRegistry
TestAdapter_PreToolCallContinue_doesNotRecord            // Continue 不写 Registry
TestAdapter_PostToolCallStop_recordsToRegistry           // P0-3
TestAdapter_PostToolCallContinue_doesNotRecord
TestAdapter_RegistryNil_doesNotPanic                     // hooks.Registry=nil 不 panic

// runner_test.go 新增
TestRunner_Run_autoInjectsRegistry                       // 验证 Run 调用前 hooks.Registry 为 nil 时被自动设置
TestRunner_Run_preservesProvidedRegistry                 // caller 自己提供 Registry 时不覆盖
```

**P0-3 关键改动** `adapter_full_to_eino.go:69`：

旧：
```go
if _, postErr := a.hooks.PostToolCall(ctx, a, output, execErr); postErr != nil {
```

新：
```go
postAction, postErr := a.hooks.PostToolCall(ctx, a, output, execErr)
if a.hooks.Registry != nil && postAction != HookActionContinue {
    a.hooks.Registry.Record(postAction)
}
if postErr != nil {
    // ... 现有逻辑
}
```

**P2-4 修复 — PreToolCall 也加 Record**（adapter_full_to_eino.go::InvokableRun PreToolCall 段）：

旧：
```go
if a.hooks != nil && a.hooks.PreToolCall != nil {
    action, err := a.hooks.PreToolCall(ctx, a, args)
    if err != nil { return "", fmt.Errorf("PreToolCall: %w", err) }
    if action != HookActionContinue {
        return "", fmt.Errorf("tool execution stopped by hook: action=%d", action)
    }
}
```

新：
```go
if a.hooks != nil && a.hooks.PreToolCall != nil {
    action, err := a.hooks.PreToolCall(ctx, a, args)
    if err != nil { return "", fmt.Errorf("PreToolCall: %w", err) }
    if a.hooks.Registry != nil {
        a.hooks.Registry.Record(action)         // P2-4 修复 — 同时覆盖 PreToolCall
    }
    if action != HookActionContinue {
        return "", fmt.Errorf("tool execution stopped by hook: action=%d", action)
    }
}
```

**P2-7 修复 — runner.go 中 hook 信号判定路径**（参 S2 §6.3.3 伪代码）：

```go
// runner.go::Run() 在装配 effectiveHooks 之后立即注入 Registry
if effectiveHooks != nil && effectiveHooks.Registry == nil {
    effectiveHooks.Registry = NewHookActionRegistry()  // P1-1（已有）
}

// 在 runner.Run() 简化状态机部分（约第 173-188 行）改造：
// 当 hook 派发非 Continue 时，调 state.Transition：
if effectiveHooks != nil && effectiveHooks.Registry != nil {
    last := effectiveHooks.Registry.LastAction()
    if last != HookActionContinue {
        ev := HookActionToLoopEvent(last)
        if ev != LoopEventInvalid {
            term, _, isTerminal := st.Transition(ev)
            if isTerminal {
                st.TerminalReason = term
                // 后续 UpdateState 使用 st.TerminalReason，不再硬编码 TerminalCompleted
            }
        }
    }
}
```

#5 不调真实 Eino LLM，但这段判定路径在单测中可被驱动（手工 effectiveHooks.Registry.Record(HookActionStop) 然后调 Run 验证 TerminalReason）。

**验收**：
- 12 单测全 PASS
- `go test -race ./internal/numind/biz/agent/...` PASS
- 现有 #2 / #4 测试 0 修改 + 仍 PASS
- 覆盖率 hooks.go ≥90%

**commit message**：`feat(agent-skill): M10 HookActionRegistry + adapter Record + runner auto-inject`

---

### M11 — Runner Skill 注入

**目标**：runner.Run() 在 AgentDefinitionID > 0 时读 agent_definition + 装配 system prompt + 注入到 adapter。

**文件清单**：
- `internal/numind/biz/agent/runner.go`（改：RunRequest 加 AgentDefinitionID + SystemPrompt 字段；Run 在 Eino 装配前加 skill 注入分支；WithSkillStore option）
- `internal/numind/biz/agent/adapter.go`（改：aiserviceAdapter 加 systemPrompt 字段；Generate 时把 prompt 加到 messages[0]）
- `internal/numind/biz/agent/runner_test.go`（加测试）
- `internal/numind/biz/agent/adapter_test.go`（加测试）

**实现要点**（S2 §6.1 + §6.2）：

```go
// RunRequest
type RunRequest struct {
    // 现有字段
    AgentDefinitionID uint64
    SystemPrompt      string
}

// RunResult
type RunResult struct {
    // 现有字段
    SkillVersion int
}

// runner.Run() 新增分支
if req.AgentDefinitionID > 0 && r.skillStore != nil {
    ad, err := r.skillStore.GetByIDIncludeInactive(ctx, req.AgentDefinitionID)
    if err != nil {
        return nil, errno.ErrSkillNotFound.SetMessage(err.Error())
    }
    if ad.ParentUserID != req.UserID && /* 子账户：从 user 表查 parentUserID */ ... {
        return nil, errno.ErrSkillNotFound  // 安全：不暴露存在性
    }
    body := ad.GeneratedSkillBody
    if ad.AdvancedMode {
        body = ad.CustomSkillBody
    }
    req.SystemPrompt = skill.PlatformBasePrompt + body + skill.PlatformSafetyFooter
    skillVer = int(ad.Version)
}

// 把 req.SystemPrompt 传给 adapter
einoAdapter := &aiserviceAdapter{
    modelName:    "qwen-turbo",
    taskID:       fmt.Sprintf("agent-runner-%d", run.ID),
    systemPrompt: req.SystemPrompt,
}

// RunResult.SkillVersion 写入返回
```

**adapter 改造**：
```go
type aiserviceAdapter struct {
    // 现有字段
    systemPrompt string
}

// Generate / Stream 调 LLM 时把 systemPrompt 加到 messages[0]:
// （#5 不调真实 LLM，但实现这个分支供 #14）
```

> 注：#5 不强制实测真实 LLM 路径；S2 已明确这是 placeholder 接口准备。

**单测**（P1-3 修复 — 用 inline mockSkillStore 不影响现有 8 处 NewAgentRunner 调用）：

```go
// 在 runner_test.go 中**追加**（不覆盖现有用例）：

type mockSkillStore struct {
    fixed *model.AgentDefinition
    err   error
}
func (m *mockSkillStore) GetByIDIncludeInactive(ctx context.Context, id uint64) (*model.AgentDefinition, error) {
    return m.fixed, m.err
}
// 其他 IAgentDefinitionStore 方法 stub 返回 nil（M11 测试不调用）

TestRunner_AgentDefinitionID0_fallThroughMock              // 兼容 #2 mock 行为
TestRunner_AgentDefinitionIDValid_loadsSkill
TestRunner_AgentDefinitionIDValid_advancedMode_usesCustomBody
TestRunner_AgentDefinitionIDValid_skillStoreErr_returnsErr
TestRunner_AgentDefinitionID_otherUser_returnsNotFound      // 安全：跨用户不暴露
TestRunner_WithSkillStore_optionInjects
TestRunner_RunResult_SkillVersion_setWhenSkillLoaded
TestRunner_RunResult_SkillVersion_zeroWhenFallThrough

TestAdapter_SystemPromptInjected_inMessages0                // adapter.Generate 把 systemPrompt 作为 messages[0]
TestAdapter_SystemPromptEmpty_messagesUnchanged
```

**验收**：
- 10 单测 PASS
- 现有 #2 测试 0 修改 + 仍 PASS
- 覆盖率 runner ≥80%

**commit message**：`feat(agent-skill): M11 runner Skill injection + WithSkillStore option + adapter systemPrompt`

---

### M12 — biz.go wire + AutoMigrate 注册

**目标**：把所有改动 wire 到 biz.go + helper.go AutoMigrate + 最终全包 race test PASS。

**文件清单**：
- `internal/numind/biz.go`（改：构造 NewAgentRunner 时加 WithSkillStore option；构造 SkillService）
- `internal/numind/helper.go`（改：AutoMigrate 加三新 model）
- `internal/numind/biz_test.go`（如有则补 / 或 0 改）

**改动要点**：
- biz.go 中找到 `agent.NewAgentRunner(...)` 调用处，加 `agent.WithSkillStore(ds.AgentDefinitions())`
- biz.go 构造 `skillService := skill.NewService(ds)` + 传入 controller
- helper.go AutoMigrate 三表（在 #4 sandbox session 表的旁边加）

**验收**：
- `go build ./...` PASS（整包编译）
- `go vet ./...` PASS
- `go test -race ./...` PASS（全包跑过 race detector）
- `task lint` PASS

**commit message**：`feat(agent-skill): M12 biz wire + AutoMigrate + final race test PASS`

---

### M13 — S5 验证策略（独立 task）

> **规则 10 强制**：S3 plan 必须含独立 S5 验证策略 task。

**目标**：明确 S5 acceptance 时的验证方式 + 关键用户路径 + 工件清单。

**输出文件**：S5 时写到 `docs/superpowers/qa/2026-05-22-agent-mode-skill-system-s5-acceptance.md`，本 task 仅在 S3 plan 中记录策略 outline。

**验证方式**：
- **Go 单测**：每 task M1-M11 单测覆盖（90+ test cases）
- **集成测**：M9 controller 集成 + service 跨 store 集成（in-memory SQLite via newTestDB）
- **race detector**：`go test -race -count=3 ./...`
- **覆盖率**：biz/skill ≥80%；biz/agent 不下降（保持 80%+）
- **不需要 Playwright / gstack `/qa`**：本 feature 仅后端 biz + API，无 UI（UI 在 #10 / #11）
- **不需要真实 LLM 调用**：spec 明确不调（#14 范围）

**关键用户路径验证**（S5 必须验证）：
1. **创建 → 列表 → 详情** 全链路（含 default:true bool fixup）
2. **PATCH 修改 questionnaire → 重组装 SKILL.md → version+1 → 写 history**
3. **DELETE 软删除 → 详情仍返回（is_active=0）→ 历史仍可查**
4. **POST restore → 创建新版本 = max+1 → 旧版本保留**
5. **POST advanced-toggle → advanced_mode 1→0 拒绝**
6. **GET history 含已软删除 agent 的所有版本**
7. **子账户调用全部 9 端点 → 全 403**
8. **questionnaire JSON schema 演进：旧 snapshot 含未知字段 → unmarshal 不 fail**
9. **Hook 信号 → terminal_reason 真实派发**（HookActionStop → hook_stopped）
10. **Skill body 注入到 adapter.systemPrompt 字段**

**回归保护诚实声明**：本 feature 用 Go 单测 + 集成测，全部留在代码库做永久回归保护。无需手动重跑 QA。

**S5 acceptance record 必须含**：
- 覆盖率截图（biz/skill ≥80% 证据）
- `go test -race -count=3 ./...` 输出（PASS 证据）
- 每个 M task commit hash + reviewer 结论
- 代码量统计（新增 LOC + 测试 LOC）
- 0 prod 影响声明

**commit message**：`docs(agent-skill): M13 S5 validation strategy locked in (plan)`

> 说明：M13 task 在 S3 plan 中描述策略，但具体 acceptance record 在 S5 阶段写。这是 S3 / S5 的契约。

## §3 任务依赖图（拓扑序，**P1-2 修复后**）

```
M1 (migration SQL)
 └─► M2 (GORM models)
      ├─► M3 (Store + IStore扩展)
      │    ├─► M7 (templates)  [需 M3]
      │    └─► M8 (service)    [需 M3, M4, M5, M6]
      │         └─► M9 (controller + router)
      │              └─► M12 (biz.go wire) [需所有]
      │
      └─► M6 (versioning) [需 M2 + M4 — P1-2 修复：不依赖 M3]

M4 (biz/skill 基础)
 ├─► M5 (skill_builder)
 ├─► M6 (versioning)
 └─► M8 (service)

M10 (Hook 信号传播) [独立]
 └─► M11 (Runner Skill 注入) [需 M3, M8, M10 commit 合并]
      └─► M12

M13 (S5 验证策略) [独立 doc]
```

## §4 并行执行计划（Tier 分析）

### 4.1 Wave 1（无依赖，可并行 Tier 3）

| Task | 文件归属（**P0-1 修复 — 含所有 _test.go**） |
|---|---|
| M1 | `migrations/20260522_220000_create_agent_definition.sql` + `_rollback.sql` + 220100 历史表两文件 + 220200 模板表两文件（6 文件）|
| M4 | `internal/numind/biz/skill/constants.go` + `errors.go` + `questionnaire.go` + `questionnaire_test.go` + `internal/pkg/errno/skill.go` |
| M10 | `internal/numind/biz/agent/hooks.go` + `hooks_test.go` + `adapter_full_to_eino.go` + `adapter_full_to_eino_test.go` + `runner.go` + `runner_test.go`（追加测试，不覆盖现有用例） |

**ndf-check-disjoint 验证**（S4 实操时跑，P0-1 修复 — 含 _test.go）：

```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "migrations/20260522_220000_create_agent_definition.sql,migrations/20260522_220100_create_agent_definition_history.sql,migrations/20260522_220200_create_skill_template.sql,migrations/20260522_220000_create_agent_definition_rollback.sql,migrations/20260522_220100_create_agent_definition_history_rollback.sql,migrations/20260522_220200_create_skill_template_rollback.sql" \
  "internal/numind/biz/skill/constants.go,internal/numind/biz/skill/errors.go,internal/numind/biz/skill/questionnaire.go,internal/numind/biz/skill/questionnaire_test.go,internal/pkg/errno/skill.go" \
  "internal/numind/biz/agent/hooks.go,internal/numind/biz/agent/hooks_test.go,internal/numind/biz/agent/adapter_full_to_eino.go,internal/numind/biz/agent/adapter_full_to_eino_test.go,internal/numind/biz/agent/runner.go,internal/numind/biz/agent/runner_test.go"
```

**预期**：exit 0（M1 / M4 / M10 文件完全不交集，注意 M10 拥有 runner.go 但 M11 在 Wave 6 才动 runner.go，串行依赖见 §4.6）。

但**注意 M10 改了 runner.go**，M11 也改 runner.go — M10 / M11 必须**串行**，且 Wave 1 / Wave 6 之间串行（M11 依赖 M10 完成）。

### 4.2 Wave 2

| Task | 依赖 |
|---|---|
| M2 | M1（migration） |

只能 M2 单跑，因为 M2 model 依赖 M1 schema 一致性。

### 4.3 Wave 3（修订 — M6 提前并入）

See §4.3.1 below.

### 4.3.1 Wave 3（M6 提前 — P1-2 修复）

| Task | 依赖 | 文件归属 |
|---|---|---|
| M3 | M2 | `internal/numind/store/agent_definition.go` + `_test.go` + `skill_template.go` + `_test.go` + `store.go`（扩展 IStore + datastore）|
| M5 | M4 | `internal/numind/biz/skill/skill_builder.go` + `_test.go` |
| M6 | M2 + M4（**P1-2 修复 — 不依赖 M3**） | `internal/numind/biz/skill/versioning.go` + `_test.go` |

3 task 并行 Tier 3（文件完全不交集）。

### 4.4 Wave 4

| Task | 依赖 |
|---|---|
| M7 | M3 |
| M11 | M3 + M8 + M10（注意 M8 还没完成；M11 暂等到 Wave 6）|

Wave 4 单跑 M7（M6 已提前到 Wave 3）。

### 4.5 Wave 5

| Task | 依赖 |
|---|---|
| M8 | M3, M4, M5, M6 |

M8 单跑。

### 4.6 Wave 6

| Task | 文件归属（**P0-2 修复 — 显式列全**）| 依赖 |
|---|---|---|
| M9 | `internal/numind/controller/v1/agent/skill.go` + `internal/numind/controller/v1/agent/skill_integration_test.go` + `internal/numind/router.go` | M8 |
| M11 | `internal/numind/biz/agent/runner.go` + `internal/numind/biz/agent/runner_test.go` + `internal/numind/biz/agent/adapter.go` + `internal/numind/biz/agent/adapter_test.go` | M3 + M8 + M10 |

**P0-2 关键前置**：M11 implementer 必须在 M10 commit 已合并后开始，**M11 implementer 必须在 worktree 内基于 M10 的 runner.go 改动（含 HookActionRegistry 注入逻辑）追加 skillStore + AgentDefinitionID 处理**。

**P0-3 验证步骤**（implementer 必跑）：
```bash
# Wave 6 启动前：
cd /private/tmp/wt-agent-mode-skill-system-numind-server
git log --oneline -5 internal/numind/biz/agent/runner.go
# 必须看到 M10 commit（"M10 HookActionRegistry + adapter Record + runner auto-inject"），
# 否则 M11 不能开始 — implementer 主动报告"M10 missing"给主控
```

**ndf-check-disjoint Wave 6 验证**：
```bash
bash numind-server/scripts/ndf/ndf-check-disjoint.sh \
  "internal/numind/controller/v1/agent/skill.go,internal/numind/controller/v1/agent/skill_integration_test.go,internal/numind/router.go" \
  "internal/numind/biz/agent/runner.go,internal/numind/biz/agent/runner_test.go,internal/numind/biz/agent/adapter.go,internal/numind/biz/agent/adapter_test.go"
```

**预期**：exit 0（M9 / M11 文件完全不交集）。

### 4.7 Wave 7

| Task | 依赖 |
|---|---|
| M12 | M1-M11（所有） |

M12 单跑，wire + final test。

### 4.8 Wave 8（独立）

| Task | 依赖 |
|---|---|
| M13 | — |

M13 是 doc-only，任何时间写都可。建议在 Wave 5 期间穿插写。

## §5 整体推进策略（按 Wave）

| Wave | 任务 | 预估 wall clock | 并行度 |
|---|---|---|---|
| 1 | M1 + M4 + M10 | 60 min | Tier 3 ✓ |
| 2 | M2 | 30 min | 单跑 |
| 3 | M3 + M5 + M6 | 60 min | Tier 3 ✓（P1-2 修复：M6 已提前到 Wave 3） |
| 4 | M7 | 45 min | 单跑 |
| 5 | M8 | 90 min | 单跑 |
| 6 | M9 + M11 | 90 min | Tier 3 ✓ |
| 7 | M12 | 30 min | 单跑 |
| 8 | M13（doc 穿插） | 30 min | — |

**总 wall clock ≈ 6.5 小时**（autopilot 实际）。

## §6 每 task 后的强制步骤

按 NDF 规则 6（每 task 两 reviewer + 0 must-fix 才能下一 task）：

1. Task 实施完成 + 主 session commit
2. **并行** dispatch 2 个 Sonnet reviewer subagent：
   - Spec Compliance Review — 对照 S2 spec 检查实施一致性
   - Code Quality Review — Go 风格 / 项目规则 / database.md 踩坑
3. 修复 P0/P1（顺手 P2）
4. 更新 manifest `progress.reviewed_tasks += 1`
5. 进下一 task

**Bug-from-Customer**：本 feature **不是 bug 修复**，无需写复现测试。

**事务边界保证**（service 层）：每个 mutation 方法必须在 `tx := s.ds.DB().Transaction(...)` 内：
1. store mutation（Create/Update/SoftDelete）
2. WriteHistorySnapshot

reviewer 检查这一点。

## §7 commit message 规范

每 task commit 格式：

```
feat(agent-skill): M{N} <短描述>

<详细说明 2-3 行>

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
```

S5 / S6 / S7 commit 用 `docs(ndf-s5)` / `chore(ndf-s6)` 前缀。

## §8 验收前置（S5 之前 reviewer 必查）

1. M1-M11 都有 commit
2. manifest.progress.reviewed_tasks == completed_tasks
3. M9 + M11 集成测在 `go test -race ./...` PASS
4. M13 写到 plan（本文件）

## §9 风险

| 风险 | 缓解 |
|---|---|
| Wave 1 M10 改 runner.go + M11 改 runner.go 串行冲突 | Wave 设计已确保 M11 在 M10 之后串行执行 |
| M9 改 router.go 时与其他 session b2b-billing 改 router.go 冲突 | b2b 在另一 worktree（fix/b2b-billing-rules-rewrite），物理隔离；merge 时 conflict resolution |
| M7 seed SQL 10 模板 JSON 体长 ≥ 1KB/行 | 用 `pretty-format` + heredoc 避免单 行超长，commit 时正常 |
| service.go 单测 ≈ 27 个 LOC 大 | 用 table-driven test 压缩 |

---

**S3 完结。S4 按 Wave 实施 M1-M12 + M13 穿插。**
