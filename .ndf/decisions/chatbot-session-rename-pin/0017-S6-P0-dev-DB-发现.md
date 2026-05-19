# S6 P0 dev DB 发现

**Date:** 2026-05-15

**Feature:** `chatbot-session-rename-pin`

**Migrated from:** `build-manifest.yaml` decisions[]

---

SSH apply migration 后实测 D2 不变量在真实 MySQL 上 FAIL — chatbot_session.updated_at DDL 含 'ON UPDATE CURRENT_TIMESTAMP' (MySQL 服务端触发器). GORM UpdateColumn 只跳过 GORM-level 自动设置, 绕不开 MySQL 服务端的 ON UPDATE — 实测 UPDATE title 后 updated_at 从 2026-04-09 20:52:57 自动刷新到 NOW(). S5 SQLite in-memory unit tests 全 PASS 因 SQLite 没此语法, 这是 S2 holistic reviewer 'broader risks: UpdateColumn 真实 MySQL 行为' 风险点的实际命中. Fix commit c0c61bf: store.UpdateTitle/SetPinnedAt 从 UpdateColumn 改为 Updates(map) 含 'updated_at = gorm.Expr("updated_at")' 显式 SET 同列同值, 触发 MySQL '已显式 SET' 规则跳过 ON UPDATE 触发器. dev DB raw SQL 等价模式实测验证有效.
