# Agent Mode v1.0-final Go-Live Checklist

> 由人类操作员执行；autopilot AI **永不**触发此流程。
> 详细 migration 步骤见 [deploy-checklist-feature-14.md](./deploy-checklist-feature-14.md)。
> 线上问题处理见 [runbook.md](./runbook.md)。

---

## Phase 0: 决策确认

- [ ] 业务方批准上线时间窗（建议非高峰时段，如工作日 22:00–00:00）
- [ ] 运维 oncall 待命（联系方式确认）
- [ ] 客服通知话术准备完毕（"AI 助手功能即将上线，如遇问题请联系…"）
- [ ] 确认 dev 环境 + qa 环境已稳定运行 24h 以上，无 P0 issue

---

## Phase 1: 代码就绪

```bash
git checkout develop && git pull origin develop
```

- [ ] `git log --oneline develop | head -30` 确认 14 features 全部 merged
  - 关键 commit 关键字：`agent-run-table`、`tool-registry`、`skill-system`、`memory-system`、`permission-pipeline`、`compliance-3layer`、`billing-integration`、`compact`、`sandbox`、`e2e-rollout`
- [ ] `go test -race ./...` PASS（允许 pre-existing skip 测试）
- [ ] `task lint` exit 0（允许 3 个 pre-existing 历史债，需确认数量未增加）
- [ ] 三个仓库（numind-server / numind-web-v3 / numind-admin-web）develop 分支均已合并

---

## Phase 2: 打 tag

```bash
# 主服务 + 用户前端
cd numind-server
git checkout develop && git pull
git tag v2.2.0
git push origin v2.2.0

# admin API 单独 tag（如果 admin 服务独立部署）
git tag admin-v2.2.0
git push origin admin-v2.2.0

# 用户前端
cd ../numind-web-v3
git tag v2.2.0
git push origin v2.2.0

# 管理前端
cd ../numind-admin-web
git tag v2.2.0
git push origin v2.2.0
```

- [ ] 4 个 tag 均已推送到 remote
- [ ] `git tag --list | grep v2.2.0` 确认

---

## Phase 3: prod migration

详细文件顺序见 [deploy-checklist-feature-14.md §2](./deploy-checklist-feature-14.md)。

- [ ] prod 数据库全库备份完成（mysqldump，保存到安全位置）
- [ ] 备份文件可访问性验证（解压检查 1 张表）

按表格顺序逐一执行（**共 17 个 SQL，跳过 dev-only #12 + #13**）：

- [ ] #1 `20260520_120000_create_agent_run_table.sql` + 验证
- [ ] #2 `20260521_010000_alter_agent_run_add_compact_columns.sql` + 验证
- [ ] #3 `20260521_120000_agent_mode_compliance_3layer.sql` + 验证
- [ ] #4 `20260521_120000_agent_permission_pipeline.sql` + 验证
- [ ] #5 `20260521_120000_create_agent_session_memory.sql` + 验证
- [ ] #6 `20260521_120000_create_tool_definition_and_factory_registry.sql` + 验证
- [ ] #7 `20260521_120100_create_user_global_memory.sql` + 验证
- [ ] #8 `20260521_140000_agent_billing_source_type_admin_test.sql` + 验证
- [ ] #9 `20260521_140100_agent_run_terminal_metadata.sql` + 验证
- [ ] #10 `20260521_140200_create_credit_admin_test_grant.sql` + 验证
- [ ] #11 `20260521_180000_agent_task_profiles_seed.sql` + 验证（expect 7 rows）
- [ ] ~~#12 `seed_e2e_test_agent.sql`~~ — **SKIP（dev only）**
- [ ] ~~#13 `seed_e2e_compliance_rule.sql`~~ — **SKIP（dev only）**
- [ ] #14 `20260521_200000_agent_run_admin_cancel.sql` + 验证
- [ ] #15 `20260522_120000_create_agent_sandbox_session.sql` + 验证
- [ ] #16 `20260522_220000_create_agent_definition.sql` + 验证
- [ ] #17 `20260522_220100_create_agent_definition_history.sql` + 验证
- [ ] #18 `20260522_220200_create_skill_template.sql` + 验证
- [ ] #19 `20260522_220300_seed_skill_template.sql` + 验证（expect 10 rows）

---

## Phase 4: deploy

按序执行：

- [ ] `/deploy-prod server`（需 `v2.2.0` tag，prod 9095）
- [ ] `/deploy-prod admin`（需 `admin-v2.2.0` tag，prod 9099）
- [ ] `cd numind-admin-web && /deploy-prod`（需 `v2.2.0` tag）
- [ ] `cd numind-web-v3 && /deploy-prod`（需 `v2.2.0` tag）

---

## Phase 5: smoke test

- [ ] `curl https://api.youshu.asia/healthz` → 200
- [ ] `curl https://admin-api.youshu.asia/healthz` → 200
- [ ] admin-web 前端可正常访问（`https://admin.youshu.asia`）
- [ ] 用户前端可正常访问（`https://youshu.asia`）

**功能验证（手动）：**

- [ ] 用父账户（`parent_user_id = NULL`）登录 admin-web → **AI 助手** 菜单可见
- [ ] 创建 1 个 test agent（配置：名称="上线验证 agent"，credit_cap_per_session=100）
- [ ] 在 agent 管理中激活 test agent
- [ ] 用学员 demo 账户（子账户）登录用户端，能看到 test agent 入口
- [ ] 学员账户对话一次，能收到回复，无报错
- [ ] admin-web → Agent 监控 → 看到对应 `agent_run` 行，state = `completed`
- [ ] Langfuse prod 后台看到完整 trace 树（`agent.run` + `agent.permission_check` 等 span）

**监控窗口：**

- [ ] 上线后监控 30 分钟（错误率、积分消耗速率、P99 延迟）
- [ ] 无 P0/P1 告警

---

## Phase 6: post-launch

**24h 后：**

- [ ] 拉 24h 监控数据：agent_run 数量 / 平均 session 时长 / 积分消耗速率
- [ ] 检查 `compliance_audit_log`：是否有大量误拦截（action=forbid 但学员反馈正常问题）
- [ ] AuditLogger drop 统计：`journalctl | grep "audit drop"` 是否有 drop
- [ ] 整理 24h 内 P0/P1 问题清单

**1 周后：**

- [ ] 学员留存率（使用 agent 的学员次日 / 7 日回访）
- [ ] 机构管理员满意度（运营同学访谈）
- [ ] 技术债清单更新（sandbox iptables / L1 memory cron / audit log persistence）
- [ ] 启动 retro session（`/retro`）

---

## 紧急回滚入口

如上线后出现 P0 问题，立即：

1. 参考 [runbook.md §1](./runbook.md) 取消失控 agent
2. 如需全量回滚，参考 [deploy-checklist-feature-14.md §5](./deploy-checklist-feature-14.md) rollback 流程
3. 通知业务方 + 运维，启动事故复盘
