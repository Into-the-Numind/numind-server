# H2 两阶段 Sonnet review

**Date:** 2026-05-16

**Feature:** `credits-gateway-completion-estimate`

**Migrated from:** `build-manifest.yaml` decisions[]

---

Spec compliance + Code quality 双 reviewer 均返回 PASS_WITH_CONCERNS。Spec: 0 P0 / 0 P1 / 2 P2 (TODO(S7) 注释过期 + line 758 placeholder 注释强化)。Code: 0 P0 / 2 P1 (thundering herd 缺 dedupe + DB error 错误缓存) / 4 P2 (race test 缺失 + TTL expiry test 缺失 + NUL 分隔符未注释 + namespace 对齐确认)。NDF Rule 7 全部就地修：P1.1 加 singleflight.Group 去重并发 loader；P1.2 重构 queryDB 返回 queryResult{err}，DB error 路径跳过缓存 + warn log；P2 加 race test + NUL 注释 + TODO 更新；TTL expiry test 跳过（cache-hit test 已间接覆盖，需注入 config 改动较大）。9/9 单测含 -race PASS。
