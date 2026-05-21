# Changelog — numind-server

All notable changes to numind-server are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

---

## [2.2.0] - 2026-05-21

### Added — Agent Mode v1.0-final（14-feature 终局集成）

13 个支撑 feature + 1 个集成 feature 落地：
- Agent ReAct Runtime（Eino + aiservice.Chat 真实调用）
- 5 层 hook chain（compliance / permission / budget / sandbox / narration）
- L1 + L2 双层 Memory（aiservice.Embed + SyncTurn extraction）
- Compact 自动压缩（PTL chain + MaxOutput chain + 会话恢复）
- 实时 Narration（YAML + 动态 LLM 兜底，sync.Map cache）
- 4 维 BudgetTracker（turns / credits / wall_time / daily_credits）+ admin_test 池
- L1/L2/L3 三层 Compliance（平台硬规则 + 父账户规则 + 注入检测）
- Skill 系统（12 题问卷 + 高级模式 + 历史版本）
- 7 个新 admin endpoints（compliance_rule CRUD + agent_run 强制取消 + 监控）
- 9 个 mock→真实 LLM 切换点（adapter.Generate / memory embedder / memory SyncTurn / compact / narration LLM / injection classifier / permission L3 classifier / budget token ctx flow / log observability）

### Fixed

- agent_run.AgentDefinitionID 字段从无到有（M-C3a），支持监控 join 查询
- biz/memory/provider.go SyncTurn 从 stub return nil 改为真实 aiservice.Chat 调用
- biz/compact/provider.go MockCompactProvider 改为生产 aiserviceCompactProvider
- narration/translator.go LLMFallback 从 stub 改为 sync.Map cache + 200ms timeout

### Changed (Schema)

- agent_run ADD COLUMN cancellation_requested_at (NULL) + agent_definition_id (NULL) + INDEX
- task_profile SEED 7 agent.* 行

### 0 Prod Impact

- config_prod.yaml zero diff
- 未打 git tag
- 未调 /deploy-prod
- 未推 feature 分支到 GitHub
