# ADR 0002 — Subscribe 克隆 skill 的 source_type 为 `imported_from_marketplace`（非 `subscribed`）

- **Feature**: agent-mode-v2-skill-marketplace
- **Stage**: S0-D2 revised → S4 T5 实现 → S5 ADR 补录
- **Date**: 2026-05-24
- **Status**: Accepted
- **Supersedes spec §3.1 / §3.4 / §11 D-COORD / plan §T11 AC-3 中关于 `source_type='subscribed'` 的所有表述**

## 背景

Spec 起初设计：marketplace 订阅产生的克隆 skill `source_type='subscribed'`，并在 §11 D-COORD 列了一个 forward-only migration `20260524_120200_skill_add_subscribed_source_type.sql`：把 `skill.source_type` ENUM 增加第 4 个值 `'subscribed'`。

S2/S3 阶段做 spec investigation Q3：与 v2 #1 (skill-as-artifact) 团队对齐 ENUM 设计。

## 决策

实际 ENUM 由 #1 (skill-as-artifact) 自己掌握并已预留 4 个值：

```sql
source_type ENUM('generated','custom','imported_from_template','imported_from_marketplace')
  NOT NULL DEFAULT 'custom'
```

订阅克隆使用 #1 预留的第 4 个值 `'imported_from_marketplace'`，**不再额外加 migration、不再用 `'subscribed'`**。

## 理由

1. **ENUM 单点所有权**：`skill` 表 ENUM 归 #1 管，#2 marketplace 不应在跨 feature 边界扩展；
2. **命名更准确**：`imported_from_marketplace` 表达"来源"（与 `imported_from_template` 平行），`subscribed` 表达"动作"，前者更符合 source_type 的语义；
3. **避免迁移碰撞风险**：#1 land 后再加 enum 值会触发表锁（MyISAM 不锁但 InnoDB ALTER TABLE 仍是 metadata lock），跨 feature 协调成本高；
4. **#2 仍持有 `skill_subscription` 表，订阅事实由该表负责**：不需要在 skill.source_type 里再编码"通过订阅来的"。

## 影响

| 路径 | 状态 | 说明 |
|------|------|------|
| `internal/numind/biz/marketplace/clone.go:45` | ✅ 实现正确 | `SourceType: "imported_from_marketplace"` |
| `internal/numind/biz/marketplace/clone_test.go:45` | ✅ 实现正确 | 单元断言已对齐 |
| `internal/numind/biz/marketplace/service_test.go:450` | ✅ 实现正确 | 集成断言已对齐 |
| `internal/pkg/model/skill_artifact.go:26` | ✅ #1 land 时已写 | ENUM 4 值定义 |
| `numind-web-v3/e2e/marketplace.spec.ts:233` | ✅ 已修正 | 断言 `source_type === 'imported_from_marketplace'` |
| `migrations/20260524_120200_skill_add_subscribed_source_type.sql` | ✅ 不创建 | spec §11 列出的"需要新建" migration 实际不需要 |
| spec §3.1 §3.4 §11 D-COORD 中 `subscribed` 表述 | ⚠️ 历史文本保留 | 请以本 ADR 为准 |
| plan §T11 AC-3 中 `source_type=subscribed` 期望 | ⚠️ 历史文本保留 | 请以本 ADR 为准 |

## 替代方案及其拒绝理由

- **方案 A**：加 enum 值 `'subscribed'`（spec 原案）。拒绝：跨 feature ENUM 扩展，#1 还没 land 时不应预决定；
- **方案 B**：用 source_type='custom' + 订阅关系另存。拒绝：丢失"这个 skill 来源是市场"的语义，运营和 UI 都要绕路；
- **方案 C**：source_type='generated'。拒绝：完全不符合语义。

## 回滚

无需回滚。本决策仅澄清"已实现的事实"与"spec 文本的旧表述"之间的差异，没有任何 schema/代码改动。
