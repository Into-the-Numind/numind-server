# 语义切分 CICD 部署 - 修改总结

## 📦 新增/修改的文件

### 1. Dockerfile（已修改）
**位置**: `/Users/zhiyuchen/Desktop/莫小派/Codes/numind-server/Dockerfile`

**修改内容**:
- ✅ 安装 `sentence-transformers` 和 `numpy`
- ✅ 预下载 bge-small-zh 模型（带重试机制）
- ✅ 设置模型缓存目录权限
- ✅ 使用新的启动脚本

**关键代码**:
```dockerfile
# 安装 Python 增强解析依赖 + 语义切分依赖
RUN pip3 install --no-cache-dir pymupdf python-docx sentence-transformers numpy

# 预下载语义切分模型（带重试机制，使用国内镜像）
RUN mkdir -p /app/model_cache && \
    python3 << 'EOF'
import os
os.environ['HF_ENDPOINT'] = 'https://hf-mirror.com'
from sentence_transformers import SentenceTransformer
model = SentenceTransformer('BAAI/bge-small-zh', cache_folder='/app/model_cache')
EOF

# 复制新的启动脚本
COPY scripts/docker-entrypoint.sh /app/start.sh
```

### 2. scripts/docker-entrypoint.sh（新增）
**位置**: `/Users/zhiyuchen/Desktop/莫小派/Codes/numind-server/scripts/docker-entrypoint.sh`

**功能**:
- 🔍 检查 Python 依赖
- 📥 检查并下载语义切分模型
- 🌐 自动使用国内镜像源
- 🔄 支持重试机制
- ⚠️ 失败时友好提示（不阻止启动）

**启动流程**:
```
显示系统信息 → 检查依赖 → 检查/下载模型 → 选择配置 → 启动应用
```

### 3. scripts/check_deployment.sh（新增）
**位置**: `/Users/zhiyuchen/Desktop/莫小派/Codes/numind-server/scripts/check_deployment.sh`

**用途**: 在服务器上检查部署状态

**检查项目**:
1. 容器运行状态
2. 健康检查
3. Python 依赖
4. 语义切分模型
5. 近期日志

**使用方法**:
```bash
bash scripts/check_deployment.sh -e dev
bash scripts/check_deployment.sh -e prod
```

### 4. CI/CD 配置（已有，无需修改）
**位置**: `.github/workflows/ci-cd.yaml`

**说明**: 现有配置已支持自动部署，无需修改。

## 🚀 部署流程

```
┌─────────────┐     ┌──────────────┐     ┌─────────────────┐
│  Git Push   │────▶│  Build Image │────▶│  Pre-download   │
│  (触发)     │     │  (Dockerfile)│     │  Model          │
└─────────────┘     └──────────────┘     └─────────────────┘
                                                  │
                          ┌───────────────────────┘
                          ▼
                   ┌──────────────┐
                   │  Push to     │
                   │  Docker Hub  │
                   └──────────────┘
                          │
                          ▼
                   ┌──────────────┐
                   │  Deploy to   │
                   │  Server      │
                   └──────────────┘
                          │
                          ▼
                   ┌──────────────┐
                   │  Check/      │
                   │  Download    │
                   │  Model       │
                   └──────────────┘
                          │
                          ▼
                   ┌──────────────┐
                   │  Start App   │
                   └──────────────┘
```

## 🌐 镜像源配置

系统已自动配置国内镜像源，无需手动设置：

```bash
# Dockerfile 中已设置
ENV HF_ENDPOINT=https://hf-mirror.com
```

如需要其他镜像源，修改 Dockerfile：
```bash
# ModelScope 镜像
ENV HF_ENDPOINT=https://www.modelscope.cn
```

## 📊 模型信息

| 项目 | 详情 |
|------|------|
| 模型名称 | BAAI/bge-small-zh |
| 大小 | ~100MB |
| 下载时间 | 1-5 分钟 |
| 缓存位置 | /app/model_cache |
| 内存占用 | ~500MB |

## ✅ 验证清单

部署完成后，检查以下内容：

### 1. 构建日志
```
📥 预下载 bge-small-zh 模型...
🌐 使用镜像源: https://hf-mirror.com
🔄 第 1 次尝试下载模型...
✅ 模型预下载成功!
```

### 2. 启动日志
```
🔍 检查语义切分模型...
✅ 语义切分模型已就绪
🟢 启动应用...
```

### 3. 功能测试
- 上传一个长文档（>2000 字符）
- 检查切片是否在主题边界处切分
- 验证切片质量

## 🔧 故障排查

### 构建时模型下载失败
**症状**: 构建日志显示 "模型预下载失败"

**解决**: 
- 正常现象，系统将在运行时重新尝试下载
- 检查网络连接
- 手动下载后复制到服务器

### 运行时模型下载失败
**症状**: 启动日志显示 "模型下载失败，将使用规则切分"

**解决**:
```bash
# 进入容器手动下载
docker exec -it numind-server-dev bash
python3 -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('BAAI/bge-small-zh')"
docker restart numind-server-dev
```

### 容器启动慢
**症状**: 容器启动需要几分钟

**原因**: 首次启动时在下载模型

**解决**: 等待即可，后续启动将从缓存加载

## 📝 下一步操作

1. **提交代码**
   ```bash
   git add Dockerfile scripts/
   git commit -m "添加语义切分自动部署支持"
   git push origin develop
   ```

2. **等待 CICD**
   - 查看 GitHub Actions 进度
   - 构建时间约 10-15 分钟（包含模型下载）

3. **验证部署**
   ```bash
   # SSH 到服务器
   bash scripts/check_deployment.sh -e dev
   ```

4. **测试功能**
   - 上传测试文档
   - 检查切片质量

## 🎉 完成！

完成以上步骤后，你的系统将具备：
- ✅ 自动部署能力
- ✅ 语义切分功能
- ✅ 智能文档切片
- ✅ 失败自动回退

如有问题，参考 `docs/DEPLOYMENT_SIMPLE.md` 故障排查部分。
