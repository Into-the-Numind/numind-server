# 🚀 一键部署指南（非技术版）

## ✅ 部署前检查清单

确保以下文件已修改并提交到 Git：

- [x] `Dockerfile` - 已添加 Python 依赖和模型下载
- [x] `scripts/docker-entrypoint.sh` - 新的启动脚本
- [x] `.github/workflows/ci-cd.yaml` - CICD 配置（已有）

## 🎯 部署步骤

### 第 1 步：提交代码

```bash
# 添加所有修改的文件
git add Dockerfile scripts/docker-entrypoint.sh

# 提交
git commit -m "添加语义切分自动部署支持"

# 推送到远程（自动触发部署）
git push origin develop  # 开发环境
git push origin release  # 测试环境
git push origin v1.x.x   # 生产环境（tag）
```

### 第 2 步：等待 CICD 完成

推送后，GitHub Actions 会自动运行：

1. 构建 Docker 镜像（约 5-10 分钟）
2. 推送镜像到 Docker Hub
3. 部署到服务器
4. 自动下载模型

在 GitHub 仓库页面 → Actions 可以查看进度。

### 第 3 步：验证部署

SSH 登录服务器，运行检查脚本：

```bash
# 开发环境
bash /opt/numind/check_deployment.sh -e dev

# 测试环境
bash /opt/numind/check_deployment.sh -e qa

# 生产环境
bash /opt/numind/check_deployment.sh -e prod
```

或使用 Docker 命令检查：

```bash
# 查看容器状态
docker ps | grep numind

# 查看日志（看是否有 "模型已就绪"）
docker logs numind-server-dev | grep -E "(模型|语义|切分)"
```

## 🔍 常见问题

### Q1: 部署后模型没有自动下载？

**正常现象**。模型下载在首次启动时进行：

- 构建时：尝试预下载（可能因网络失败）
- 启动时：自动检查并下载

等待 1-5 分钟，然后检查日志：
```bash
docker logs numind-server-dev | tail -50
```

### Q2: 如何确认语义切分已启用？

检查日志中出现：
```
✅ 语义切分模型已就绪
```

或上传一个长文档测试切片质量。

### Q3: 模型下载太慢/失败？

系统已配置国内镜像源（hf-mirror.com），如果仍然失败：

```bash
# 进入容器手动下载
docker exec -it numind-server-dev bash
python3 << 'EOF'
from sentence_transformers import SentenceTransformer
model = SentenceTransformer('BAAI/bge-small-zh', cache_folder='/app/model_cache')
print("Done")
EOF
exit

# 重启容器
docker restart numind-server-dev
```

### Q4: 如何关闭语义切分？

如果出现问题，系统会自动回退到规则切分。如需完全禁用，修改代码：

```go
// 在初始化切分器的地方
splitter := service.NewHybridSplitter(service.HybridSplitterConfig{
    Strategy: service.StrategyRuleOnly,  // 只使用规则切分
})
```

## 📊 部署成功标志

容器启动后，日志应显示：

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

## 🆘 紧急回滚

如果部署后出现问题：

```bash
# 1. 查看历史镜像
docker images neozhang96/numind-server

# 2. 使用上一个版本
docker stop numind-server-dev
docker rm numind-server-dev
docker run -d \
  --name numind-server-dev \
  -p 9091:9091 \
  -e APP_ENV=dev \
  neozhang96/numind-server:<上一个tag>
```

## 📞 需要帮助？

如果部署遇到问题：

1. 查看 CICD 日志（GitHub → Actions）
2. 查看服务器日志：`docker logs numind-server-dev`
3. 运行检查脚本：`bash scripts/check_deployment.sh -e dev`
4. 联系技术支持

## 🎉 恭喜！

完成以上步骤后，你的系统将：
- ✅ 自动部署到服务器
- ✅ 自动下载语义切分模型
- ✅ 使用混合切分策略（规则+语义）
- ✅ 智能识别文档主题边界

现在可以上传文档测试新的切分效果了！
