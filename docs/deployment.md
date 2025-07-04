# Numind Server 部署指南

## 概述

Numind Server 使用 GitHub Actions 进行 CI/CD 自动化部署，支持多环境部署和滚动更新。

## 环境说明

- **开发环境 (develop)**: 自动部署到开发服务器
- **QA环境 (release)**: 自动部署到QA服务器  
- **生产环境 (product)**: 自动部署到生产服务器

## CI/CD 流程

### 1. 代码质量检查
- 代码编译检查
- 依赖安装验证
- 基础构建测试

### 2. 代码构建
- 多平台二进制文件构建
- 构建产物上传到 GitHub Actions Artifacts

### 3. Docker 镜像构建
- 自动构建 Docker 镜像
- 推送到 GitHub Container Registry (GHCR)
- 支持多标签策略

### 4. 环境部署
- 自动部署到对应环境
- 健康检查验证
- 滚动更新支持

## 部署特性

### 滚动更新
- 自动停止旧容器
- 删除旧容器
- 启动新容器
- 健康检查验证

### 健康检查
- Docker 内置健康检查
- HTTP 端点健康检查 (`/healthz`)
- 双重验证机制

### 错误处理
- 部署失败自动回滚
- 详细的错误日志
- 容器状态监控

## 分支策略

| 分支 | 触发条件 | 部署环境 | 说明 |
|------|----------|----------|------|
| `main` | Push/PR | 无 | 主分支，仅构建测试 |
| `develop` | Push/PR | 开发环境 | 开发测试环境 |
| `release` | Push/PR | QA环境 | 质量保证环境 |
| `product` | Push/PR | 生产环境 | 生产环境 |

## 手动部署

### 使用部署脚本

```bash
# 部署到开发环境
./scripts/deploy.sh develop

# 部署到特定版本
./scripts/deploy.sh v1.0.0

# 回滚到上一个版本
./scripts/deploy.sh --rollback
```

### 环境变量

部署脚本需要以下环境变量：

```bash
export GHCR_TOKEN="your_github_token"
export GITHUB_ACTOR="your_github_username"
```

## 健康检查

### 健康检查端点
- URL: `http://localhost:9091/healthz`
- 方法: GET
- 响应: `{"status": "ok"}`

### 健康检查配置
- 检查间隔: 30秒
- 超时时间: 10秒
- 重试次数: 3次
- 启动等待: 5秒

## 监控和日志

### 容器状态查看
```bash
# 查看容器状态
docker ps -f name=numind-server

# 查看容器日志
docker logs numind-server

# 查看健康状态
docker inspect --format='{{.State.Health.Status}}' numind-server
```

### 服务状态检查
```bash
# HTTP 健康检查
curl -f http://localhost:9091/healthz

# 容器健康检查
docker inspect numind-server | jq '.[0].State.Health'
```

## 故障排除

### 常见问题

1. **容器启动失败**
   ```bash
   # 查看容器日志
   docker logs numind-server
   
   # 检查端口占用
   netstat -tlnp | grep 9091
   ```

2. **健康检查失败**
   ```bash
   # 检查应用是否正常启动
   curl http://localhost:9091/healthz
   
   # 检查容器资源使用
   docker stats numind-server
   ```

3. **镜像拉取失败**
   ```bash
   # 重新登录 GHCR
   echo $GHCR_TOKEN | docker login ghcr.io -u $GITHUB_ACTOR --password-stdin
   
   # 清理 Docker 缓存
   docker system prune -f
   ```

### 回滚操作

```bash
# 自动回滚
./scripts/deploy.sh --rollback

# 手动回滚到指定版本
docker stop numind-server
docker rm numind-server
docker run -d --name numind-server -p 9091:9091 --restart always \
  ghcr.io/into-the-numind/numind-server:previous-tag -c /app/config_dev.yaml
```

## 安全考虑

1. **密钥管理**: 使用 GitHub Secrets 存储敏感信息
2. **权限控制**: 最小权限原则，仅授予必要权限
3. **网络安全**: 使用 SSH 密钥认证，避免密码明文传输
4. **镜像安全**: 使用非 root 用户运行容器

## 性能优化

1. **缓存策略**: 使用 GitHub Actions 缓存加速构建
2. **并行构建**: 多阶段构建减少镜像大小
3. **健康检查**: 合理的检查间隔避免资源浪费
4. **资源限制**: 设置容器资源限制防止资源耗尽

## 更新日志

### v1.0.0
- 初始版本发布
- 基础 CI/CD 流程
- 多环境部署支持

### v1.1.0
- 添加滚动更新功能
- 改进健康检查机制
- 增强错误处理和回滚
- 优化部署脚本 