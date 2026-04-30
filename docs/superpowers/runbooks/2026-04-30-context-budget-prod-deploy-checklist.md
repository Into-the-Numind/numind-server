# Prod Deploy Checklist — Context Budget + 155 commits → v2.1.13

> 一次性 checklist，配合本次 prod 部署（2026-04-30）。完整 runbook 推迟到上线后写。
>
> **prod 当前**：v2.1.12（develop @ `5a5f579`，2026-04-28 部署）
> **目标**：v2.1.13 = develop 当前 tip + 各 fix 分支
> **带上 prod 的 commits**：155 个（含 context-budget feature + 5 个 hotfix + credit_multiplier feat + F-7/F-8 fix）

---

## §0 部署前最终核对（5 min）

```bash
# 1. develop tip 是不是你预期的？
git log --oneline -1 develop
# 期望: 含 F-7/F-8 fix + 之前所有 merge

# 2. dev 是不是这套代码？dev 容器最近一次启动时间应该接近 develop 最新 push 时间
sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no "$DEV_SSH_USER@$DEV_SSH_HOST" \
  "docker inspect numind-server-dev --format '{{.State.StartedAt}} | {{.Config.Image}}'"

# 3. dev 最后一次 chatbot 真实调用是 PASS 状态？
sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no "$DEV_SSH_USER@$DEV_SSH_HOST" \
  'docker exec numind-mysql-dev mysql -uroot -pNumind2025 -D"numind-dev" -e "
SELECT id, operation, status, reserved_credits, actual_cost_cents FROM credit_reservation
WHERE estimation_source=\"context_budget\" ORDER BY id DESC LIMIT 3"'
# 期望: 最新一行 status=reconciled, actual_cost_cents 合理（个位数 cents）
```

---

## §1 备份 prod DB（5 min）

```bash
TS=$(date +%Y%m%d-%H%M)
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker exec numind-mysql-prod mysqldump -uroot -pNumind2025 \
   --single-transaction --routines --triggers numind-prod \
   > /backup/pre-r3-${TS}.sql && ls -lh /backup/pre-r3-${TS}.sql"
```

**Gate**: 备份文件大小 > 100MB（与上次备份相当）。

---

## §2 跑 prod migration（2 min）

⚠️ **顺序**：先 migration，后部署代码。否则启动会因找不到表/字段挂掉。

```bash
# 跑核心 R3 schema
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker exec -i numind-mysql-prod mysql -uroot -pNumind2025 -D'numind-prod'" \
  < migrations/20260425_172000_context_budget_compression.sql

# 注意：20260429 (user_type_credit_multiplier) 在 prod 已跑过，跳过！
```

**验证**：

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'docker exec numind-mysql-prod mysql -uroot -pNumind2025 -D"numind-prod" -e "
SHOW TABLES LIKE \"context_budget_%\";
SHOW TABLES LIKE \"token_estimation_profile\";
SHOW TABLES LIKE \"context_summary\";
DESCRIBE credit_reservation
" 2>&1 | grep -E "^(context_|token_|estimation_source|token_profile_id|estimated_prompt|estimated_completion|provider|^model|context_budget_event_id)"'
```

**Gate**: 4 张新表存在（`context_budget_event`, `context_budget_policy`, `context_summary`, `token_estimation_profile`）+ credit_reservation 多 6 个字段。

---

## §3 跑 max_output_tokens backfill（2 min，幂等）

```bash
# Dry-run（预期: 0 个 NEEDS BACKFILL，因为你已手动补完）
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker exec -i numind-mysql-prod mysql -uroot -pNumind2025 -D'numind-prod'" \
  < scripts/2026-04-27-context-budget-max-output-backfill/01-dry-run.sql 2>&1 | tail -10

# Apply（兜底；如果 dry-run 显示 0 个，apply 也是 0 影响）
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker exec -i numind-mysql-prod mysql -uroot -pNumind2025 -D'numind-prod'" \
  < scripts/2026-04-27-context-budget-max-output-backfill/02-apply.sql 2>&1 | tail -5
```

**Gate**: verify 全 OK 或 WARN（warn 是 max_output < 16384 的合法低值，比如 deepseek-v3.2 8192）。

---

## §4 打 tag → CI 部署（5 min + ~5 min CI）

```bash
git checkout develop
git pull origin develop
git log --oneline -1   # 确认 tip 没变

git tag v2.1.13 -m "Release v2.1.13: context-budget-compression + 155-commit hotfix bundle

包含:
- context-budget-compression feature (S5-done)
- credit_multiplier per-model feat (511edff/fa2cb2f)
- F-7 cost holder set flag fix
- F-8 hardening (charge_user *bool)
- 5 prod hotfix (wechat/alipay/customer-list/cert-mount/booster-quantity)
- 多个 NDF S6/S7-done 项

Migration 20260425 必须先跑（建 4 张 R3 表 + credit_reservation 6 字段）。
backfill SQL 已跑作为兜底。"

git push origin v2.1.13
```

**等 CI**:

```bash
gh run list --branch develop --limit 1   # 应看到 v2.1.13 build 启动
# 也可看 tag 触发的 release pipeline
gh run list --workflow "release" --limit 3
```

**部署确认**:

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker inspect numind-server-prod --format '{{.State.StartedAt}} | {{.Config.Image}} | {{.State.Status}}'"
```

**Gate**: image 是 `pmtmyaggy/numind-server:v2.1.13`，状态 `running`，启动 log 无 panic。

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker logs numind-server-prod --tail 50 2>&1 | grep -iE 'error|panic|fatal'"
# 期望: 无致命 error
```

---

## §5 Canary 真实调用验证（10 min）

用 admin 账号自己当 canary（避免影响真实客户）。

**Step 5a — 登录 prod 拿 token**:

```bash
PROD_TOKEN=$(curl -sS -X POST "https://youshu.asia/api/v1/web/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"$E2E_USERNAME\",\"password\":\"$E2E_PASSWORD\"}" \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['data']['access_token'])")
echo "token: ${PROD_TOKEN:0:30}..."
```

**Step 5b — chatbot 调用**:

```bash
curl -sS -N --max-time 60 -X POST "https://youshu.asia/api/v1/chatbot/sessions/<SESSION_ID>/chat" \
  -H "Authorization: Bearer $PROD_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -d '{"message":"v2.1.13 canary","model_key":"gemini-3-pro"}' 2>&1 | tail -3

# 注意: SESSION_ID 用 prod 上 admin 自己的 chatbot session id
```

**Step 5c — DB 验证全链路**:

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'docker exec numind-mysql-prod mysql -uroot -pNumind2025 -D"numind-prod" -e "
SELECT id, status, reserved_credits, actual_cost_cents, model FROM credit_reservation
WHERE estimation_source=\"context_budget\" ORDER BY id DESC LIMIT 1;
SELECT id, operation, status, reservation_id, actual_prompt_tokens FROM context_budget_event
ORDER BY id DESC LIMIT 1;
SELECT id, prompt_tokens, completion_tokens, cost_cents, JSON_EXTRACT(metadata, \"\$.context_budget_event_id\") AS ev FROM usage_record
WHERE user_id=<ADMIN_USER_ID> ORDER BY id DESC LIMIT 1;
"'
```

**Gate**:
- ✅ reservation `status=reconciled`，`actual_cost_cents` 合理（个位/十位 cents）
- ✅ event `status=ok`，`reservation_id` 非 NULL
- ✅ usage_record metadata 含 `context_budget_event_id`

如果有任何 ❌ → 进入 §7 回滚决策。

---

## §6 24h 观察期监控

每 4-6 小时执行一次：

```bash
# 看 reservation 状态分布（是否有卡住的 reserved）
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'docker exec numind-mysql-prod mysql -uroot -pNumind2025 -D"numind-prod" -e "
SELECT estimation_source, status, COUNT(*) AS cnt FROM credit_reservation
WHERE created_at >= NOW() - INTERVAL 4 HOUR
GROUP BY estimation_source, status ORDER BY estimation_source, status"'

# 看 event 错误率
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'docker exec numind-mysql-prod mysql -uroot -pNumind2025 -D"numind-prod" -e "
SELECT status, COUNT(*) AS cnt FROM context_budget_event
WHERE created_at >= NOW() - INTERVAL 4 HOUR GROUP BY status"'
```

**告警阈值**:
- `credit_reservation status='reserved'` 占比 > 1% → orphan 太多，定位 finalize 问题
- `context_budget_event status` 非 'ok' 占比 > 5% → 估算/压缩异常
- 任何 panic 出现在 docker logs → 立即 §7

---

## §7 回滚预案（应急用）

**触发条件**：
- migration 失败（§2 Gate 不过）
- 启动 panic（§4 docker logs 显示 fatal）
- canary 失败（§5 Gate 多个 ❌）
- 24h 观察期出现 P0（orphan reserved > 1% or panic）

**回滚动作**:

```bash
# 1. 重新部署 v2.1.12
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker stop numind-server-prod && \
   docker rm numind-server-prod && \
   docker run -d --name numind-server-prod ...（保留原 docker run 命令）... \
   pmtmyaggy/numind-server:v2.1.12"

# 2. 回滚 schema（如果 migration 跑过了）
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker exec -i numind-mysql-prod mysql -uroot -pNumind2025 -D'numind-prod'" \
  < migrations/20260425_172000_context_budget_compression_rollback.sql

# 3. （可选）从 §1 备份恢复
# 仅当数据明显损坏时使用，schema rollback 通常足够
```

**回滚后**:
- 通知用户：本次部署回滚
- 写一份 incident note：什么挂了、回滚耗时、下次怎么避免

---

## §8 部署成功后（10 min）

```bash
# 标 manifest S6-done
# 编辑 build-manifest-archive.yaml 的 context-budget-compression 条目，把 stage 从 S5-done 改成 S6-done

# 提交并 push
git add build-manifest-archive.yaml
git commit -m "chore(manifest): context-budget-compression S6-done after v2.1.13 deploy"
git push origin develop
```

**14 天后**：跑 `docs/superpowers/runbooks/context-budget-calibration.md` 第 1 轮调参。

---

*生成于：2026-04-30*
*作者：本次部署执行 AI*
*仅用于 v2.1.13 部署，复用前请审视前置假设*
