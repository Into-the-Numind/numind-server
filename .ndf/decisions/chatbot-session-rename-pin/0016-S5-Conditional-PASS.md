# S5 Conditional PASS

**Date:** 2026-05-14

**Feature:** `chatbot-session-rename-pin`

**Migrated from:** `build-manifest.yaml` decisions[]

---

自动化测试套件全 PASS — 后端 task test (race+coverage) 0 FAIL, biz/chatbot 34.6% coverage; store 10 + biz 8 = 18 unit tests 全过 (含 D2 不变量 / EC-14 重复置顶 / SQL 排序 3-case); 前端 lint 0 errors + type-check 0 + Vitest 380/403 PASS。E2E + 手动 curl 401/403 deferred 到 S6 — 理由: config_local.yaml 实际指向 dev DB (49.233.219.254:13306), 但 dev DB 未 apply migration; visibility-scope 已 S7 上线用过同 pattern (memory project_dev_deploy_migration_gap: CI 镜像无 migrations 目录, 必须 S6 手动 SSH apply). QA 报告 numind-server/docs/superpowers/specs/2026-05-13-chatbot-session-rename-pin-qa-report.md 含 §3 deferred 理由 + S6 阻塞前置清单 + §6 风险评估 (MySQL 8 SQL 排序 / UpdateColumn 真实 MySQL 行为 / UI 渲染 / 跨 feature 冲突均 Low-Med)。S6 必跑: SSH apply migration → merge develop → CI deploy → Playwright E2E 8 条路径 + 手动 curl 2 条 + 人工 QA 截图。
