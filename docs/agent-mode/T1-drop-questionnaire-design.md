# T1 — 删除问卷模式，改为直接写提示词 (设计决策, 2026-06-17)

## 核心结论
- **运行时零改动**。`runner.go`/`runner_runstream.go`/`runner_prompt.go`/`student_run_lifecycle.go` 的双路径保留：
  - `ShouldUseV2Prompt(ad)` = `ad.SystemPrompt != ""` → `BuildSystemPromptV2`（5 段，用 SystemPrompt 作 institution 段）。
  - `SystemPrompt == ""` → `BuildSystemPromptLegacy`（读 GeneratedSkillBody / CustomSkillBody）。
- **`SystemPrompt` 成为唯一可编辑提示词字段**。新建 Agent 写 `SystemPrompt`（V2 路径）。
- **存量兼容、零迁移**：
  - 旧问卷 Agent（advanced_mode=false, system_prompt=""）继续走 legacy（读 generated_skill_body），行为字节不变。
  - 编辑器预填「有效提示词」= `system_prompt || custom_skill_body || generated_skill_body`；保存写 `system_prompt` → 该 Agent 转 V2 路径，无缝。
  - 旧高级 Agent（advanced_mode=true, custom_skill_body 有值, system_prompt=""）继续走 legacy 直到被编辑。

## 删除（用户面）
- 后端：问卷 Create 校验 (`validateRequiredQuestionnaireForCreate`)、用户 Create/Patch 里的 `QuestionnaireAnswers` / `CustomSkillBody` 请求字段、Create/Patch 里的 `Build()` 调用、`AdvancedToggle`、B7 `DefaultSkillSyncer`（接口+字段+WithDefaultSkillSyncer+router 接线+文件+测试）、`/advanced-toggle` 路由、errno 的 2 个 advanced 错误。
- 前端：问卷 Q6/Q7/Q8 + CreditSlider + 模式选择/切换弹窗 + AgentAdvancedEdit「即将上线」只读占位；改为单一直接创建页（名字/头像/描述/欢迎语/引导问题 + 提示词编辑框）。

## 保留（内部，不破坏其它功能）
- `Build()` + `QuestionnaireAnswers` 类型：**仅**作为内部「模板编译器」服务 v2 ImportTemplate（artifact/service.go）+ 10 个 seed 模板 + 旧快照 history diff。**这块属于 T4「技能/市场重设计」范畴**，T1 不动它，避免现在拆模板 schema 后 T4 再返工。
- 运行时双路径、AdvancedMode/CustomSkillBody/GeneratedSkillBody 三列、QuestionnaireAnswers 列：全部保留作存量兼容。
- compliance/skill_soft_rules.go：用自带内联 struct 读旧 agent Q10/Q11，不依赖 skill.QuestionnaireAnswers 类型，无需改。

## 校验
- Create 要求 `SystemPrompt` 非空（保证每个新 Agent 都有运行时提示词，杜绝空壳 Agent）。从模板创建 = 仅预填元数据，提示词由用户写。
