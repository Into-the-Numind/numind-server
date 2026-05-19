# S2 spec review

**Date:** 2026-04-30

**Feature:** `membership-credits-redesign`

**Migrated from:** `build-manifest.yaml` decisions[]

---

派 4 个并行 review subagent 分章节做严苛 review，发现 53 个问题（18 P0 + 21 P1 + 14 P2）。最严重问题：cycle_index vs cycle_start schema 不一致（SQL 直接报错）/ §3.5 锁顺序与 §4.1 字典序自相矛盾 / BalanceDTO 与 §5.3 后端响应字段全部不匹配 / membership_event months_or_quantity 字段名前后不一致 / §6 Invariant 1 必然失败 / §6.2.2 TIMESTAMPDIFF MONTH 截断
