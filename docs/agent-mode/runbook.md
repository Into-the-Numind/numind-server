# Agent Mode v1.0-final Runbook

> oncall 操作手册。人类操作员或技术支持查阅；autopilot AI 永不自动执行 §5-§7。

---

## 1. 紧急取消失控 Agent

**场景**：学员反馈某 agent 卡死 / 无限 loop / 积分消耗异常，需立即中止。

### 方式 A：Admin Web UI（推荐）

1. 登录 admin-web（`https://admin.youshu.asia` 或 dev 地址）
2. 进入 **AI 助手 → Agent 监控**
3. 按 user_id / agent_name / 时间范围筛选，找到对应 `agent_run`
4. 点击 **[强制取消]** 按钮 → ConfirmModal 确认
5. 刷新确认 `agent_run.state` 变为 `cancelled`

### 方式 B：Admin API direct（无 UI 访问时）

```bash
# 先拿 admin token
ADMIN_TOKEN=$(curl -s -X POST https://api.youshu.asia/v1/admin/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<pass>"}' | jq -r '.data.token')

# 强制取消
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  https://api.youshu.asia/v1/admin/agent-runs/$RUN_ID/cancel \
  -d '{"reason":"oncall_force_cancel"}'
```

成功响应：`{"code":0,"message":"ok","data":{"run_id":...,"state":"cancelling"}}`

### 方式 C：DB 直写（极端情况，API 不可用时）

```sql
-- 先确认 run 状态
SELECT id, state, state_reason, user_id FROM agent_run WHERE id = $RUN_ID;

-- 强制设 cancelled（跳过正常流程，仅限 P0 事故）
UPDATE agent_run
  SET state = 'cancelled',
      state_reason = 'oncall_force_cancel',
      updated_at = NOW()
  WHERE id = $RUN_ID AND state NOT IN ('completed', 'failed', 'cancelled');
```

**注意**：DB 直写不会触发 credit reconcile，需后续人工核查积分。

---

## 2. 查 Compliance Audit

**场景**：某机构反馈学员问题被误拦截 / 应拦截没拦截。

```sql
-- 按 parent_user_id + 时间范围查
SELECT
  cal.id,
  cal.user_id,
  cal.parent_user_id,
  cal.agent_run_id,
  cal.check_layer,
  cal.action,
  cal.rule_id,
  cal.matched_phrase,
  cal.created_at
FROM compliance_audit_log cal
WHERE cal.parent_user_id = $TARGET_PARENT_ID
  AND cal.created_at > NOW() - INTERVAL 7 DAY
ORDER BY cal.created_at DESC
LIMIT 50;

-- 按学员 user_id 查（含所有 layer）
SELECT * FROM compliance_audit_log
WHERE user_id = $STUDENT_USER_ID
ORDER BY created_at DESC LIMIT 20;
```

**规则调整**（拦截过严 / 过松）：

1. admin-web → **合规管理 → 规则列表**
2. 找到对应 `compliance_rule`（`parent_user_id` 归属机构）
3. 修改 `pattern` / `action`（`forbid` / `warn` / `allow`）或禁用 `is_active = false`
4. **Layer-0（全局规则）**不在此处管理，需联系技术团队修改 `compliance_rule` 的 `scope = 'global'` 行

---

## 3. 升降 Budget 阈值

**场景**：某机构整体积分消耗超预算 / 需要紧急限速。

### 降低单 session 上限

```sql
-- 查当前阈值
SELECT id, name, credit_cap_per_session, daily_credit_cap
FROM agent_definition
WHERE parent_user_id = $PARENT_ID;

-- 修改（可用 admin-web UI 操作，无需 SQL）
UPDATE agent_definition
  SET credit_cap_per_session = 200,  -- 原值可能更高
      updated_at = NOW()
  WHERE id = $AGENT_DEF_ID;
```

### 降低每日上限

```sql
UPDATE agent_definition
  SET daily_credit_cap = 1000,
      updated_at = NOW()
  WHERE id = $AGENT_DEF_ID;
```

### 紧急冻结（立即下线某 agent）

admin-web UI：AI 助手 → Agent 管理 → 找到 agent → **[停用]**

或 SQL：

```sql
UPDATE agent_definition SET is_active = 0, updated_at = NOW()
WHERE id = $AGENT_DEF_ID;
-- 学员端实时生效，进行中的 run 不受影响（会自然结束）
```

---

## 4. Langfuse Trace 查询

**场景**：排查某 run 的 LLM 调用链 / 计费异常 / 模型选择。

### 方式 A：Admin Web 跳转

1. admin-web → **Agent 监控** → 找到对应 run
2. 点击 **trace_id** 链接 → 自动跳 Langfuse UI

### 方式 B：直接访问

```
https://langfuse.youshu.asia/trace/$TRACE_ID
```

### 方式 C：SQL 查 trace_id

```sql
SELECT id, trace_id, state, user_id, created_at
FROM agent_run
WHERE user_id = $USER_ID
ORDER BY created_at DESC LIMIT 10;
```

---

## 5. Sandbox iptables 配置（v1 out-of-scope — 运维手动）

**当前状态**：v1 Sandbox 走 Docker bridge 网络，无额外网络限制。

v2 收紧前，可临时在 prod 主机加 iptables 规则限制 sandbox container 出口：

```bash
# 查 sandbox container subnet（从 docker network inspect 获取）
SANDBOX_SUBNET=$(docker network inspect agent-sandbox-net \
  --format '{{range .IPAM.Config}}{{.Subnet}}{{end}}')

# 允许访问 aiservice gateway（内网地址）
iptables -A DOCKER-USER -s $SANDBOX_SUBNET -d $AISERVICE_INTERNAL_IP -j ACCEPT

# 允许 DNS
iptables -A DOCKER-USER -s $SANDBOX_SUBNET -p udp --dport 53 -j ACCEPT

# 拒绝其他出口
iptables -A DOCKER-USER -s $SANDBOX_SUBNET -j REJECT

# 持久化（CentOS/RHEL）
service iptables save
```

**注意**：此为临时 oncall 操作。正式收紧需写到 docker-compose.yml / K8s NetworkPolicy，并走 NDF standard feature。

---

## 6. L1 Memory TTL Cron 配置（v1 out-of-scope — 运维手动）

**当前状态**：`agent_session_memory.expires_at` 字段写入但无自动清理 cron。

**临时手动清理：**

```bash
# SSH 到部署机，执行 MySQL 清理
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "mysql -u <db_user> -p<db_pass> <db_name> -e \
   'DELETE FROM agent_session_memory WHERE expires_at IS NOT NULL AND expires_at < NOW();
    SELECT ROW_COUNT() AS deleted_rows;'"
```

**长期方案（v2 — K8s CronJob）：**

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: agent-memory-l1-cleanup
  namespace: numind
spec:
  schedule: "0 3 * * *"    # 每天凌晨 3 点（业务低谷）
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 1
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: OnFailure
          containers:
          - name: cleanup
            image: mysql:8.0
            command:
            - mysql
            - -h$(MYSQL_HOST)
            - -u$(MYSQL_USER)
            - -p$(MYSQL_PASS)
            - $(MYSQL_DB)
            - -e
            - "DELETE FROM agent_session_memory WHERE expires_at IS NOT NULL AND expires_at < NOW();"
            env:
            - name: MYSQL_HOST
              valueFrom:
                secretKeyRef:
                  name: numind-db-secret
                  key: host
            # ... 其他 env
```

---

## 7. AuditLogger Drop 监控（v1 log-based — A9）

**背景**：v1 AuditLogger 用 async channel queue（buffer=1000）。queue 满时丢弃 + 内存计数。
阈值 `audit_drop_threshold=10`：累计丢弃超 10 条写一条 WARN 日志。

**监控方式：**

```bash
# 从 journalctl 查（部署机）
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "journalctl -u numind-server --since '1 hour ago' | grep 'audit drop count exceeded threshold'"

# 或通过 Docker logs
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "docker logs numind-server 2>&1 | grep 'audit drop'"
```

**应对措施：**

| 严重程度 | 判断 | 处理 |
|---------|------|------|
| 偶发（每日 < 5 次） | 瞬间流量尖峰 | 观察，记录到 retro |
| 频繁（每小时 > 1 次） | audit pipeline 过载 | 增加 channel buffer；考虑独立 audit service |
| 持续（每分钟 > 10 次） | P1 事故 | 立即拉大 buffer + 告警 + 下一个 sprint 修复 |

```sql
-- 同步核查：audit log 行数与 agent_run 数量是否合理
SELECT
  DATE(created_at) AS day,
  COUNT(*) AS audit_rows
FROM compliance_audit_log
WHERE created_at > NOW() - INTERVAL 7 DAY
GROUP BY day ORDER BY day DESC;
```
