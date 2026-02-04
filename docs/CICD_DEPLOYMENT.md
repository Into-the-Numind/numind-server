# CICD 自动部署指南（语义切分版）

## 🎯 概述

本文档介绍如何配置 CICD 流水线，实现代码提交后自动部署到服务器，并在部署过程中自动下载语义切分模型。

## 📁 修改的文件

| 文件 | 说明 |
|------|------|
| `Dockerfile` | 添加 Python 依赖和模型预下载 |
| `scripts/docker-entrypoint.sh` | 启动脚本，包含模型检查和下载 |
| `.github/workflows/ci-cd.yaml` | CICD 配置（已有） |

## 🚀 自动部署流程

```
Git Push → 构建镜像 → 推送 Docker Hub → 服务器拉取 → 启动容器 → 检查/下载模型 → 启动应用
```

## 📦 Dockerfile 关键修改

### 1. Python 依赖安装
```dockerfile
RUN pip3 install --no-cache-dir pymupdf python-docx sentence-transformers numpy
```

### 2. 模型预下载（构建时）
```dockerfile
# 预下载语义切分模型（带重试机制）
RUN mkdir -p /app/model_cache && \
    python3 << 'EOF'
# 自动使用 hf-mirror.com 镜像源
# 支持 3 次重试
# 构建失败不会阻止镜像构建
EOF
```

### 3. 启动脚本
```dockerfile
COPY scripts/docker-entrypoint.sh /app/start.sh
```

## 🔧 环境变量配置

在 CICD 中设置以下 Secrets：

| Secret | 说明 |
|--------|------|
| `SSH_PRIVATE_KEY` | 服务器 SSH 私钥 |
| `DEPLOY_USER` | 部署用户名 |
| `DEV_HOST` | 开发服务器地址 |
| `QA_HOST` | 测试服务器地址 |
| `PROD_HOST` | 生产服务器地址 |

### 可选：模型下载配置

如果需要使用其他镜像源，在 CICD 中添加：

```yaml
env:
  HF_ENDPOINT: https://hf-mirror.com  # 中国大陆镜像
```

## 🌐 镜像源配置（中国大陆）

系统会自动检测并使用国内镜像源，无需额外配置。

如需手动设置：

```bash
# 在 Dockerfile 中
ENV HF_ENDPOINT=https://hf-mirror.com

# 或在运行时
export HF_ENDPOINT=https://hf-mirror.com
```

## 📋 部署检查清单

部署完成后，检查以下日志输出：

```
[INFO] 🔍 检查语义切分模型...
[INFO] 🌐 使用镜像源: https://hf-mirror.com
[INFO] ✅ 语义切分模型已就绪

或

[WARN] ⚠️ 模型未找到或文件不完整，尝试下载...
[INFO] 🔄 第 1 次尝试下载模型...
[INFO] ✅ 模型下载成功
```

## 🔍 故障排查

### 问题1：模型下载超时

**症状**：构建时模型下载失败

**解决**：
```bash
# 1. 检查网络连接
# 2. 手动下载模型后复制到服务器
docker exec <container> python3 -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('BAAI/bge-small-zh')"

# 3. 或使用离线模式（模型已预下载到镜像）
```

### 问题2：运行时模型下载失败

**症状**：启动时显示 "模型下载失败"

**解决**：
```bash
# 进入运行中的容器
docker exec -it <container> bash

# 手动下载模型
python3 << 'EOF'
import os
os.environ['HF_ENDPOINT'] = 'https://hf-mirror.com'
from sentence_transformers import SentenceTransformer
model = SentenceTransformer('BAAI/bge-small-zh', cache_folder='/app/model_cache')
print("Done")
EOF

# 重启容器
docker restart <container>
```

### 问题3：容器启动慢

**症状**：容器启动需要几分钟

**原因**：首次启动时在下载模型

**解决**：
- 正常现象，模型约 100MB
- 后续启动将从缓存加载，秒级启动

## 📝 手动部署命令（备用）

如果 CICD 失败，可以手动部署：

```bash
# 1. 登录服务器
ssh root@<server-ip>

# 2. 拉取最新镜像
docker pull neozhang96/numind-server:develop

# 3. 停止旧容器
docker stop numind-server-dev
docker rm numind-server-dev

# 4. 启动新容器
docker run -d \
  --name numind-server-dev \
  -p 9091:9091 \
  -e APP_ENV=dev \
  -v /opt/numind/dev:/opt/numind/dev \
  neozhang96/numind-server:develop

# 5. 查看日志
docker logs -f numind-server-dev
```

## 🔄 回滚方案

如果部署失败，快速回滚：

```bash
# 使用上一个镜像标签
docker pull neozhang96/numind-server:<previous-tag>
docker stop numind-server-dev
docker rm numind-server-dev
docker run -d --name numind-server-dev ...
```

## 📊 模型信息

| 属性 | 值 |
|------|-----|
| 模型名称 | BAAI/bge-small-zh |
| 大小 | ~100MB |
| 下载时间 | 1-5 分钟（取决于网络） |
| 内存占用 | ~500MB |
| 缓存位置 | /app/model_cache |

## ✅ 验证部署成功

检查以下指标：

1. **容器运行状态**
   ```bash
   docker ps | grep numind-server
   ```

2. **健康检查**
   ```bash
   curl http://localhost:9091/healthz
   ```

3. **语义切分可用性**
   ```bash
   # 查看日志中是否有 "语义切分模型已就绪"
   docker logs numind-server-dev | grep "语义切分"
   ```

4. **上传测试文档**
   - 上传一个长文档
   - 检查切片质量
   - 确认主题切分正确

## 🎉 完成！

现在每次 `git push` 到对应分支，系统将自动：
1. 构建包含语义切分依赖的 Docker 镜像
2. 尝试预下载模型到镜像中
3. 部署到对应环境的服务器
4. 启动时检查并下载模型（如需要）
5. 自动回退到规则切分（如果模型不可用）
