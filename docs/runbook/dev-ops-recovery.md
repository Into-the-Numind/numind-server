# Dev 环境运维恢复手册

> 本文件记录 dev 环境 (49.233.219.254) 常见运维场景的恢复步骤，减少 AI / 新人
> 手工运维时踩坑。本文**不覆盖 prod** —— prod 有 CI/CD 全自动流水线。

---

## 1. 手工 restart `numind-server-dev` 或 `numind-admin-server-dev` 后必须同时 restart nginx 容器

### 现象
手工 `docker restart numind-admin-server-dev` 后，admin web 页面访问（浏览器访问
`http://49.233.219.254:9100/...`）返回 **502 Bad Gateway**，但直连
`http://49.233.219.254:9099/healthz` 正常 200。

admin-web nginx log 显示：
```
connect() failed (111: Connection refused) while connecting to upstream,
upstream: "http://172.20.0.X:9099/..."
```

### 根因
admin-server 容器重启后 Docker 分配新的内网 IP（172.20.0.X 会变），但
`numind-admin-web-dev` 内的 nginx 已缓存旧 IP 的 DNS 解析，拒绝刷新。同样问题
影响 `numind-web-v3-dev`（用户端 nginx）面对 `numind-server-dev` 重启。

### 恢复步骤
```bash
# Admin 链路
docker restart numind-admin-web-dev

# 用户端链路
docker restart numind-web-v3-dev

# 两套都 restart（admin-server + server 都重启时）
docker restart numind-admin-web-dev numind-web-v3-dev
```

### 为什么 CI 部署不受影响
CI (`ci-cd.yaml` deploy_dev)使用 `docker rm + docker run` 重建整条链路，每次
都 fresh，不会触发 DNS 缓存问题。只有**手工** `docker restart` 才会踩坑。

### 长期修法（后续考虑）
在 admin-web / web-v3 的 nginx conf 里用 `resolver 127.0.0.11 valid=5s` +
`set $upstream ...` 变量写法强制 DNS TTL。当前 `proxy_pass http://numind-
admin-server-dev:9099/v1/;` 硬编码 DNS，nginx 启动时解析一次后不再刷新。

---

## 2. 手工修改 dev MySQL 表结构后 AutoMigrate 启动失败

### 现象
手工 `CREATE TABLE` 或 `ALTER TABLE` 某张 model 对应的表后，下次 admin-server
或 server 启动，GORM AutoMigrate 报：

```
Error 1061 (42000): Duplicate key name '<index_name>'
failed to migrate basic tables: ...
```

容器启动失败，admin 持续 502。

### 根因
GORM 的索引名规则（`idx_<tablename>_<column>`、`uniq_<tablename>_<column>`）
和手工 CREATE TABLE 时用的 UNIQUE KEY 名不一致。启动时 AutoMigrate 再次 CREATE
索引，MySQL 拒绝重复名。

### 恢复步骤
```bash
# 1. 查当前所有 index
docker exec numind-mysql-dev mysql -uroot -pNumind2025 numind-dev \
  -e "SHOW INDEX FROM <table_name>"

# 2. 删除你手工建的冲突 index（保留 GORM 约定的）
docker exec numind-mysql-dev mysql -uroot -pNumind2025 numind-dev \
  -e "ALTER TABLE <table_name> DROP INDEX <your_custom_key_name>"

# 3. restart admin-server 让 AutoMigrate 继续
docker restart numind-admin-server-dev numind-server-dev
docker restart numind-admin-web-dev numind-web-v3-dev   # 同时 refresh nginx
```

### 防坑原则
手工建 dev 表时，**索引命名用 GORM 风格**：
- `idx_<table>_<column>` 给 non-unique
- `idx_<table>_<column>`（GORM 也用这个 prefix 给 unique）

不要用 `uniq_<col1>_<col2>` 这种自定义 composite 名。

---

## 3. Langfuse trace 刚写入时 `/traces/:id` 返 404

### 现象
用户触发 LLM 调用后立即通过 Langfuse REST API 查 trace 详情：

```bash
curl -u "pk-lf-...:sk-lf-..." http://110.42.221.25:3100/api/public/traces/<id>
```

偶发返回 `{"error": "LangfuseNotFoundError", "message": "Trace ... not found
within authorized project"}`，但 `/api/public/traces?userId=...` list 能看到
同一个 trace。

### 根因
Langfuse 的 ingestion 是 **two-stage async**：ClickHouse insert → Postgres
index。新 trace 写入后秒级进入 list endpoint（走 CH），但 `/traces/:id`
单点查询有时要等 index commit。实测 < 1 秒到达 (SOP 完成 →
`/traces/:id` 200 + 4 obs 齐全)。

### 恢复步骤
不用修，**重试**。ops 排障脚本建议：
```bash
for i in 1 2 3; do
  resp=$(curl -s -u "$PK:$SK" ".../api/public/traces/$TID")
  echo "$resp" | grep -q '"LangfuseNotFoundError"' || break
  sleep 5
done
```

### 长期观察
如果 ops 排障时经常碰到 404 需要 5+ 次重试，说明 Langfuse ingestion
不健康，查 Langfuse 部署资源（ClickHouse / Postgres / Redis 容器）。

---

## 4. Dev MySQL root 密码

`Numind2025` —— 从 `docker inspect numind-mysql-dev` 的 env 里能看到，
出于运维便利暂未改。prod 密码通过 secret 管理。

---

## 5. Dev 环境端口速查

| 端口 | 服务 | 容器 |
|------|------|------|
| 9091 | 用户端 API | `numind-server-dev` |
| 9092 | 用户端 pprof / 内部 | `numind-server-dev` |
| 9099 | Admin API | `numind-admin-server-dev` |
| 9100 | Admin web nginx | `numind-admin-web-dev` |
| 9200 | 用户 web nginx | `numind-web-v3-dev` |
| 3306 | MySQL | `numind-mysql-dev` |
| 6379 | Redis | `numind-redis-dev` |

Langfuse 在**另一台服务器** `110.42.221.25:3100`（API key 在
`config_dev.yaml` 的 `langfuse.*` 字段）。

---

*最后更新：2026-04-19（credits-system feature 上线后整理）*
