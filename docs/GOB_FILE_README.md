# GOB 文件说明

## 📌 什么是 GOB 文件？

`.gob` 文件是 **chromem-go 向量数据库的持久化存储文件**，使用 Go 标准库 `encoding/gob` 进行二进制序列化。

## 📂 文件位置

**自动路径计算**:
```
根据 resource.image_path 智能计算:

规则:
1. 如果父目录是 "image"，则向上一级后创建 vector_db
   /opt/numind/dev/image/upload => /opt/numind/dev/vector_db
2. 否则在父目录下创建 vector_db
   /Users/.../res/upload => /Users/.../res/vector_db

示例:
- Dev:  /opt/numind/dev/vector_db/
- QA:   /opt/numind/qa/vector_db/
- Prod: /opt/numind/prod/vector_db/
- Local: /Users/.../res/vector_db/
```

**文件结构**:
```
{base_path}/vector_db/
└── 6e317bcd/              # Collection ID (books集合)
    ├── 00000000.gob       # Collection 元数据 (82B) ⚠️ 必须存在
    ├── 8c52a8e0.gob       # Document 向量文件 (10KB)
    ├── 1944c93e.gob       # Document 向量文件 (6.3KB)
    └── ...                # 其他笔记的向量文件
```

**路径优先级**:
1. `rag.vector_db_path` 配置（如果指定）
2. 基于 `resource.image_path` 自动计算（推荐）
3. 默认路径 `./data/vector_db`（仅开发环境）

## 🔴 重要性级别

**极其重要，绝对不可删除！**

| 影响 | 说明 |
|------|------|
| ❌ 数据完全丢失 | 删除后所有笔记向量数据永久丢失 |
| ❌ RAG 功能失效 | 无法进行语义检索，AI 对话功能不可用 |
| ❌ 无法自动恢复 | 需要重新调用 Embedding API 向量化 |
| ❌ 服务中断 | 恢复期间 RAG 聊天功能完全不可用 |

## 📊 文件内容

**00000000.gob** - Collection 元数据
```
- Collection 名称: "books"
- 配置信息
- 大小: 82B
```

**其他 .gob 文件** - Document 向量数据
```go
{
    ID: "book_469",                    // 文档ID
    Embedding: [0.123, -0.456, ...],   // 1536维向量（约6KB）
    Metadata: {
        "user_id": "2",                // 用户ID
        "book_id": "469",              // 笔记ID
        "content": "笔记完整内容..."    // 笔记原文
    }
}
```

## 💾 当前数据统计

```bash
Collection ID: 6e317bcd
文件数量: 13个
总大小: 172KB
文件大小范围: 82B - 17KB
```

## 📈 容量估算

| 笔记数量 | 预估大小 |
|---------|---------|
| 100篇   | 600KB - 1.7MB |
| 1,000篇 | 6MB - 17MB |
| 10,000篇 | 60MB - 170MB |
| 100,000篇 | 600MB - 1.7GB |

## 🔧 如果误删除怎么办？

### 方案1：从备份恢复（推荐）

```bash
# 1. 停止服务
systemctl stop numind

# 2. 恢复备份
tar -xzf /backup/vector_db_20241124.tar.gz -C ./

# 3. 重启服务
systemctl start numind
```

### 方案2：重新向量化（无备份时）

系统会自动检测并重新向量化所有笔记：

```bash
# 重启服务，自动触发向量化
systemctl restart numind

# 后台自动执行，不阻塞服务
# 根据笔记数量，耗时：
# - 100篇：1-2 分钟
# - 1000篇：5-10 分钟
# - 10000篇：50-100 分钟
```

**费用估算**（重新向量化）：
- 单篇笔记：约 ¥0.0005
- 1000篇笔记：约 ¥0.5
- 10000篇笔记：约 ¥5

## 🐳 容器化环境配置

### Docker Compose 示例

```yaml
version: '3.8'
services:
  numind-dev:
    image: numind-server:dev
    volumes:
      # 挂载持久化目录（包含图片和向量数据库）
      - /data/numind/dev:/opt/numind/dev
    environment:
      - ENV=dev
    # 无需额外配置 vector_db_path，系统会自动计算:
    # /opt/numind/dev/image/upload => /opt/numind/dev/vector_db
```

### 宿主机目录结构

```bash
/data/numind/dev/
├── image/
│   └── upload/          # 图片上传目录（持久化）
└── vector_db/           # 向量数据库目录（持久化） ✅ 自动创建
    └── 6e317bcd/        # Collection
        ├── 00000000.gob
        └── *.gob
```

**优势**:
- ✅ 容器重启/重建数据不丢失
- ✅ 图片和向量数据库统一管理
- ✅ 无需额外配置，自动计算路径
- ✅ 便于备份整个持久化目录

---

## 🛡️ 备份策略

### 每日备份（推荐）

```bash
#!/bin/bash
# 创建备份脚本 backup_vector_db.sh

# 根据环境设置基础路径
ENV=${1:-"dev"}  # 默认 dev 环境
BASE_PATH="/opt/numind/${ENV}"
VECTOR_DB_PATH="${BASE_PATH}/vector_db"
BACKUP_DIR="/backup/vector_db"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)

# 创建备份
tar -czf ${BACKUP_DIR}/vector_db_${ENV}_${TIMESTAMP}.tar.gz ${VECTOR_DB_PATH}

# 保留最近7天的备份
find ${BACKUP_DIR} -name "vector_db_${ENV}_*.tar.gz" -mtime +7 -delete

echo "✅ 备份完成: vector_db_${ENV}_${TIMESTAMP}.tar.gz"
```

### 使用方法

```bash
# 备份 dev 环境
./backup_vector_db.sh dev

# 备份 qa 环境
./backup_vector_db.sh qa

# 备份 prod 环境
./backup_vector_db.sh prod
```

### 设置定时任务

```bash
# 添加到 crontab，每天凌晨2点备份
0 2 * * * /path/to/backup_vector_db.sh >> /var/log/vector_db_backup.log 2>&1
```

## 🔍 监控命令

```bash
# 设置环境变量（根据实际环境调整）
ENV=dev  # 或 qa, prod
VECTOR_DB_PATH="/opt/numind/${ENV}/vector_db"

# 查看向量数据库状态
du -sh ${VECTOR_DB_PATH}
find ${VECTOR_DB_PATH} -name "*.gob" | wc -l

# 查看文件列表
ls -lh ${VECTOR_DB_PATH}/6e317bcd/

# 查看最后修改时间
stat ${VECTOR_DB_PATH}

# 本地开发环境
# VECTOR_DB_PATH="./res/vector_db"
```

## ⚙️ 数据保护措施

```bash
# 1. 限制目录权限（仅所有者可访问）
chmod 700 data/vector_db/

# 2. 添加到 .gitignore（避免误提交到代码仓库）
echo "data/vector_db/" >> .gitignore

# 3. 设置定期备份（见上方）
```

## ❓ 常见问题

**Q: 可以删除 gob 文件吗？**
A: ❌ 不可以！删除会导致所有向量数据丢失，RAG 功能失效。

**Q: gob 文件占用太多空间怎么办？**
A: ✅ 这是正常的。10000篇笔记约占用 60-170MB，属于合理范围。

**Q: 如何迁移到新服务器？**
A: ✅ 直接打包 `data/vector_db/` 目录，在新服务器上解压即可。

**Q: gob 文件损坏了怎么办？**
A: 
1. ✅ 从备份恢复（推荐）
2. ✅ 删除整个目录，重启服务自动重建（有成本）

**Q: 为什么使用 gob 而不是 JSON？**
A: 
- ✅ 更高效（二进制格式，体积小）
- ✅ 更快（序列化/反序列化速度快）
- ✅ 类型安全（保留 Go 类型信息）

## 📚 相关文档

详细信息请参考：[docs/ai-rag-chat.md](./ai-rag-chat.md)
- 第 4.4 节：向量数据库持久化机制
- 第 11.8 节：向量数据库备份策略
- 第 12.3 节：数据安全措施

---

**最后提醒：一定要定期备份！** 🔔
