# 会员积分体系重构：状态机 + cron → 时间驱动 + lazy

## 来源
- 提出人：用户（产品决策方）
- 提出日期：2026-04-29

## 需求描述

当前会员积分体系存在双制并存（legacy_tier + credits）+ 多包预切月份 + cron 状态推进的复杂结构，导致到期/续费/激活之间存在空档、状态机和到期时间不同步、客服困扰。需要彻底重构为**时间驱动 + 懒创建**模型，对齐 Stripe / Apple Subscription 等业界标准做法。

### 用户的核心产品规则（已锁定）

1. **完全废弃 legacy_tier 制**（不在本次重构作用域，但本次完成后即可下线）
2. **三种积分类型**：
   - Trial 体验包：¥9.9，200 积分，3 天有效期，**lifetime 单次**
   - Pro 月订阅：¥99/月，2000 积分/月，**严格按月不可借支**
   - Booster 加量包：¥29.9/份，600 积分/份，**永不过期**，但**仅会员状态可用**
3. **Trial + Pro 叠加**：trial 期内可同时购买 pro，pro 立即激活；扣减优先级 trial → pro → booster
4. **显示状态仅 3 种**（用户端）：free / trial / pro。trial 在期一律显示 trial（即使 pro 也在期）
5. **续费语义**：
   - 在期 pro 续费 → `expires_at += N 个月`（不新建行）
   - 已过期再开 → 重置为 `expires_at = now + N 个月`，`current_started_at = now`
6. **Pro 月度刷新锚点**：以 `current_started_at` 为锚点；过期重开后锚点重置
7. **自然月计算**：anchor-restore 算法（1/31 → 2/28 → 3/31 → 4/30），应用层算好后传 SQL
8. **Booster 简化**：每用户 1 行 balance，购买只 += credits，多份无差异
9. **B2B 账单**：依赖 `membership_event` append-only 事件日志
10. **不实现退款**、**不实现到期提醒**、**保留 legacy 字段并行只读**

### 关键设计决策（已与用户确认）

- 用 `current_started_at` 做月度对齐锚点（不用 `first_started_at`），因为重新开通就重新计时更符合直觉
- anchor-restore 在应用层算（Go），SQL 只接收完整时间戳；MySQL `DATE_ADD` 行为不可信
- 父账户客户管理页显示双状态（"试用中 + Pro 已开通"），子账户端坚持 3 状态简洁

## 业务目标

1. **消除空档**：会员到期/续费/月度刷新瞬间无任何"卡顿"窗口
2. **去 cron 化**：会员体系不再依赖定时任务推进状态，避免 cron 故障导致计费异常
3. **简化数据模型**：5 张表清晰分离订阅期、月度配额、加量包、事件日志，每张表单一职责
4. **审计可追溯**：B2B 账单、用户生命周期分析全走事件日志，subscription 单行覆盖更新不丢历史
5. **支撑 B2B2C 计费场景**：父账户帮开 / 续费 / 加量包，月末按事件聚合自动出账单

## 优先级

**高**——计费核心重构，影响所有付费用户和 B2B 客户。

## Triage

- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：**是**（5 张新表 + 迁移脚本）
  2. 新增 API 端点：**是**（grant / deduct / balance / B2B billing 都要重写）
  3. 新外部服务集成：否
  4. 影响文件数：**>3**（跨 numind-server / numind-web-v3 / numind-admin-web 三仓）
  5. 高风险业务逻辑（支付/权限）：**是**（计费核心 + B2B 账单）
- 人类决定：**确认 Standard Track**（2026-04-29，用户原话"马上进入 NDF standard track"）

## 涉及仓库

- `numind-server`：5 张新表 schema + biz 重写（subscription / cycle / trial / booster / event）+ 迁移脚本 + payment.go 改写 + 移除 cron + 删除 legacy 写入路径
- `numind-web-v3`：余额接口适配 + booster 冻结提示 UI + 客户管理页双状态显示
- `numind-admin-web`：B2B 月度账单页适配新口径

## 关键约束（设计阶段已锁定，S2 之前不可变更）

| 决策 | 锁定值 |
|---|---|
| 时间单位 | 自然月（应用层 anchor-restore） |
| Subscription 行数 | 每用户最多 1 行，覆盖更新 |
| Cycle 创建时机 | 懒创建（首次扣减或首次余额查询触发） |
| Cycle 锚点 | `subscription.current_started_at` |
| Booster 数据模型 | 每用户 1 行 balance |
| 扣减优先级 | trial → cycle → booster（硬规则） |
| 显示状态（用户端） | free / trial / pro（3 种） |
| 显示状态（父账户管理页） | 增加"试用中 + Pro 已开通"叠加显示 |
| 退款 | 不实现（事件日志预留 sub_revoked 字段，后续扩展） |
| 到期提醒 | 不实现 |
| Legacy 字段 | 保留，单向只读，不双写 |
| LLM 调用与事务 | 复用现有 Reserve/Reconcile 双阶段，LLM 调用绝不在事务内 |
| 锁顺序 | user_id ASC → 表名字典序（防死锁） |
| 时间精度 | DATETIME(0)，服务器统一 UTC+8 |
| 幂等键 | membership_event 加 idempotency_key 唯一索引 |

## 已识别的子问题（S2 spec 必须覆盖）

参考 review 阶段两个 subagent 提出的 P0/P1 问题，全部需在 spec 中解决：

1. 自然月 anchor-restore 算法（应用层实现）
2. Cycle 懒创建并发竞态（ON CONFLICT + 重新 SELECT FOR UPDATE）
3. 长事务 timestamp 一致性（事务起点固定 ts）
4. Subscription 续费 lost update（SQL 表达式或 SELECT FOR UPDATE）
5. 迁移脚本段合并（防止跨非连续段错误聚合）
6. 锁顺序与 LLM-out-of-tx 约定
7. cycle_end vs sub.expires_at 边界半开区间统一
8. sub 过期 = 所有派生权益失效（cycle 余额作废 / booster 冻结）
9. 父账户 grant booster 也要校验子账户会员状态
10. membership_event idempotency_key 防重入
11. B2B 账单复合索引 + 月度预聚合表
12. 时区 UTC+8 统一 + DATETIME(0) 精度
13. 规避 GORM `default:true` bool 陷阱（用 enum status）
14. legacy 字段单向只读，不双写
15. payment.go 现有 tier rank 判断改写为 HasActiveSubscription 调用

## 备注

本次重构基于多轮 ultrathink 讨论（2026-04-29），含两次并行 subagent 评审。所有 P0/P1 问题已纳入 S2 spec 必须覆盖项。设计文档完整脉络见本次 session 对话记录，S1 提案应基于本卡片展开技术可行性论证、工作量估算、迁移与灰度策略。
