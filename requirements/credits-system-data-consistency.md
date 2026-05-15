# credits-system-data-consistency

## 来源
- 提出人：用户（zchen27）
- 提出日期：2026-05-15

## 需求描述

重构 credits-system 数据模型，解决以下三类问题（详细现状见 `numind-server/docs/credits-system-data-consistency-audit.md`）：

**P0 数据不一致（prod 实测）**
- `user_booster_balance` vs Σ`credit_package(booster)` — 4 行中 3 行 drift（user 1: 6000 vs 0 幽灵余额）
- `credit_account.balance` vs Σ`credit_package(active)` — 6 个用户余额漂移（其中 5 个幽灵正余额、1 个反向）
- `trial_grant` vs `credit_package(type='trial')` — 33 vs 32 行，差 125 积分
- `usage_record.credits_deducted` 全表 SUM=0（死字段）

**P1 设计冗余（同份数据存两次）**
- `trial_grant` ↔ `credit_package(type='trial')`
- `user_booster_balance` ↔ Σ`credit_package(booster)`
- `credit_account.balance` ↔ Σ`credit_package(active)`
- `usage_record.credits_deducted` ↔ `credit_transaction.amount`

**P2 关系不清 + 缺硬 FK**
- `subscription` / `credit_cycle` / `credit_package(subscription)` 三角语义未锁定
- prod 唯一硬 FK 仅 `pricing_rule_tier.rule_id → pricing_rule.id`，关键 4 条软关联建议补硬约束

## 业务目标

- **数据正确性**：消除 prod 数据 drift（user 1 的 6000 幽灵 booster 余额是潜在客诉/营收风险）
- **可维护性**：消除新老两套表共存（`membership-credits-redesign` 的 Task 16 cleanup 未真正执行的债）
- **可观测性**：让 SOT 单一，调试 credit 问题不再需要交叉验 4 张表
- **B2B 月结准确**：父账户对公结算依赖 `credit_package.grant_source='b2b_grant'` 聚合，结构清晰后报表更可靠

## 优先级

**P0**（user 1 的 6000 幽灵 booster 是潜在营收漏洞 + 客诉风险；其他用户漂移影响余额展示准确性）

## Triage

- 推荐轨道：**待定**（依赖 audit doc §8 的 D1-D7 决策）
- 分类理由（按 5 条标准初评估，最终结果取决于路径选择）：
  1. 数据库 schema 变更：**是**（拟 DROP COLUMN / DROP TABLE / ADD CONSTRAINT，至少 3-5 条 migration）
  2. 新增 API 端点：**否**（纯数据/schema 改造，无新业务能力）
  3. 新外部服务集成：否
  4. 影响文件数：**>3**（涉及 `internal/numind/biz/credit/`、`internal/numind/store/credit.go`、`internal/numind/biz/membership/`、`internal/pkg/model/credit.go`、migrations/、可能含 grant_membership/payment biz）
  5. 高风险业务逻辑（支付/权限）：**是**（credits 扣减是支付核心路径；P0 bug 直接命中营收）
- **结论**：在 D7a（单 tracker）或 D7c（并入 membership-credits-redesign）路径下必然是 Standard Track。在 D7b（多独立 feature）路径下，删字段 / 删缓存表可能是 Hotfix，删 trial_grant / 加 FK 必然 Standard。

- 人类决定：**待 audit 阅读后回答 D1-D7 决策清单**

## 备注

- 关联 feature：
  - `membership-credits-redesign`（S5，2026-04-29 起）— 设计了 5 张新表，但 Task 16 cleanup 未真正落地是本 feature 根因
  - `credits-deduct-cycle-wiring`（completed 2026-05-15 v2.1.19）— 补丁式修了 Reserve/Reconcile 切表，但没动老表写入路径
- 参考：`DEPRECATED_FEATURES.md` 现未登记任何 credits 相关字段/表为"已下线"，本 feature 完成后应同步更新 §3
- Prod tag：v2.1.19（已包含全部 credits-deduct-cycle-wiring 修复）
- 详细 audit：`numind-server/docs/credits-system-data-consistency-audit.md`（含全景图 / caller 调研 / P0 数据 / 8 类决策清单）
