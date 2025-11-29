# 向量数据库持久化迁移指南

## 📌 变更说明

**旧方式**:
```yaml
# 固定相对路径（容器重启数据丢失）
rag:
  vector_db_path: "./data/vector_db"
```

**新方式**:
```yaml
# 基于 resource.image_path 自动计算（持久化）
resource:
  image_path: "/opt/numind/dev/image/upload"
# 系统自动计算: /opt/numind/dev/vector_db
```

---

## 🔄 迁移步骤

### 1. 本地开发环境

**无需迁移**，系统会自动处理：

```bash
# 启动服务时会自动创建新路径
# 旧路径: ./data/vector_db
# 新路径: /Users/.../res/vector_db

# 如果需要迁移现有数据
mv data/vector_db/* res/vector_db/
```

### 2. 容器化环境（Dev/QA/Prod）

#### 2.1 更新 Docker Compose

```yaml
version: '3.8'
services:
  numind-dev:
    image: numind-server:dev
    volumes:
      # 持久化挂载（包含图片和向量数据库）
      - /data/numind/dev:/opt/numind/dev
    environment:
      - ENV=dev
```

#### 2.2 创建目录结构

```bash
# 在宿主机创建目录
mkdir -p /data/numind/dev/image/upload
mkdir -p /data/numind/dev/vector_db

# 设置权限
chown -R 1000:1000 /data/numind/dev
chmod -R 755 /data/numind/dev
```

#### 2.3 迁移现有数据（如有）

```bash
# 如果容器内有旧的向量数据
docker cp numind-dev:/path/to/old/vector_db /data/numind/dev/vector_db
```

#### 2.4 重启服务

```bash
# 停止旧容器
docker-compose down

# 启动新容器
docker-compose up -d

# 检查日志
docker-compose logs -f | grep "vector_db_path"
```

---

## ✅ 验证

### 检查路径

```bash
# 本地环境
ls -lh /Users/.../res/vector_db/

# 容器环境（宿主机）
ls -lh /data/numind/dev/vector_db/

# 容器环境（容器内）
docker exec numind-dev ls -lh /opt/numind/dev/vector_db/
```

### 检查日志

启动时应该看到：

```
使用基于 resource.image_path 计算的向量数据库路径
  image_path: /opt/numind/dev/image/upload
  vector_db_path: /opt/numind/dev/vector_db
```

### 测试功能

```bash
# 测试 RAG 聊天
curl -X POST "https://youshu.asia/dev/api/rag/chat" \
  -H "Authorization: Bearer TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"question":"测试","book_ids":[469]}'
```

---

## 🐛 故障排除

### 问题1：容器内找不到向量数据库

**原因**: 挂载路径不正确

**解决**:
```bash
# 检查挂载
docker inspect numind-dev | grep Mounts -A 10

# 确认路径
docker exec numind-dev ls -lh /opt/numind/dev/
```

### 问题2：权限错误

**原因**: 容器用户无权限写入

**解决**:
```bash
# 检查容器用户
docker exec numind-dev whoami

# 修改宿主机权限
sudo chown -R 1000:1000 /data/numind/dev
```

### 问题3：路径计算错误

**原因**: resource.image_path 配置不正确

**解决**:
```yaml
# 确保配置正确
resource:
  image_path: "/opt/numind/dev/image/upload"  # 必须是绝对路径
```

---

## 📊 回滚方案

如果新方案有问题，可以临时回滚：

```yaml
# 显式指定旧路径
rag:
  vector_db_path: "./data/vector_db"
```

但**不推荐**，因为容器重启数据会丢失。

---

## 📚 相关文档

- [GOB_FILE_README.md](docs/GOB_FILE_README.md) - GOB 文件详细说明
- [ai-rag-chat.md](docs/ai-rag-chat.md) - RAG 聊天架构文档

