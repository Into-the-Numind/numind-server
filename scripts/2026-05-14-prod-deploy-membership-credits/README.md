# Prod 上线手册 — membership-credits-redesign

> **目标**：把 2 周前停滞的会员积分体系重构推上 prod (`https://youshu.asia`)
> **创建时间**：2026-05-14
> **预计执行窗口**：30–60 分钟（含 CI 部署等待）
> **建议时段**：低峰时段（凌晨 / 工作日早晨）
> **风险等级**：**高**（动 prod DB 计费数据），但已通过 dev 完整端到端验证

---

## 0. 阅读须知

本手册按"先准备 → 再执行 → 后验证 → 兜底回滚"4 段组织。每段下面分若干步骤：

- **🤖 AI 执行**：可以让 Claude 自动执行（你说一声"开始 X 步"即可）
- **👤 你执行/确认**：需要你亲自确认或操作的步骤
- **⚠️ 必须**：跳过会出问题的关键步骤

> **执行哲学**：每一段做完，AI 报状态给你确认 OK 后再进下一段。任何一步异常就**先暂停**，不要硬推进。

---

## 1. 前置依赖（必须满足，否则不开工）

### 1.1 dev 上验证全过 ✅（已完成）

- [x] dev DB schema + data migration end-to-end 跑通（subscription 2 / trial_grant 2 / user_booster_balance 1 / membership_event 9，I1–I8 invariant 全 0）
- [x] Playwright E2E 11/11 pass
- [x] Langfuse trace 真实 LLM 调用回归通过（trace_id 入表 + 三桶扣减优先级正确）
- [x] dev 上"立即购买加量包"端到端可用（含 QR 扫码）
- [x] 三个仓库 develop 推送完毕：`numind-server@07256e1+0574b98`、`numind-admin-web@f4dc251`、`numind-web-v3@6fa6385`

### 1.2 prod 状态摸底 ✅（已完成）

- [x] **wechat 证书在 prod** (`/opt/numind/config/cert/apiclient_key.pem` + `pub_key.pem`)
- [x] **prod DB 数据干净**（4-30 审计：0 booster 包 + 0 多 trial 用户，91 条 credit_package / 68 用户）
- [x] **prod DB schema 缺**：5 张新表 + `payment_order.quantity` 列都还不存在（**必须本次部署补齐**）
- [x] **prod config 缺**：`billing.b2b_cutover_date` + `langfuse:` 整段（**必须本次部署补齐**）

---

## 2. 准备阶段（部署前一天即可做）

### 2.1 🤖 AI 执行：备份 prod DB 关键三表

> 这是兜底保险。本次只迁移 credit_package、user、payment_order 三表，备份这些即可。

```bash
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker exec numind-mysql-prod mysqldump -uroot -pNumind2025 \
     numind-prod credit_package user payment_order \
     > /root/backup-prod-membership-credits-$(date +%Y%m%d_%H%M%S).sql && \
   ls -la /root/backup-prod-membership-credits-*.sql | tail -1"
```

**预期输出**：一个 `*.sql` 文件（数 MB 大小），日期戳是今天。

### 2.2 👤 你执行：审核 config 增量

打开 `scripts/2026-05-14-prod-deploy-membership-credits/config_prod_increments.yaml`，
确认：

- [ ] `billing.b2b_cutover_date` 值是合理的日期（推荐用上线当天）
- [ ] `langfuse.public_key` / `secret_key` 是否要换 prod 专属 keys（不换也能跑，会和 dev 共用一个 Langfuse 项目，trace 混在一起）

### 2.3 🤖 AI 执行：上传 config 增量到 prod 并合并

```bash
# 1. 先备份当前 config
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "cp /opt/numind/config/config_prod.yaml \
      /opt/numind/config/config_prod.yaml.bak-$(date +%Y%m%d_%H%M%S)"

# 2. 上传 increments
sshpass -p "$PROD_SSH_PASS" scp config_prod_increments.yaml \
  "$PROD_SSH_USER@$PROD_SSH_HOST:/tmp/config_prod_increments.yaml"

# 3. 追加到 prod config 末尾
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "cat /tmp/config_prod_increments.yaml >> /opt/numind/config/config_prod.yaml"

# 4. 验证 yaml 仍然合法
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "python3 -c \"import yaml; yaml.safe_load(open('/opt/numind/config/config_prod.yaml'))\" && echo OK"
```

**预期输出**：最后一行 `OK`。

### 2.4 🤖 AI 执行：把 4 件套迁移 SQL 上传 prod

```bash
# 上传到 prod 主机
sshpass -p "$PROD_SSH_PASS" scp \
  scripts/2026-04-30-membership-credits-redesign-migration/*.sql \
  "$PROD_SSH_USER@$PROD_SSH_HOST:/tmp/"

# 还需上传建表的 schema migration
sshpass -p "$PROD_SSH_PASS" scp \
  migrations/20260430_membership_credits_redesign.sql \
  "$PROD_SSH_USER@$PROD_SSH_HOST:/tmp/"

# 拷进 mysql container
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "for f in /tmp/01-dry-run.sql /tmp/02-apply.sql /tmp/03-verify.sql /tmp/04-rollback.sql /tmp/20260430_membership_credits_redesign.sql; do
     docker cp \$f numind-mysql-prod:/tmp/
   done && docker exec numind-mysql-prod ls -la /tmp/*.sql"
```

**预期输出**：5 个 SQL 文件都在 container 里。

---

## 3. 执行阶段（窗口期内连续做）

> 从这里开始建议**集中精力，不要被打断**。

### 3.1 🤖 AI 执行：在 prod 建 5 张新表（schema 迁移）

```bash
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker exec numind-mysql-prod sh -c \
     'mysql -uroot -pNumind2025 numind-prod < /tmp/20260430_membership_credits_redesign.sql'"

# 验证 5 张表存在
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker exec numind-mysql-prod mysql -uroot -pNumind2025 numind-prod -N -e \"
     SELECT 'subscription' AS tbl, COUNT(*) FROM information_schema.tables WHERE table_schema='numind-prod' AND table_name='subscription' UNION ALL
     SELECT 'trial_grant', COUNT(*) FROM information_schema.tables WHERE table_schema='numind-prod' AND table_name='trial_grant' UNION ALL
     SELECT 'credit_cycle', COUNT(*) FROM information_schema.tables WHERE table_schema='numind-prod' AND table_name='credit_cycle' UNION ALL
     SELECT 'user_booster_balance', COUNT(*) FROM information_schema.tables WHERE table_schema='numind-prod' AND table_name='user_booster_balance' UNION ALL
     SELECT 'membership_event', COUNT(*) FROM information_schema.tables WHERE table_schema='numind-prod' AND table_name='membership_event';
   \""
```

**预期输出**：5 行，每行 `<tbl_name>\t1`（每张表存在性=1）。

### 3.2 🤖 AI 执行：dry-run 演练

```bash
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker exec numind-mysql-prod sh -c \
     'mysql -uroot -pNumind2025 numind-prod < /tmp/01-dry-run.sql'" | tail -40
```

**预期输出（关键检查）**：
- F 段所有 `BLOCKER_F*_violation_count = 0`（与 4-30 prod 审计一致）
- 末尾出现 `=== DRY RUN COMPLETE — Review output before running 02-apply.sql ===`

⚠️ **暂停点**：把输出贴回来，我和你一起核对所有 BLOCKER 都是 0。如果有非零，**立刻停止**，按 spec §6 排查脏数据。

### 3.3 🤖 AI 执行：apply 真实迁移（不可逆，回滚靠 04）

```bash
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker exec numind-mysql-prod sh -c \
     'mysql -uroot -pNumind2025 numind-prod < /tmp/02-apply.sql'" | tail -20
```

**预期输出**：
- `BACKUP_OK` 行（backup 表已建）
- 5 行 `step1..step5` apply_log，每行有 `rows_inserted` 数字
- 最后 `APPLY COMPLETE — run 03-verify.sql to audit post-migration state`

⚠️ **暂停点**：贴回来，确认 5 步全跑完了。

### 3.4 🤖 AI 执行：verify invariants

```bash
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker exec numind-mysql-prod sh -c \
     'mysql -uroot -pNumind2025 numind-prod < /tmp/03-verify.sql'" | tail -25
```

**预期输出**：
- `I1..I8 violation_count` 全部为 `0`
- 新表 row_count 和 apply_log 数字一致
- 末尾 `VERIFY COMPLETE — resolve any violation_count > 0 before releasing traffic`

⚠️ **暂停点**：贴回来确认 8 个 invariant 全 0。**任何 violation > 0 立即 rollback**（跳 § 5）。

### 3.5 👤 你执行：在 GitHub 上打 4 个 tag

> 同时打 4 个 tag 触发 CI 并行 build 镜像。3 个仓库（server / web-v3 / admin-web）+ 一个 admin tag（在 server 仓库）。

按下面命令在 4 个仓库的 develop 分支上分别打 tag 并 push。**注意**：每个 tag 都在仓库本地 develop 上打。

```bash
# numind-server (含 admin)
cd /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-server
git checkout develop && git pull --ff-only
git tag v2.1.13 -m "membership-credits-redesign + admin-router :user_id fix"
git tag admin-v1.4.2 -m "membership-credits-redesign admin API (GET /v1/admin/users/:id/balance)"
git push origin v2.1.13 admin-v1.4.2

# numind-web-v3
cd /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-web-v3
git checkout develop && git pull --ff-only
git tag v1.0.16 -m "BoosterPurchaseDialog QR + idempotency HTTP fallback"
git push origin v1.0.16

# numind-admin-web
cd /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-admin-web
git checkout develop && git pull --ff-only
git tag v1.4.1 -m "B2B monthly billing report view + CSV export"
git push origin v1.4.1
```

🤖 **AI 监控**：我会跑 monitor 盯 4 个 CI，每个完成发一条通知。Server CI ~4 分钟，admin/admin-web/web-v3 ~2 分钟，全部完成大概 5–7 分钟。

### 3.6 🤖 AI 执行：验证 prod 容器镜像更新到新 tag

```bash
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker ps --filter name=numind --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}'"
```

**预期输出**：
- `numind-server-prod` → `pmtmyaggy/numind-server:v2.1.13`，Up < 5 min healthy
- `numind-admin-server-prod` → `pmtmyaggy/numind-admin:admin-v1.4.2`，Up healthy
- `numind-web-v3` → `pmtmyaggy/numind-web-v3:v1.0.16`，Up healthy
- `numind-admin-web-prod` → `pmtmyaggy/numind-admin-web:v1.4.1`，Up healthy

⚠️ **关键**：server 起来时会触发 GORM AutoMigrate，自动给 `payment_order` 加 `quantity` 列。看 server 日志确认没 panic：

```bash
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker logs numind-server-prod --tail 50 2>&1 | grep -iE 'panic|fatal|listen|error' | head -20"
```

**预期**：看到 `listen on :9091` 类似消息，无 panic / fatal。

---

## 4. 验证阶段（部署后 10–30 分钟内）

### 4.1 🤖 AI 执行：health endpoint

```bash
curl -s -w "\nHTTP %{http_code}\n" --max-time 10 "https://youshu.asia/healthz"
curl -s -w "\nHTTP %{http_code}\n" --max-time 10 "https://youshu.asia/api/v1/credits/balance" \
  -H "Authorization: Bearer ${YOUR_PROD_TOKEN}"  # 你自己登录拿一个
```

### 4.2 👤 你执行：浏览器手测关键路径

打开 `https://youshu.asia`，用你的账号登录：

- [ ] 进设置页 → 看到积分余额（应该来自老 credit_package，迁移后还显示同样数字）
- [ ] 看到"加量包"卡 → 点"立即购买"
- [ ] 选 1 份 → "立即购买" → 看到二维码
- [ ] **关键测试**：扫码付一份 ¥29.9 真实付款，等回调
- [ ] 看积分增加 600（booster_total += 600）
- [ ] 进"客户管理" → 找一个子账户 → action 菜单 → "帮开通会员"
- [ ] 选 trial（200 积分 / 3 天）→ 确认
- [ ] 切到子账户看 `/credits` 页：显示"试用中"badge + trial 余额

### 4.3 🤖 AI 执行：看 Langfuse trace 是否产生

```bash
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker logs numind-server-prod --since 10m 2>&1 | grep -iE 'langfuse|trace_id|generation' | tail -10"
```

**预期**：能看到 langfuse client 初始化日志 + 真实 LLM 调用产生的 trace_id 记录。

---

## 5. 兜底回滚（只在 verify 失败或线上异常时执行）

### 5.1 回滚窗口

- **T+24 小时内**：04-rollback.sql 安全，因为 application 还没机会创建新 subscription/trial/booster 行（除非用户在维护窗口后真的开通了会员/买了 booster）
- **T+24 小时后**：rollback 变破坏性，必须 DBA review

### 5.2 紧急回滚步骤

```bash
# 1. 先回滚 docker image 到 v2.1.12
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" "
  docker stop numind-server-prod
  docker rm numind-server-prod
  docker run -d --name numind-server-prod \
    --network numind-network -p 9095:9091 -p 9096:9092 \
    -e APP_ENV=prod \
    -v /opt/numind/prod:/opt/numind/prod \
    -v /opt/numind/config/cert:/opt/numind/config/cert:ro \
    -v /etc/ssl/certimate/youshu.asia:/etc/ssl/certimate/youshu.asia:ro \
    -v /opt/numind/model/model_cache:/app/model_cache \
    --log-driver json-file --log-opt max-size=20m --log-opt max-file=5 \
    --restart always \
    pmtmyaggy/numind-server:v2.1.12
"

# 2. 跑 rollback.sql 清掉 5 张新表数据
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker exec numind-mysql-prod sh -c \
     'mysql -uroot -pNumind2025 numind-prod < /tmp/04-rollback.sql'" | tail -20

# 3. 同样回滚 web-v3 / admin-web 到上一版本
# （类似 docker stop/rm/run，镜像 tag 改成 ca9c325 / v1.4.0）
```

### 5.3 终极兜底：从备份恢复

```bash
# 仅在 rollback.sql 也救不回时用
sshpass -p "$PROD_SSH_PASS" ssh "$PROD_SSH_USER@$PROD_SSH_HOST" "
  BACKUP=/root/backup-prod-membership-credits-<timestamp>.sql
  docker exec -i numind-mysql-prod mysql -uroot -pNumind2025 numind-prod < \$BACKUP
"
```

---

## 6. 上线后清理（部署成功 7 天后再做）

- [ ] 删除 backup 表：`DROP TABLE migration_20260430_credit_pkg_backup, migration_20260430_apply_log;`
- [ ] 清理 prod 上的临时 SQL 文件：`rm /tmp/01-dry-run.sql /tmp/02-apply.sql /tmp/03-verify.sql /tmp/04-rollback.sql /tmp/20260430_membership_credits_redesign.sql`
- [ ] 更新 manifest stage S5 → S6 → S7（标记 prod 上线完成）
- [ ] 把 `web-v3 prod` 当前那个奇怪的 ca9c325 commit 记录到 hotfix 历史里（防止以后回滚找不到来源）

---

## 7. 已知低优问题（不阻断上线）

| 项 | 影响 | 后续处理 |
|---|---|---|
| Spec 说 cutover_date 缺失要 fatal，实际 admin.go 只 warn | 配置漏写不会停服务，但 B2B 账单会用错口径 | 看是否要按 spec 改 fatal，单独 P2 task |
| dev `_work_sse_test/` 未跟踪目录（你今天的临时诊断脚本）| 仅本地，不影响 dev/prod | 可加 .gitignore 或删除 |
| 几个孤儿本地分支（feature/ai-service-manager / fix/test-free-user-typed-error）| 本地 git 杂草 | 上线后单独清理 |

---

## 附录 A：环境变量速查

```
PROD_SSH_HOST     # prod 主机 IP
PROD_SSH_USER     # root
PROD_SSH_PASS     # 密码
DEV_SSH_HOST      # dev 主机 IP
DEV_SSH_USER      # root
DEV_SSH_PASS      # 密码
```

均在 `.claude/settings.local.json` 配置（不进 git）。

## 附录 B：相关文档

- `docs/superpowers/specs/2026-04-29-membership-credits-redesign-design.md` — 6559 行 spec
- `docs/superpowers/specs/2026-04-29-membership-credits-redesign-validation-strategy.md` — S5 验证策略
- `docs/superpowers/verification/2026-04-30-prod-data-audit.md` — prod 数据 4-30 审计报告
- `docs/superpowers/verification/2026-04-30-migration-dry-run-rehearsal.md` — dev 演练报告
- `scripts/2026-04-30-membership-credits-redesign-migration/README.md` — 4 件套 SQL 的设计说明
- `build-manifest.yaml` — feature `membership-credits-redesign` 条目

---

*Last updated: 2026-05-14 (准备文档创建日)*
