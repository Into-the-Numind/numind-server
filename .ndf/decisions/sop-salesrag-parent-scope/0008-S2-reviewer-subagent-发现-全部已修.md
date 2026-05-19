# S2 reviewer subagent 发现 — 全部已修

**Date:** 2026-05-18

**Feature:** `sop-salesrag-parent-scope`

**Migrated from:** `build-manifest.yaml` decisions[]

---

adversarial reviewer 找出 2 P0 + 6 P1 + 2 P2 问题, 已全部吸收进 spec. P0: (1) admin CreateTemplate 路径继续制造 NULL creator_user_id (D6); (2) biz 层无 defense-in-depth assertion (D8). P1: 修 proposal §3 风险表与 D1 冲突;layer violation 顺手修;FK CASCADE + INT UNSIGNED;model 注释更新;回归测试 content_monitor/self_service_config 覆盖;dead code at sop.go:409 保留(实际是 correctness guard 不是 dead). P2: 删 updated_at;snapshot fixture S5 处理.
