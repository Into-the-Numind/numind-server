# S2 spec written

**Date:** 2026-05-13

**Feature:** `chatbot-session-rename-pin`

**Migrated from:** `build-manifest.yaml` decisions[]

---

写出 ~1200 行 spec 含 10 节：§0 背景边界 + §1 数据模型 (migration forward+rollback DDL + GORM model 改动) + §2 后端 API 契约 (rename + pin + ListSessions 改造) + §3 store/biz/controller 实现要点 + §4 排序 SQL 兼容性验证 (MySQL 8) + §5 前端设计 (api/store/Vue 改造 + inline RenameModal) + §6 17 条边界 case 全清单 + §7 测试策略 + §8 范围外列表 + §9 S3 待答 + §10 现有 feature 关联
