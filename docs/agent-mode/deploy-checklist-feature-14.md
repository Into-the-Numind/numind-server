# Deploy Checklist · Feature #14 (Agent Mode v1.0-final)

> ⚠️ 本文档适用于 prod 部署。dev/qa 部署只跑 §3 migration 顺序（自动化由 /deploy-dev 处理）。
> 🚫 自动化禁止条件：本文档**仅由人类操作员**执行；autopilot AI 永不运行 prod 部署命令。

---

## §1 Pre-deploy 准备

- [ ] 数据库备份：mysqldump prod 全库
- [ ] 通知运维：prod 部署窗口 + 失败回滚通知人
- [ ] 验证 dev/qa 已运行本 feature 满 24h 无 P0 issue
- [ ] `git pull origin develop`（确认本 feature 已 merged）
- [ ] `git tag v2.2.0`（候选版本号）；admin tag `admin-v2.2.0`

---

## §2 Migration 顺序（#14 agent-mode 新增 20 SQL）

按 timestamp + 字母序执行。仅跑 **UP** 文件（无 `_rollback` 后缀）。

| # | SQL 文件 | Feature | 类型 | 验证 SQL |
|---|---------|---------|------|---------|
| 1 | `20260520_120000_create_agent_run_table.sql` | #2 runtime skeleton | CREATE | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_run';` (expect 1) |
| 2 | `20260521_010000_alter_agent_run_add_compact_columns.sql` | #9 compact | ALTER | `SHOW COLUMNS FROM agent_run LIKE 'compact_state';` (expect 1 row) |
| 3 | `20260521_120000_agent_mode_compliance_3layer.sql` | #13 compliance | CREATE | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='compliance_rule';` (expect 1) |
| 4 | `20260521_120000_agent_permission_pipeline.sql` | #6 permissions | CREATE | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_permission_config';` (expect 1) |
| 5 | `20260521_120000_create_agent_session_memory.sql` | #7 memory | CREATE | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_session_memory';` (expect 1) |
| 6 | `20260521_120000_create_tool_definition_and_factory_registry.sql` | #3 tool registry | CREATE | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='tool_definition';` (expect 1) |
| 7 | `20260521_120100_create_user_global_memory.sql` | #7 memory | CREATE | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='user_global_memory';` (expect 1) |
| 8 | `20260521_140000_agent_billing_source_type_admin_test.sql` | #12 billing | ALTER | `SELECT COLUMN_TYPE FROM information_schema.COLUMNS WHERE table_name='credit_transaction' AND column_name='source_type';` (expect enum includes 'admin_test') |
| 9 | `20260521_140100_agent_run_terminal_metadata.sql` | #12 billing | ALTER | `SHOW COLUMNS FROM agent_run LIKE 'terminal_metadata';` (expect 1 row) |
| 10 | `20260521_140200_create_credit_admin_test_grant.sql` | #12 billing | CREATE | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='credit_admin_test_grant';` (expect 1) |
| 11 | `20260521_180000_agent_task_profiles_seed.sql` | #14 e2e rollout | SEED | `SELECT COUNT(*) FROM task_profile WHERE task_id LIKE 'agent.%';` (expect 7) |
| 12 | `20260521_190000_seed_e2e_test_agent.sql` | #14 e2e | **DEV ONLY — SKIP IN PROD** | — |
| 13 | `20260521_190100_seed_e2e_compliance_rule.sql` | #14 e2e | **DEV ONLY — SKIP IN PROD** | — |
| 14 | `20260521_200000_agent_run_admin_cancel.sql` | #14 admin cancel | ALTER | `SHOW COLUMNS FROM agent_run LIKE 'cancellation_requested_at';` (expect 1 row); `SHOW COLUMNS FROM agent_run LIKE 'agent_definition_id';` (expect 1 row) |
| 15 | `20260522_120000_create_agent_sandbox_session.sql` | #4 sandbox | CREATE | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_sandbox_session';` (expect 1) |
| 16 | `20260522_220000_create_agent_definition.sql` | #5 skill system | CREATE | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_definition';` (expect 1) |
| 17 | `20260522_220100_create_agent_definition_history.sql` | #5 skill system | CREATE | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_definition_history';` (expect 1) |
| 18 | `20260522_220200_create_skill_template.sql` | #5 skill system | CREATE | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='skill_template';` (expect 1) |
| 19 | `20260522_220300_seed_skill_template.sql` | #5 skill system | SEED | `SELECT COUNT(*) FROM skill_template;` (expect 10) |

> **同 timestamp 多文件（20260521_120000_\*）**：先跑 `agent_mode_compliance_3layer`，再 `agent_permission_pipeline`，再 `create_agent_session_memory`，再 `create_tool_definition_and_factory_registry`（字母序）。

**SSH 命令模板：**

```bash
# 将 SQL 文件 scp 到部署机再执行
sshpass -p "$PROD_SSH_PASS" scp migrations/<filename>.sql "$PROD_SSH_USER@$PROD_SSH_HOST:/tmp/"
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "mysql -u <db_user> -p<db_pass> <db_name> < /tmp/<filename>.sql"
```

---

## §3 Deploy 执行

按序执行（先后端，后前端）：

- [ ] `/deploy-prod server`（prod 9095，需 `v2.2.0` tag）
- [ ] `/deploy-prod admin`（prod 9099，需 `admin-v2.2.0` tag）
- [ ] `/deploy-prod`（numind-admin-web，需对应 tag）
- [ ] `/deploy-prod`（numind-web-v3，需对应 tag）

部署后：

- [ ] 监控 `/healthz`：4 服务全 200

---

## §4 Post-deploy 验证

- [ ] 父账户在 admin-web 创建 1 个 test agent，学员 demo 账户对话跑通
- [ ] `credit_admin_test_grant` 表有新增行（试聊配额）
- [ ] Langfuse prod trace 后台可见完整 trace 树
- [ ] `compliance_audit_log` 写入正常（触发一条合规判断）
- [ ] `credit_transaction.source_type` CHECK constraint 未变更（`chk_ct_source_type`）
- [ ] 监控 30min 看错误率 / 积分消耗速率

---

## §5 Rollback 步骤（如出问题）

按 migration 倒序执行对应 `_rollback.sql` 文件（19 → 18 → ... → 1）：

```bash
# 示例：rollback migration #19
sshpass -p "$PROD_SSH_PASS" scp migrations/20260522_220300_seed_skill_template_rollback.sql "$PROD_SSH_USER@$PROD_SSH_HOST:/tmp/"
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "mysql -u <db_user> -p<db_pass> <db_name> < /tmp/20260522_220300_seed_skill_template_rollback.sql"
```

- [ ] 按 19 → 1 倒序跑完所有 rollback SQL
- [ ] `git checkout v2.1.x`（上一个稳定 tag）→ `/deploy-prod server` + `/deploy-prod admin`
- [ ] 前端同步回滚到上一 tag
- [ ] `/healthz` × 4 验证

---

## §6 注意事项

- `credit_transaction.source_type` CHECK constraint 由 #12 锁定，#14 不动（新增 `admin_test` 值在 migration #8）
- `agent_definition_id` 列在 `agent_run` 是 NULL 兼容，旧 row 不被影响
- `agent_task_profiles` 新增 7 条 seed，旧 task_profile 行不变（ON DUPLICATE KEY UPDATE 幂等）
- **dev-only migrations**（#12 `seed_e2e_test_agent` + #13 `seed_e2e_compliance_rule`）在 prod **必须跳过**；它们的 id `99999` 与真实数据冲突
- `20260521_120000_*` 4 个同时间戳文件以字母序执行；它们之间无 FK 依赖，顺序不影响正确性
- `agent_run_admin_cancel` 使用 `ADD COLUMN IF NOT EXISTS`（MySQL 8.0.29+）；低版本请用 procedure 包装（文件内有注释）
