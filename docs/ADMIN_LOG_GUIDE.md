# Numind Admin 日志查看指南

## 概述

Numind Admin 后台管理系统在 dev、qa、prod 三个环境中运行，所有日志都通过 Docker 容器管理。本文档说明如何查看和管理这些日志。

## 日志轮转配置

所有环境的容器都配置了日志轮转，防止日志文件无限增长：

- **单个日志文件最大大小**: 10MB
- **保留的日志文件数量**: 3 个
- **日志格式**: JSON（Docker 默认格式）

当单个日志文件达到 10MB 时，Docker 会自动轮转，保留最新的 3 个日志文件，自动删除旧文件。

## 容器信息

| 环境 | 容器名称 | 宿主机端口 | 容器端口 |
|------|---------|-----------|---------|
| Dev | `numind-admin-server-dev` | 9099 | 9099 |
| QA | `numind-admin-server-qa` | 9103 | 9099 |
| Prod | `numind-admin-server-prod` | 9104 | 9099 |

## 查看日志的方法

### 1. 使用 docker logs 命令（推荐）

这是最简单和常用的方法，可以实时查看容器日志。

#### 查看开发环境日志

```bash
# 查看所有日志
docker logs numind-admin-server-dev

# 查看最后 100 行日志
docker logs --tail 100 numind-admin-server-dev

# 实时跟踪日志（类似 tail -f）
docker logs -f numind-admin-server-dev

# 查看最近 10 分钟的日志
docker logs --since 10m numind-admin-server-dev

# 查看指定时间范围的日志
docker logs --since "2024-01-01T00:00:00" --until "2024-01-01T23:59:59" numind-admin-server-dev
```

#### 查看 QA 环境日志

```bash
# 查看所有日志
docker logs numind-admin-server-qa

# 查看最后 100 行日志
docker logs --tail 100 numind-admin-server-qa

# 实时跟踪日志
docker logs -f numind-admin-server-qa

# 查看最近 1 小时的日志
docker logs --since 1h numind-admin-server-qa
```

#### 查看生产环境日志

```bash
# 查看所有日志
docker logs numind-admin-server-prod

# 查看最后 100 行日志
docker logs --tail 100 numind-admin-server-prod

# 实时跟踪日志
docker logs -f numind-admin-server-prod

# 查看最近 1 小时的日志
docker logs --since 1h numind-admin-server-prod
```

### 2. 直接查看日志文件

Docker 将日志存储在宿主机文件系统中，可以直接访问日志文件。

#### 查找日志文件位置

```bash
# 获取容器 ID
CONTAINER_ID=$(docker inspect -f '{{.Id}}' numind-admin-server-dev)
echo "容器 ID: $CONTAINER_ID"

# 查看日志文件路径
docker inspect -f '{{.LogPath}}' numind-admin-server-dev
```

#### 查看日志文件

```bash
# 开发环境日志文件路径（示例）
# /var/lib/docker/containers/<container-id>/<container-id>-json.log

# 使用 cat 查看（注意：文件可能很大）
cat /var/lib/docker/containers/<container-id>/<container-id>-json.log

# 使用 tail 查看最后几行
tail -n 100 /var/lib/docker/containers/<container-id>/<container-id>-json.log

# 使用 less 分页查看
less /var/lib/docker/containers/<container-id>/<container-id>-json.log

# 使用 grep 搜索特定内容
grep "ERROR" /var/lib/docker/containers/<container-id>/<container-id>-json.log
```

### 3. 使用 docker-compose logs（如果使用 docker-compose）

如果使用 docker-compose 管理容器：

```bash
# 查看所有服务日志
docker-compose logs

# 查看特定服务日志
docker-compose logs numind-admin-server-dev

# 实时跟踪日志
docker-compose logs -f numind-admin-server-dev

# 查看最后 100 行
docker-compose logs --tail 100 numind-admin-server-dev
```

## 常用日志查看场景

### 查看错误日志

```bash
# 查看所有环境的错误日志
docker logs numind-admin-server-dev 2>&1 | grep -i error
docker logs numind-admin-server-qa 2>&1 | grep -i error
docker logs numind-admin-server-prod 2>&1 | grep -i error

# 查看最近的错误（最后 1000 行中）
docker logs --tail 1000 numind-admin-server-dev 2>&1 | grep -i error
```

### 查看启动日志

```bash
# 查看容器启动时的日志
docker logs numind-admin-server-dev 2>&1 | head -n 50
```

### 实时监控日志

```bash
# 实时监控开发环境日志
docker logs -f numind-admin-server-dev

# 实时监控并过滤错误
docker logs -f numind-admin-server-dev 2>&1 | grep -i error

# 实时监控并高亮显示
docker logs -f numind-admin-server-dev 2>&1 | grep --color=always -E "ERROR|WARN|INFO"
```

### 导出日志到文件

```bash
# 导出开发环境日志到文件
docker logs numind-admin-server-dev > admin-dev-$(date +%Y%m%d-%H%M%S).log

# 导出最近 1 小时的日志
docker logs --since 1h numind-admin-server-dev > admin-dev-recent.log

# 导出错误日志
docker logs numind-admin-server-dev 2>&1 | grep -i error > admin-dev-errors.log
```

### 查看日志统计信息

```bash
# 查看日志文件大小
docker inspect -f '{{.LogPath}}' numind-admin-server-dev | xargs ls -lh

# 查看日志行数
docker logs numind-admin-server-dev 2>&1 | wc -l

# 查看日志中的关键词统计
docker logs numind-admin-server-dev 2>&1 | grep -o "ERROR" | wc -l
```

## 日志轮转说明

### 轮转机制

- 当日志文件达到 10MB 时，Docker 会自动创建新文件
- 保留最新的 3 个日志文件
- 旧文件会自动删除

### 日志文件命名

Docker 日志文件命名格式：
```
<container-id>-json.log
<container-id>-json.log.1
<container-id>-json.log.2
```

### 手动清理日志

如果需要手动清理日志（不推荐，Docker 会自动管理）：

```bash
# 清理所有未使用的容器日志（谨慎使用）
docker system prune -a --volumes

# 清理特定容器的日志（需要停止容器）
docker stop numind-admin-server-dev
truncate -s 0 $(docker inspect -f '{{.LogPath}}' numind-admin-server-dev)
docker start numind-admin-server-dev
```

## 日志格式

Docker 默认使用 JSON 格式存储日志，每条日志包含：

```json
{
  "log": "日志内容\n",
  "stream": "stdout",
  "time": "2024-01-01T00:00:00.000000000Z"
}
```

### 解析 JSON 日志

如果需要解析 JSON 格式的日志：

```bash
# 使用 jq 解析日志
docker logs numind-admin-server-dev 2>&1 | jq -r '.log'

# 提取特定时间段的日志
docker logs numind-admin-server-dev 2>&1 | jq 'select(.time > "2024-01-01T00:00:00Z")'
```

## 故障排查

### 容器日志为空

如果 `docker logs` 返回空内容：

```bash
# 检查容器是否正在运行
docker ps -a | grep numind-admin-server

# 检查容器状态
docker inspect numind-admin-server-dev | grep -A 10 "State"

# 检查容器是否正常启动
docker logs numind-admin-server-dev 2>&1 | tail -n 50
```

### 日志文件过大

如果发现日志文件过大（超过预期）：

```bash
# 检查日志文件大小
docker inspect -f '{{.LogPath}}' numind-admin-server-dev | xargs ls -lh

# 检查日志轮转配置
docker inspect numind-admin-server-dev | grep -A 5 "LogConfig"

# 如果轮转未生效，检查 Docker daemon 配置
cat /etc/docker/daemon.json
```

### 无法查看日志

如果无法查看日志：

```bash
# 检查容器是否存在
docker ps -a | grep numind-admin-server

# 检查 Docker 服务状态
systemctl status docker

# 检查日志文件权限
ls -l $(docker inspect -f '{{.LogPath}}' numind-admin-server-dev)
```

## 最佳实践

1. **定期检查日志**: 建议每天检查一次日志，及时发现问题
2. **监控错误日志**: 设置监控脚本，自动检测错误日志
3. **日志备份**: 重要日志建议导出备份
4. **日志分析**: 使用日志分析工具（如 ELK、Loki）进行集中管理
5. **不要手动删除日志**: 让 Docker 自动管理日志轮转

## 快速参考

```bash
# 查看所有环境的最新日志
docker logs --tail 50 numind-admin-server-dev
docker logs --tail 50 numind-admin-server-qa
docker logs --tail 50 numind-admin-server-prod

# 实时监控所有环境
docker logs -f numind-admin-server-dev &
docker logs -f numind-admin-server-qa &
docker logs -f numind-admin-server-prod &

# 搜索所有环境的错误
for env in dev qa prod; do
  echo "=== $env 环境错误日志 ==="
  docker logs numind-admin-server-$env 2>&1 | grep -i error | tail -n 10
done
```

## 相关文档

- [Docker 日志驱动文档](https://docs.docker.com/config/containers/logging/)
- [Numind Admin API 文档](./NUMIND_ADMIN_API.md)
- [容器部署指南](./CONTAINER_DEPLOYMENT_GUIDE.md)

