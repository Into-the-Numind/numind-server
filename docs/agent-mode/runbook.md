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

## 8. 网络搜索 (web_search) 故障排查

### Tavily quota 耗尽 (429)

```bash
# Langfuse 确认 — 查 cache_hit + HTTP 状态
# 在 Langfuse UI 找到对应 agent_run trace，展开 Span tool.web_search.execute
# 看 metadata.status_code=429 + metadata.cache_hit=false
```

**处理步骤：**
1. 确认是 429 还是其他错误：log 含 `"tavily: status 429"` 即为限流
2. 短期：tool 内置 sleep + retry（最多 3 次，指数退避 1s/2s/4s）；若仍失败返 `ErrExternalAPI` + narration "搜索暂时不可用，请稍后再试"
3. 中期：Tavily 控制台确认当前 plan 的月度配额；需要扩量时联系 support 或升级 plan
4. Dev 环境测试：切换到 Tavily Dev plan（10,000 次/月免费）避免消耗 Prod quota

### 配置缺 api_key (401)

**症状**：log 含 `"tavily: status 401"` 或 tool 启动时告警 `"web_search.tavily.api_key empty"`

**原因**：`config_*.yaml` 的 `web_search.tavily.api_key` 为空字符串或未设置

**处理步骤：**
1. `grep "tavily" config_local.yaml config_dev.yaml config_qa.yaml` 确认字段存在
2. 本地：在 `config_local.yaml` 填入 Tavily API key（不进 git）
3. Dev/QA：联系运维在部署机的 `config_dev.yaml` / `config_qa.yaml` 补充配置，然后重启服务
4. tool 启动时检测 api_key 为空会 warn log 并跳过 HTTP 调用，narration 输出"搜索暂不可用"

### web_search Langfuse Span 缺失

**症状**：agent 调 web_search 但 Langfuse trace 无对应 Span

**处理**：`grep "tool.web_search.execute" <app log>` 确认 Span 是否正常写入；若 Langfuse 连接断开，trace 写入 async channel，不阻塞工具调用——看 AuditLogger drop 监控（§7）

---

## 9. 网页读取 (web_fetch) 故障排查

### SSRF 拒绝（期望行为，无需 action）

**症状**：log 含 `"web_fetch: disallowed IP"` / `"internal hostname not allowed"` / `"private IP range rejected"`

**这是正常的安全行为**：`web_fetch` 对内网 IP（RFC1918）/ `*.local` / 云 metadata 地址（169.254.x.x）/ localhost 一律拒绝。

如遇误报（例如合法 CDN IP 被误拒）：检查 `validateFetchURL` 函数，确认 DNS 解析结果是否真为私有 IP；确实误报则提 bug，走 NDF hotfix。

### 30s timeout（请求挂起）

**症状**：Langfuse Span `tool.web_fetch.execute` 的 `latency_ms > 30000`，返回 `ErrTimeout`

**处理步骤：**
1. 确认目标域名是否可达（让学员换一个 URL 试试）
2. 查 agent_run.messages 确认 tool narration 是否提示"网页读取超时"
3. 若特定域名系统性超时：考虑加入 fetch 黑名单（配置）或在 narration 提示"该网站响应慢，建议直接复制文字"

### 100KB 截断

**症状**：tool 返回 `truncated=true`，narration 含"内容过长，仅读取前 100KB"

**这是期望行为**。如确需读取完整内容（如大型技术文档），建议用户复制 PDF 上传（走 file_read 路径）或分段请求。

---

## 10. 反问学员 (ask_user_question) stuck run 处理

### 查询 stuck run

```sql
-- 查所有 waiting_for_user_choice 超 30 分钟无应答的 run
SELECT
  r.id,
  r.user_id,
  r.agent_def_id,
  r.state_reason,
  r.pending_question_at,
  TIMESTAMPDIFF(MINUTE, r.pending_question_at, NOW()) AS wait_minutes
FROM agent_run r
WHERE r.state_reason = 'waiting_for_user_choice'
  AND r.pending_question_at < NOW() - INTERVAL 30 MINUTE
ORDER BY r.pending_question_at ASC;
```

### 手工 cancel stuck run

**方式 A：Admin Web**（推荐）

admin-web → **AI 助手 → Agent 监控** → 筛选 `state_reason=waiting_for_user_choice` → 点 **[强制取消]**

**方式 B：Admin API**

```bash
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  https://api.youshu.asia/v1/admin/agent-runs/$RUN_ID/cancel \
  -d '{"reason":"stuck_waiting_for_user"}'
```

### Budget 在 yield 期间仍累计（T7 未完全 wire）

**症状**：run 处于 `waiting_for_user_choice` 状态但 credit_reservation 的 used_amount 仍增加

**原因**：BudgetTracker.Pause() 在 yield 时调用，BudgetTracker.Resume() 在 answer 时调用，若 T7 wire 未完成则 Pause/Resume 可能未生效

**处理**：
1. `SELECT * FROM credit_reservation WHERE agent_run_id = $RUN_ID` 确认 used_amount 变化时间戳
2. 若 wait 期间有异常扣减，记录 credit_transaction，联系财务走人工补偿（积分退还）
3. 上报 bug，归入 T7 后续 hotfix

---

## 11. 读取文件 (file_read) 故障排查

### 跨账户读拒（期望行为）

**症状**：log 含 `"file_read: file not owned by current user"` / `ErrPermissionDenied`

**这是正常的安全行为**：`file_read` 解析 attachment URL 中的 `<userID>` 路径段，与 ctx 中的 userID 对比；不匹配时拒绝，narration 提示"您无权访问该文件"。

### PDF 解析慢

**症状**：Langfuse Generation `tool.file_read.execute` 的 `latency_ms > 30000`

**原因**：PDF 走 qwen-long 解析，每页约 5s；> 60 页时返 ErrTooLarge（在 200KB 截断前通常早已触发）

**处理步骤：**
1. 查 Generation metadata 的 `page_count` 字段确认页数
2. 告知学员上传 < 60 页 / < 200KB 的 PDF 以获得最佳体验
3. 若特定 PDF 格式导致超时（扫描版大图 PDF）：narration 提示"该 PDF 解析较慢，请耐心等待或上传文字版 PDF"

### 不支持 mime（docx / xlsx 等）

**症状**：log 含 `ErrUnsupportedFileType`，narration 提示"暂不支持该文件格式"

**当前 v1 支持**：`application/pdf` / `image/*` / `text/plain` / `text/markdown`

**不支持**：`application/vnd.openxmlformats-officedocument.*`（docx/xlsx）/ `application/zip` 等

**处理**：建议学员将 Word/Excel 另存为 PDF 后上传；v2 可考虑加 docx/xlsx 支持（走 NDF standard track）

### HEAD 请求失败

**症状**：log 含 `"file_read: HEAD request failed"` + `ErrExternalAPI`

**处理**：
1. 确认附件 URL 是否仍有效（文件是否已被删除 / 签名过期）
2. 若 OSS 签名 URL 有效期短（< 30 分钟），学员需重新上传文件再发消息

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
