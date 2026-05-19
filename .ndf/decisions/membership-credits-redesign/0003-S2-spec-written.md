# S2 spec written

**Date:** 2026-04-29

**Feature:** `membership-credits-redesign`

**Migrated from:** `build-manifest.yaml` decisions[]

---

拆 6 个并行 subagent 写出 6127 行 spec（10 章节）——§1 概览 + §2 数据模型（5 表 DDL）+ §3 核心算法（7 函数 Go 伪代码）+ §4 并发与事务（锁顺序 + idempotency + Reserve/Reconcile）+ §5 API 契约（6 端点 + 错误码 11 条）+ §6 迁移策略（4 件套脚本）+ §7 切换日双口径拼接 + §8 前端契约 + §9 验证策略 + §10 部署回滚。中途 §3+§4 大 agent 卡死 + 后续 §1+§2+§6 / §8+§9+§10 也卡，全部停掉拆为单章节 agent 重启完成。Self-review 修正了 §1.3 映射表的章节编号错位（6 处 §11/§7/§8 等错位标签全部改对）
