# S3 plan reviewer 第 1 轮发现 - 全部已修

**Date:** 2026-05-18

**Feature:** `sop-salesrag-parent-scope`

**Migrated from:** `build-manifest.yaml` decisions[]

---

2 P0 + 6 P1 + 2 P2 — (P0) wrong module path github.com/aiagent-numind/...,实际是 numind-server; (P0) biz.B 全局变量不存在,middleware redirect 没目标; (P0) 漏掉 caller monitor.go:146 + customer_permission_lifecycle_test.go 11 处; (P0) sop_test.go 不存在,改用 sop_template_visibility_test.go; (P0) CreateTemplateByUserReq 无 Prompt 字段; (P0) Task 4 不可丢 TrailingChatEnabled 处理 + GORM default:true fixup; (P0) Task 5 不可丢 GrantTemplateToConfiguredSubUsers 自动授权; (P1) IStore not DataStore; (P1) test helper 名字; (P1) ListVisibleTemplates 2-axis 签名变更(ctx+ownerID); (P1) 用 middleware.GetCurrentUser helper. Task 3 加 Step 0 wire biz.B 单例 + 含 monitor.go + 测试两个文件全部迁移. 共 6 atomic task.
