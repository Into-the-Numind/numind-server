# AI Service Manager Migration Runbook

Migration 文件：`migrations/20260416_000001_ai_service_manager.sql`
Rollback 文件：`migrations/20260416_000001_ai_service_manager_rollback.sql`
关联 Spec：`docs/superpowers/specs/2026-04-15-ai-service-manager-design.md §2`

---

## 前置条件

### MySQL 版本要求

**最低 MySQL 8.0.13**（需要 `JSON DEFAULT (JSON_OBJECT())` 表达式语法）。

查询 prod MySQL 版本（不需要现在跑，供部署时核查）：

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'docker exec numind-mysql-prod mysql -N -e "SELECT VERSION();"'
```

期望输出：`8.0.x` 且 x >= 13

### 需要的工具

- `mysql` 客户端（本地或通过 SSH + docker exec）
- `sshpass`（SSH 密码认证）
- `docker`（本地演练用）
- `mysqldump`（schema dump 用）

---

## 演练（本地 Docker 验证）

**演练目的**：在隔离环境中验证 migration + rollback 可完整执行，然后再 apply 到 prod。

### 完整演练命令序列（复制可跑）

```bash
# ========================================
# Step 1: 启动 MySQL 8.0.36 测试容器
# ========================================
docker run --rm -d --name numind-mig-test \
  -e MYSQL_ROOT_PASSWORD=testpass \
  -p 23306:3306 \
  mysql:8.0.36

# ========================================
# Step 2: 等待 MySQL 就绪（最多 40s）
# ========================================
for i in $(seq 1 20); do
  if docker exec numind-mig-test mysqladmin -uroot -ptestpass ping 2>&1 | grep -q "alive"; then
    echo "MySQL ready after ${i}×2s"; break
  fi
  sleep 2
done

# ========================================
# Step 3: 创建测试数据库
# ========================================
docker exec numind-mig-test mysql -uroot -ptestpass \
  -e "CREATE DATABASE IF NOT EXISTS numind CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"

# ========================================
# Step 4: 从 dev 环境 dump 现有 schema 作为基线
# ========================================
sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no -q "$DEV_SSH_USER@$DEV_SSH_HOST" \
  'docker exec numind-mysql-dev mysqldump -uroot -pNumind2025 --no-data --single-transaction numind-dev 2>/dev/null' \
  > /tmp/numind_dev_schema.sql

# ========================================
# Step 5: 应用 dev 基线 schema 到测试容器
# ========================================
docker exec -i numind-mig-test mysql -uroot -ptestpass numind < /tmp/numind_dev_schema.sql

# ========================================
# Step 6: 执行 migration
# ========================================
REPO_DIR="/path/to/numind-server"  # 替换为实际路径
docker exec -i numind-mig-test mysql -uroot -ptestpass numind \
  < "$REPO_DIR/migrations/20260416_000001_ai_service_manager.sql"

# ========================================
# Step 7: 验证 migration 结果
# ========================================
docker exec numind-mig-test mysql -uroot -ptestpass numind -e "
SELECT 'task_profile count:', COUNT(*) FROM task_profile;
-- 应 = 14

SELECT model_key, service_type FROM ai_service WHERE service_type IN ('ocr','asr');
-- 应看到 baidu-ocr-accurate 和 funasr-paraformer

SELECT COUNT(*) as non_llm_providers FROM llm_provider WHERE provider_type != 'llm';
-- 应 = 3（baidu-ocr, bailian-file, funasr-local）

SELECT * FROM llm_model LIMIT 1;
-- VIEW 应可读

SHOW FULL TABLES WHERE Tables_in_numind IN
  ('ai_service','ai_service_route','task_profile','task_profile_service','ai_service_audit_log');
-- 以上 5 张表应全部存在，TABLE_TYPE = BASE TABLE

EXPLAIN SELECT * FROM usage_record
  WHERE task_id='sop.text' AND created_at > NOW() - INTERVAL 7 DAY;
-- key 应命中 idx_task_created（验证 S2 spec P2-1 落地）
"

# ========================================
# Step 8: 执行 rollback（验证可回滚）
# ========================================
docker exec -i numind-mig-test mysql -uroot -ptestpass numind \
  < "$REPO_DIR/migrations/20260416_000001_ai_service_manager_rollback.sql"

# ========================================
# Step 9: 验证 rollback 结果
# ========================================
docker exec numind-mig-test mysql -uroot -ptestpass numind -e "
SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES
  WHERE TABLE_SCHEMA=DATABASE()
  AND TABLE_NAME IN ('llm_model','llm_model_provider','ai_service','task_profile');
-- llm_model 和 llm_model_provider 应为 BASE TABLE；ai_service 和 task_profile 应不存在

SHOW COLUMNS FROM usage_record LIKE 'task_id';
-- 应返回空（列已删除）

SHOW COLUMNS FROM llm_provider LIKE 'provider_type';
-- 应返回空（列已删除）
"

# ========================================
# Step 10: 清理测试容器
# ========================================
docker stop numind-mig-test
```

### 已验证的演练结果（2026-04-15）

- 基线：dev 环境 schema dump（MySQL 8.4.2）
- Migration 执行：无错误输出，所有 5 张新表创建成功
- 验证结果：
  - `task_profile` = 14 条（含 2 个 `user_selectable=1`：chatbot.stream、salesrag.chat）
  - `provider non-llm` = 3（baidu-ocr、bailian-file、funasr-local）
  - `ai_service OCR/ASR` = 2（baidu-ocr-accurate、funasr-paraformer）
  - `llm_model` VIEW 可读
  - `idx_task_created` EXPLAIN 命中（Using index condition）
- Rollback 执行：无错误输出，干净回到基线状态
  - `llm_model`、`llm_model_provider` 恢复为 BASE TABLE
  - `ai_service`、`task_profile` 等新表不存在
  - `usage_record.task_id` 列不存在
  - `llm_provider.provider_type` 列不存在

---

## Prod 部署流程

> ⚠ **本 migration 需要短暂维护窗口（2-3 分钟）**。MySQL DDL（ALTER/RENAME）是非事务性的，在线执行会造成不可预期的时间窗问题。

> **前置要求**：执行本节任何 Prod 步骤前，必须先在本地 shell 设置环境变量：
> ```bash
> export PROD_DB_PASS=<实际生产数据库密码>
> ```
> 密码从团队密钥管理工具获取，禁止硬编码到任何文件或脚本中。

### 部署前检查清单

- [ ] 已在 dev 环境（基于 dev DB snapshot）完整演练 migration + rollback
- [ ] 已确认 prod MySQL 版本 >= 8.0.13（见上方版本查询命令）
- [ ] 已通知相关人员维护窗口时间（预计 3-5 分钟）
- [ ] 已备份 prod DB（建议 `mysqldump` 完整备份或 RDS 快照）

### 部署步骤

**Step 1: 备份 prod 数据库**

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'docker exec numind-mysql-prod mysqldump -uroot -p"$PROD_DB_PASS" \
    --single-transaction --routines --triggers numind-prod \
    > /root/backup/numind_pre_ai_service_manager_$(date +%Y%m%d_%H%M%S).sql'
```

**Step 2: 停止 numind-server 和 numind-admin 服务**

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'docker stop numind-server-prod numind-admin-server-prod 2>/dev/null || true'
```

验证服务已停止：

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'docker ps --format "{{.Names}}: {{.Status}}" | grep numind'
# 期望：numind-server-prod 和 numind-admin-server-prod 不出现在运行列表中
```

**Step 3: 将 migration 文件传到 prod 服务器**

```bash
sshpass -p "$PROD_SSH_PASS" scp -o StrictHostKeyChecking=no \
  migrations/20260416_000001_ai_service_manager.sql \
  "$PROD_SSH_USER@$PROD_SSH_HOST:/root/numind/migrations/"
```

**Step 4: 执行 migration**

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'docker exec -i numind-mysql-prod mysql -uroot -p"$PROD_DB_PASS" numind-prod \
    < /root/numind/migrations/20260416_000001_ai_service_manager.sql'
```

**Step 5: 验证 migration 成功**

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'docker exec numind-mysql-prod mysql -uroot -p"$PROD_DB_PASS" numind-prod -e "
    SELECT COUNT(*) as task_profile_count FROM task_profile;
    SELECT COUNT(*) as non_llm_providers FROM llm_provider WHERE provider_type != '\''llm'\'';
    SHOW FULL TABLES WHERE Tables_in_numind IN (
      '\''ai_service'\'','\''ai_service_route'\'','\''task_profile'\'',
      '\''task_profile_service'\'','\''ai_service_audit_log'\'');
  "'
# 期望：task_profile_count = 14，non_llm_providers = 3，5 张表全部 BASE TABLE
```

**Step 6: 启动服务**

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'cd /root/numind && docker-compose -f docker-compose.prod.yml up -d numind-server-prod numind-admin-server-prod'
```

注：服务启动时 `aiservice.SyncProviderCredentials` 会自动将 config 中的 API key 同步到 `llm_provider` 表（Task 8 实现后生效）。

**Step 7: 验证服务健康**

```bash
# 等待 10s 让服务启动
sleep 10

# 检查 healthz
curl -s https://youshu.asia/healthz
# 期望：{"status": "ok"} 或 HTTP 200

# 验证 AI healthz（Task 8 实现后可用）
curl -s https://youshu.asia/healthz/ai
```

---

## 异常处理与 Rollback

### 何时执行 rollback

- Step 4 migration 执行报错且无法继续
- Step 5 验证发现数据异常（如 task_profile_count != 14）
- Step 7 服务无法启动（与本 migration 相关的原因）

### Rollback 步骤

**Step R1: 确保服务已停止**（如果还在运行）

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'docker stop numind-server-prod numind-admin-server-prod 2>/dev/null || true'
```

**Step R2: 将 rollback 文件传到 prod 服务器**

```bash
sshpass -p "$PROD_SSH_PASS" scp -o StrictHostKeyChecking=no \
  migrations/20260416_000001_ai_service_manager_rollback.sql \
  "$PROD_SSH_USER@$PROD_SSH_HOST:/root/numind/migrations/"
```

**Step R3: 执行 rollback**

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'docker exec -i numind-mysql-prod mysql -uroot -p"$PROD_DB_PASS" numind-prod \
    < /root/numind/migrations/20260416_000001_ai_service_manager_rollback.sql'
```

**Step R4: 验证 rollback 成功**

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'docker exec numind-mysql-prod mysql -uroot -p"$PROD_DB_PASS" numind-prod -e "
    SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES
      WHERE TABLE_SCHEMA=DATABASE()
      AND TABLE_NAME IN ('\''llm_model'\'','\''llm_model_provider'\'','\''ai_service'\'','\''task_profile'\'');
    SHOW COLUMNS FROM usage_record LIKE '\''task_id'\'';
  "'
# 期望：llm_model 和 llm_model_provider 为 BASE TABLE，ai_service 和 task_profile 不存在，task_id 列不存在
```

**Step R5: 启动旧版本服务**（使用 rollback 前的 image tag）

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'cd /root/numind && docker-compose -f docker-compose.prod.yml up -d numind-server-prod numind-admin-server-prod'
```

---

## 注意事项

### AMBIGUITY 记录

⚠️ **BLOCKER: Task 1a 完成后，`task_profile` 表的 14 条数据为"半成品状态"——所有 `default_service_id=NULL` 且 `task_profile_service` 为空。在 Task 8（Gateway 入口 + seed 生效）完成之前，任何直接读取 `task_profile` 的代码必须容错 NULL。业务调用路径在 Task 8 前仍走老 llmrouter 不受影响。**

1. **task_profile.default_service_id 留 NULL**：spec §5.1 中指定的默认服务（如 deepseek-v3、qwen-plus）在 `ai_service` 表中的 `model_key` 尚未确认存在。migration 将 `default_service_id` 留为 NULL，待 Task 8 的 `SyncProviderCredentials` 或运营通过管理端完成绑定。

2. **task_profile_service 未 seed**：fallback 和 allowed 绑定数据留空，由运营通过管理端（`PUT /v1/admin/ai/tasks/:id`）手动配置。

3. **usage_record.service_type 兼容**：dev 环境的 `usage_record` 已有 `service_type VARCHAR(50) NOT NULL`（来自老 billing schema），migration 通过 PROCEDURE 做条件检查跳过已有列，保持向下兼容。

### 幂等性

本 migration 使用 `INSERT IGNORE`、`CREATE TABLE IF NOT EXISTS`、`CREATE OR REPLACE VIEW` 和条件 PROCEDURE，可在同一数据库上重复执行不报错（即幂等性）。

### prod MySQL 版本核查命令

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  'docker exec numind-mysql-prod mysql -N -e "SELECT VERSION();"'
```

期望结果：8.0.x，且 x >= 13
