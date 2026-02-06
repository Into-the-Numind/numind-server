# 切分问题修复总结

## 🐛 发现的问题

### 问题 1：切分策略不可见
- **症状**：无法知道实际使用了哪种切分策略
- **解决**：添加了 `SPLITTER_DEBUG` 环境变量控制日志输出

### 问题 2：强制切分不优先选择句子边界
- **症状**：在句子中间切断
- **原因**：`findForceSplitPoint` 只考虑分词边界，没有优先考虑句子边界
- **解决**：修改算法，优先寻找句子结束符

## ✅ 已做的修改

### 1. hybrid_splitter.go
```go
// 添加了调试日志
if os.Getenv("SPLITTER_DEBUG") == "1" {
    log.Printf("[HybridSplitter] Text length: %d, Strategy: %s...", ...)
}
```

### 2. enhanced_splitter.go
```go
// 修改 findForceSplitPoint，优先句子边界
// 1. 首先寻找句子结束符
// 2. 其次使用分词边界
// 3. 最后才强制切分
```

### 3. 新增诊断工具
- `scripts/analyze_splitting.go` - 切分分析工具
- `scripts/check_splitting.sh` - 快速检查脚本
- `docs/SPLITTING_DEBUG_GUIDE.md` - 调试指南

## 🔍 如何查看实际切分策略

### 方法 1：开启调试日志

```bash
# 停止当前容器
docker stop numind-server-dev

# 重新启动，启用调试模式
docker run -d \
  --name numind-server-dev \
  -e SPLITTER_DEBUG=1 \
  -e APP_ENV=dev \
  -p 9091:9091 \
  neozhang96/numind-server:develop

# 查看实时日志
docker logs -f numind-server-dev | grep "Splitter"
```

你会看到类似这样的输出：
```
[HybridSplitter] Text length: 3500, Strategy: auto, SemanticAvailable: true
[HybridSplitter] Auto-selected semantic splitting (text > 2000)
[EnhancedSplitter] Section length: 3500, boundaries found: 15
[EnhancedSplitter] Chunk at boundary: pos=980, len=980
[EnhancedSplitter] Forced split: pos=1980, len=1000, next_start="这是新句子的开始..."
```

### 方法 2：使用诊断脚本

```bash
# 在服务器上运行
bash scripts/check_splitting.sh numind-server-dev
```

### 方法 3：检查数据库中的切片

上传文档后，查看切片内容：

```bash
# 进入 MySQL
mysql -u root -p

# 查询切片
USE numind-dev;
SELECT id, LENGTH(content) as len, LEFT(content, 50) as preview 
FROM knowledge_chunks 
WHERE document_id = <你的文档ID>
ORDER BY sequence;
```

## 📊 默认切分策略

### 当前配置（NewDefaultHybridSplitter）

```go
Strategy: StrategyAuto  // 自动选择
SemanticMinLength: 2000  // 超过 2000 字符使用语义切分

RuleConfig:
  MaxChunkSize: 1000
  MinChunkSize: 200
  
SemanticConfig:
  MaxChunkSize: 1000
  MinChunkSize: 100
```

### 策略选择逻辑

```
if Strategy == Auto:
    if 文本长度 > 2000 && 语义切分可用:
        使用语义切分
    else:
        使用规则切分
```

## 🎯 切分边界优先级（已修复）

### 规则切分边界优先级：
1. **Markdown 标题**（# ## ###）
2. **段落边界**（空行）
3. **句子边界**（。！？.!?） ← **修复后优先**
4. **分词边界**（jieba）
5. **强制切分**（最后手段）

## 🚀 部署修复

### 步骤 1：提交修复

```bash
git add internal/numind/biz/salesrag/service/
git add scripts/
git add docs/

git commit -m "fix: 修复切分边界选择，优先句子边界

- 修改 findForceSplitPoint，优先在句子边界切分
- 添加 SPLITTER_DEBUG 环境变量控制日志
- 新增切分诊断工具"

git push origin develop
```

### 步骤 2：等待 CICD 部署

等待约 15 分钟，新镜像构建完成。

### 步骤 3：验证修复

```bash
# 启用调试模式，重启容器
docker stop numind-server-dev
docker rm numind-server-dev
docker run -d \
  --name numind-server-dev \
  -e SPLITTER_DEBUG=1 \
  ...其他参数...
  neozhang96/numind-server:develop

# 上传测试文档，查看日志
docker logs -f numind-server-dev | grep "Splitter"
```

## 📈 预期改善

修复前：
```
Chunk 1: "...这是一个"
Chunk 2: "很长的句子..."
```

修复后：
```
Chunk 1: "...这是一个很长的句子。"
Chunk 2: "这是另一个句子的开始..."
```

## 🆘 如果仍有问题

如果修复后仍然从句子中间切断，请：

1. **收集日志**：
```bash
docker logs numind-server-dev > splitting_logs.txt
```

2. **提供测试样本**：准备一个导致问题的文本文件

3. **查看实际切片**：
```sql
SELECT content FROM knowledge_chunks WHERE document_id = X;
```

联系技术支持进行进一步分析。
