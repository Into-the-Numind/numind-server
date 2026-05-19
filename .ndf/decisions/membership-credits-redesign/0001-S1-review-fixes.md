# S1 review fixes

**Date:** 2026-04-29

**Feature:** `membership-credits-redesign`

**Migrated from:** `build-manifest.yaml` decisions[]

---

第三个 subagent review 提出 5 P0 + 8 P1 + 2 P2，全部接受并修复——AC-21 拆为 a/b/c 含字段映射/去重/单位规则；AC-2/3/4 锁定 anchor=current_started_at 且 anchor_add_months 函数签名固化；新增完整错误码清单 11 条；EC-7 锁定 cycle_end 半开区间精确语义；EC-3/4/4b 校验顺序固化（先查 trial_grant 再查 sub）；§3 R6 改为'一次性切换数据漂移'风险（双写已废）；AC-16 拆 a/b 区分 idempotency 重放；新增 AC-13b 反作弊 out-of-scope 声明 + AC-13c booster 冻结期前后端兜底；§6 回滚分段决策（24h/7d/7d+ 三段 SOP）；§2 估时上调至 20-22 工作日
