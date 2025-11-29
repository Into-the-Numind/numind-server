# Numind 日志位置说明文档

## 概述

本文档说明 Numind 服务在服务器上的日志存储位置，包括主服务（numind-server）和后台管理系统（numind-admin）的日志位置。

## 日志类型

Numind 系统包含两种类型的日志：

1. **Docker 容器日志**：由 Docker 引擎管理的容器标准输出日志
2. **应用日志文件**：应用程序写入的日志文件（如 `numind.log`）

## 日志轮转配置

### 主服务（numind-server）

| 环境 | 单个文件最大 | 保留文件数 | 总日志容量 |
|------|------------|----------|-----------|
| Dev  | 10MB       | 3 个     | 30MB      |
| QA   | 10MB       | 3 个     | 30MB      |
| Prod | 20MB       | 5 个     | 100MB     |

### 后台管理系统（numind-admin）

| 环境 | 单个文件最大 | 保留文件数 | 总日志容量 |
|------|------------|----------|-----------|
| Dev  | 10MB       | 3 个     | 30MB      |
| QA   | 10MB       | 3 个     | 30MB      |
| Prod | 10MB       | 3 个     | 30MB      |

## 容器信息

### 主服务（numind-server）

| 环境 | 容器名称 | 宿主机端口 | 容器端口 | 数据挂载目录 |
|------|---------|-----------|---------|------------|
| Dev  | `numind-server-dev` | 9091, 9092 | 9091, 9092 | `/opt/numind/dev` |
| QA   | `numind-server-qa` | 9093, 9094 | 9091, 9092 | `/opt/numind/qa` |
| Prod | `numind-server-prod` | 9095, 9096 | 9091, 9092 | `/opt/numind/prod` |

### 后台管理系统（numind-admin）

| 环境 | 容器名称 | 宿主机端口 | 容器端口 |
|------|---------|-----------|---------|
| Dev  | `numind-admin-server-dev` | 9099 | 9099 |
| QA   | `numind-admin-server-qa` | 9103 | 9099 |
| Prod | `numind-admin-server-prod` | 9104 | 9099 |

## Docker 容器日志位置

### 默认存储位置

Docker 容器日志默认存储在以下位置：

```
/var/lib/docker/containers/<容器ID>/<容器ID>-json.log
```

### 查找日志文件路径

#### 方法 1：使用 docker inspect 命令（推荐）

```bash
# 主服务 - 开发环境
docker inspect -f '{{.LogPath}}' numind-server-dev

# 主服务 - QA 环境
docker inspect -f '{{.LogPath}}' numind-server-qa

# 主服务 - 生产环境
docker inspect -f '{{.LogPath}}' numind-server-prod

# 后台管理系统 - 开发环境
docker inspect -f '{{.LogPath}}' numind-admin-server-dev

# 后台管理系统 - QA 环境
docker inspect -f '{{.LogPath}}' numind-admin-server-qa

# 后台管理系统 - 生产环境
docker inspect -f '{{.LogPath}}' numind-admin-server-prod
```

#### 方法 2：通过容器 ID 查找

```bash
# 获取容器 ID
CONTAINER_ID=$(docker inspect -f '{{.Id}}' numind-server-dev)
echo "容器 ID: $CONTAINER_ID"

# 日志文件路径
LOG_PATH="/var/lib/docker/containers/${CONTAINER_ID}/${CONTAINER_ID}-json.log"
echo "日志文件路径: $LOG_PATH"
```

### 日志文件命名规则

Docker 日志轮转后的文件命名规则：

- 当前日志：`<容器ID>-json.log`
- 轮转日志1：`<容器ID>-json.log.1`
- 轮转日志2：`<容器ID>-json.log.2`
- 轮转日志3：`<容器ID>-json.log.3`
- （生产环境最多到 `.5`）

### 查看日志文件

```bash
# 查看当前日志文件
sudo tail -f /var/lib/docker/containers/<容器ID>/<容器ID>-json.log

# 查看轮转的日志文件
sudo cat /var/lib/docker/containers/<容器ID>/<容器ID>-json.log.1
sudo cat /var/lib/docker/containers/<容器ID>/<容器ID>-json.log.2

# 查看所有日志文件（包括轮转的）
sudo ls -lh /var/lib/docker/containers/<容器ID>/
```

## 应用日志文件位置

### 主服务（numind-server）

应用日志文件 `numind.log` 存储在容器内的工作目录，通过数据卷挂载到宿主机：

#### 开发环境

- **容器内路径**：`/opt/numind/dev/numind.log`（如果应用配置了日志文件路径）
- **宿主机路径**：`/opt/numind/dev/numind.log`（通过卷挂载）

```bash
# 查看应用日志文件
cat /opt/numind/dev/numind.log

# 实时跟踪日志
tail -f /opt/numind/dev/numind.log

# 查看容器内的日志文件
docker exec numind-server-dev ls -lh /opt/numind/dev/numind.log
```

#### QA 环境

- **容器内路径**：`/opt/numind/qa/numind.log`
- **宿主机路径**：`/opt/numind/qa/numind.log`

```bash
# 查看应用日志文件
cat /opt/numind/qa/numind.log

# 实时跟踪日志
tail -f /opt/numind/qa/numind.log
```

#### 生产环境

- **容器内路径**：`/opt/numind/prod/numind.log`
- **宿主机路径**：`/opt/numind/prod/numind.log`

```bash
# 查看应用日志文件
cat /opt/numind/prod/numind.log

# 实时跟踪日志
tail -f /opt/numind/prod/numind.log
```

### 后台管理系统（numind-admin）

后台管理系统的日志文件位置（如果配置了文件输出）：

- **容器内路径**：`/app/logs/`（容器内日志目录）
- **注意**：后台管理系统主要使用标准输出，日志通过 Docker 日志系统管理

## 使用 docker logs 命令查看日志（推荐）

### 主服务（numind-server）

#### 开发环境

```bash
# 查看所有日志
docker logs numind-server-dev

# 查看最后 100 行日志
docker logs --tail 100 numind-server-dev

# 实时跟踪日志（类似 tail -f）
docker logs -f numind-server-dev

# 查看最近 10 分钟的日志
docker logs --since 10m numind-server-dev

# 查看指定时间范围的日志
docker logs --since "2024-01-01T00:00:00" --until "2024-01-01T23:59:59" numind-server-dev

# 查看错误日志
docker logs numind-server-dev 2>&1 | grep -i error
```

#### QA 环境

```bash
# 查看所有日志
docker logs numind-server-qa

# 查看最后 100 行日志
docker logs --tail 100 numind-server-qa

# 实时跟踪日志
docker logs -f numind-server-qa

# 查看最近 1 小时的日志
docker logs --since 1h numind-server-qa
```

#### 生产环境

```bash
# 查看所有日志
docker logs numind-server-prod

# 查看最后 100 行日志
docker logs --tail 100 numind-server-prod

# 实时跟踪日志
docker logs -f numind-server-prod

# 查看最近 1 小时的日志
docker logs --since 1h numind-server-prod
```

### 后台管理系统（numind-admin）

参考 `docs/ADMIN_LOG_GUIDE.md` 文档。

## 日志文件权限

### Docker 容器日志

Docker 容器日志文件通常需要 root 权限访问：

```bash
# 需要 sudo 权限
sudo cat /var/lib/docker/containers/<容器ID>/<容器ID>-json.log
```

### 应用日志文件

应用日志文件通过卷挂载，权限由挂载目录的权限决定：

```bash
# 检查权限
ls -lh /opt/numind/dev/numind.log

# 如果需要修改权限
sudo chown -R 1001:1001 /opt/numind/dev
sudo chmod -R 775 /opt/numind/dev
```

## 日志文件大小检查

### 检查 Docker 日志文件大小

```bash
# 检查主服务开发环境日志大小
docker inspect -f '{{.LogPath}}' numind-server-dev | xargs sudo du -h

# 检查所有容器的日志大小
docker ps --format "{{.Names}}" | while read container; do
    echo "=== $container ==="
    docker inspect -f '{{.LogPath}}' $container | xargs sudo du -h 2>/dev/null || echo "无法访问"
done
```

### 检查应用日志文件大小

```bash
# 检查开发环境应用日志
du -h /opt/numind/dev/numind.log

# 检查所有环境的应用日志
for env in dev qa prod; do
    echo "=== $env 环境 ==="
    if [ -f "/opt/numind/$env/numind.log" ]; then
        du -h /opt/numind/$env/numind.log
    else
        echo "日志文件不存在"
    fi
done
```

## 日志清理

### 清理 Docker 日志

⚠️ **注意**：清理日志前请确保已备份重要日志。

```bash
# 清理特定容器的日志（需要停止容器）
docker stop numind-server-dev
truncate -s 0 $(docker inspect -f '{{.LogPath}}' numind-server-dev)
docker start numind-server-dev

# 清理所有容器的日志（谨慎使用）
docker ps -q | while read container_id; do
    container_name=$(docker inspect -f '{{.Name}}' $container_id | sed 's/\///')
    echo "清理容器: $container_name"
    docker stop $container_id
    truncate -s 0 $(docker inspect -f '{{.LogPath}}' $container_id)
    docker start $container_id
done
```

### 清理应用日志文件

```bash
# 清理开发环境应用日志（保留文件，清空内容）
> /opt/numind/dev/numind.log

# 或者删除并重新创建
rm /opt/numind/dev/numind.log
touch /opt/numind/dev/numind.log
chown 1001:1001 /opt/numind/dev/numind.log
chmod 664 /opt/numind/dev/numind.log
```

## 日志轮转机制

### Docker 日志轮转

Docker 使用 `json-file` 日志驱动，自动进行日志轮转：

- 当日志文件达到 `max-size` 时，自动轮转
- 保留 `max-file` 个历史日志文件
- 自动删除超出保留数量的旧日志文件

### 应用日志轮转

应用日志文件（如 `numind.log`）目前**没有自动轮转机制**，需要：

1. 使用外部工具（如 `logrotate`）进行轮转
2. 或者定期手动清理日志文件

## 快速参考命令

### 查看所有容器日志位置

```bash
# 一键查看所有容器的日志路径和大小
echo "=== 主服务容器 ==="
for container in numind-server-dev numind-server-qa numind-server-prod; do
    if docker ps -a --format "{{.Names}}" | grep -q "^${container}$"; then
        echo "容器: $container"
        docker inspect -f '  日志路径: {{.LogPath}}' $container
        docker inspect -f '{{.LogPath}}' $container | xargs sudo du -h 2>/dev/null || echo "  无法访问"
        echo ""
    fi
done

echo "=== 后台管理系统容器 ==="
for container in numind-admin-server-dev numind-admin-server-qa numind-admin-server-prod; do
    if docker ps -a --format "{{.Names}}" | grep -q "^${container}$"; then
        echo "容器: $container"
        docker inspect -f '  日志路径: {{.LogPath}}' $container
        docker inspect -f '{{.LogPath}}' $container | xargs sudo du -h 2>/dev/null || echo "  无法访问"
        echo ""
    fi
done
```

### 实时监控所有容器日志

```bash
# 同时监控多个容器的日志
docker logs -f numind-server-dev &
docker logs -f numind-server-qa &
docker logs -f numind-server-prod &
wait
```

## 注意事项

1. **权限要求**：访问 Docker 日志文件需要 root 权限
2. **日志格式**：Docker 日志为 JSON 格式，包含时间戳、日志级别等信息
3. **日志轮转**：Docker 自动管理日志轮转，无需手动干预
4. **磁盘空间**：定期检查日志文件大小，避免占用过多磁盘空间
5. **备份重要日志**：在清理日志前，请备份重要的错误日志

## 相关文档

- [后台管理系统日志查看指南](ADMIN_LOG_GUIDE.md)
- [部署检查清单](DEPLOYMENT_CHECKLIST.md)

