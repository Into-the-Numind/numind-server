# Git 提交检查清单

## ✅ 提交前检查

在运行 `git push` 之前，确认以下文件已正确修改：

### 1. 检查 Dockerfile
```bash
git diff Dockerfile
```

确认包含以下内容：
- [ ] `sentence-transformers numpy` 在 pip install 命令中
- [ ] 预下载模型的代码块
- [ ] `COPY scripts/docker-entrypoint.sh /app/start.sh`

### 2. 检查启动脚本存在
```bash
ls -la scripts/docker-entrypoint.sh
ls -la scripts/check_deployment.sh
```

确认两个脚本都有执行权限：
```bash
chmod +x scripts/docker-entrypoint.sh
chmod +x scripts/check_deployment.sh
```

### 3. 检查 Go 代码编译
```bash
cd /Users/zhiyuchen/Desktop/莫小派/Codes/numind-server
go build ./...
```

确认没有编译错误。

## 🚀 提交命令

```bash
# 1. 添加修改的文件
git add Dockerfile
git add scripts/docker-entrypoint.sh
git add scripts/check_deployment.sh
git add docs/*.md  # 文档文件

# 2. 提交（使用清晰的提交信息）
git commit -m "feat: 添加语义切分自动部署支持

- 在 Dockerfile 中安装 sentence-transformers 和 numpy
- 预下载 bge-small-zh 模型（带重试机制）
- 创建 docker-entrypoint.sh 启动脚本
- 添加部署检查脚本
- 配置国内镜像源 (hf-mirror.com)
- 支持构建时和运行时模型下载"

# 3. 推送到远程（触发 CICD）
# 开发环境
git push origin develop

# 测试环境
git push origin release

# 生产环境
git tag v1.x.x  # 替换为实际版本号
git push origin v1.x.x
```

## ⏱️ 预期时间

| 阶段 | 时间 |
|------|------|
| GitHub Actions 排队 | 0-2 分钟 |
| Docker 镜像构建 | 8-15 分钟 |
| 模型预下载 | 2-5 分钟 |
| 推送到 Docker Hub | 1-3 分钟 |
| 部署到服务器 | 1-2 分钟 |
| **总计** | **12-27 分钟** |

## 🔍 部署后验证

### 方法 1：使用检查脚本（推荐）

```bash
# SSH 到服务器后运行
bash /path/to/check_deployment.sh -e dev
```

### 方法 2：手动检查

```bash
# 1. 查看容器状态
docker ps | grep numind

# 2. 查看日志
docker logs numind-server-dev | tail -50

# 3. 检查模型
docker exec numind-server-dev ls -la /app/model_cache/sentence_transformers/

# 4. 健康检查
curl http://localhost:9091/healthz
```

## ✅ 成功标志

在日志中看到这些信息表示部署成功：

```
🚀 启动 Numind Server...
📊 系统信息:
   环境: dev
   Python: Python 3.10.x
   模型缓存: /app/model_cache

🔍 检查语义切分模型...
🌐 使用镜像源: https://hf-mirror.com
✅ 语义切分模型已就绪

📄 使用配置文件: /app/config_dev.yaml
🟢 启动应用...
```

## 🆘 如果失败

### CICD 构建失败

1. 查看 GitHub Actions 日志
2. 检查 Dockerfile 语法
3. 确认脚本有执行权限

### 模型下载失败

1. 检查网络连接
2. 查看容器日志：`docker logs numind-server-dev`
3. 手动下载模型（见 DEPLOYMENT_SIMPLE.md）

### 应用无法启动

1. 检查配置文件是否存在
2. 检查端口是否被占用
3. 查看详细日志：`docker logs numind-server-dev`

## 📞 回滚

如果部署失败，快速回滚到上一个版本：

```bash
# 查看历史镜像
docker images neozhang96/numind-server

# 停止当前容器
docker stop numind-server-dev
docker rm numind-server-dev

# 使用上一个镜像版本
docker run -d \
  --name numind-server-dev \
  -p 9091:9091 \
  -e APP_ENV=dev \
  neozhang96/numind-server:<上一个tag>
```

## 📝 备注

- 首次部署模型下载可能需要 5-10 分钟
- 模型只会下载一次，后续从缓存加载
- 如果模型下载失败，系统会自动回退到规则切分
- 生产环境建议先在开发/测试环境验证
