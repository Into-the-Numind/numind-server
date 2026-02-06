# 切分策略诊断指南

## 🔍 如何查看实际使用的切分策略

### 方法一：查看应用日志（推荐）

在服务器上查看容器日志：

```bash
# 实时查看日志
docker logs -f numind-server-dev | grep -E "(Splitter|切分|chunk)"

# 查看最近 100 行
docker logs --tail 100 numind-server-dev | grep -E "(Splitter|切分|chunk)"
```

**开启调试模式**：

修改 docker-compose 或启动命令，添加环境变量：

```yaml
environment:
  - SPLITTER_DEBUG=1
```

或在 docker run 时添加：

```bash
docker run -e SPLITTER_DEBUG=1 ...
```

然后重启容器，你会看到详细的切分日志：

```
[HybridSplitter] Text length: 3500, Strategy: auto, SemanticAvailable: true
[HybridSplitter] Auto-selected semantic splitting (text > 2000)
[EnhancedSplitter] Section length: 1500, boundaries found: 8
[EnhancedSplitter] Chunk at boundary: pos=980, len=980
[EnhancedSplitter] Forced split: pos=1000, len=1000, next_start="这是一个新句子的开始..."
```

---

### 方法二：使用诊断脚本

```bash
# 进入容器
docker exec -it numind-server-dev bash

# 运行诊断工具
cd /app
go run scripts/analyze_splitting.go /path/to/your/text.txt
```

---

### 方法三：通过 API 测试

创建一个测试接口来查看切分详情：

```go
// 在 handler 中添加临时测试接口
func TestSplitHandler(c *gin.Context) {
    text := c.PostForm("text")
    
    splitter := service.NewDefaultHybridSplitter()
    chunks, details, err := splitter.SplitWithDetails(text)
    
    c.JSON(200, gin.H{
        "text_length": len(text),
        "strategy": details["strategy"],
        "semantic_available": details["semantic_available"],
        "auto_selected": details["auto_selected"],
        "chunk_count": len(chunks),
        "chunks": chunks,
    })
}
```

---

## 📊 切分策略选择逻辑

### 默认策略（StrategyAuto）

```
if 文本长度 > 2000 字符 && 语义切分可用:
    使用语义切分 (Embedding)
else:
    使用规则切分 (Enhanced)
```

### 规则切分（StrategyRuleOnly）

边界优先级：
1. **Markdown 标题**（最高优先级）
2. **段落结束**（双换行）
3. **句子结束**（。！？.!? 等）
4. **中文分词边界**（jieba 分词）

### 强制切分（当找不到合适边界时）

新的优先级：
1. **句子边界**（优先）
2. **分词边界**（次之）
3. **直接切分**（最后手段）

---

## 🐛 常见问题排查

### 问题 1：从句子中间切断

**症状**：Chunk 结尾是 "这是一个"，下一个 Chunk 开头是 "测试句子"

**可能原因**：
1. 没有找到合适的句子边界
2. 强制切分点选择不当

**排查方法**：

```bash
# 查看详细日志
SPLITTER_DEBUG=1 docker logs numind-server-dev 2>&1 | grep "Forced split"
```

**解决方案**：
- 增加 MaxChunkSize，给切分器更多空间找边界
- 或者减小 MinChunkSize，允许更小的 chunk

### 问题 2：使用了错误的策略

**症状**：长文本 (>2000) 但没有使用语义切分

**排查**：

```bash
docker logs numind-server-dev | grep "Auto-selected"
# 应该显示: "semantic splitting"
# 如果显示: "rule splitting"，说明语义切分不可用
```

**检查语义切分是否可用**：

```bash
docker exec numind-server-dev python3 -c "from sentence_transformers import SentenceTransformer; print('OK')"
```

### 问题 3：切分块数太少/太多

**调整参数**：

```go
// 在 NewDefaultHybridSplitter 中修改
HybridSplitterConfig{
    RuleConfig: EnhancedSplitterConfig{
        MaxChunkSize: 1500,  // 增大 = 更少的 chunk
        MinChunkSize: 300,   // 增大 = 更大的 chunk
    },
    SemanticMinLength: 1500,  // 降低 = 更多文本使用语义切分
}
```

---

## 🎯 理想的切分效果

### ✅ 好的切分

```
Chunk 1: "...这是一个完整的句子。"
Chunk 2: "这是另一个完整的句子。..."
```

### ❌ 差的切分

```
Chunk 1: "...这是一个"
Chunk 2: "完整的句子。这是另..."
```

---

## 🔧 立即调试

### 步骤 1：开启调试模式

```bash
# 停止容器
docker stop numind-server-dev

# 重新启动，添加调试环境变量
docker run -d \
  --name numind-server-dev \
  -e SPLITTER_DEBUG=1 \
  -e APP_ENV=dev \
  ...其他参数...
  neozhang96/numind-server:develop
```

### 步骤 2：上传测试文档

上传一个中等长度的文档（约 3000 字符），触发切分。

### 步骤 3：查看日志

```bash
# 实时查看
docker logs -f numind-server-dev | grep -E "(Splitter|Chunk|Forced)"
```

### 步骤 4：分析结果

检查日志中：
1. 使用的策略是否正确
2. 是否出现 "Forced split"
3. 切分点是否在句子边界

---

## 📈 性能对比

| 策略 | 适用场景 | 切分质量 | 速度 |
|------|----------|----------|------|
| 规则切分 | 短文本 (<2000) | ⭐⭐⭐ | 快 |
| 语义切分 | 长文本 (>2000) | ⭐⭐⭐⭐⭐ | 慢 |
| 混合策略 | 通用 | ⭐⭐⭐⭐ | 中等 |

---

## 🆘 紧急修复

如果发现切分效果很差，可以立即禁用语义切分：

```bash
# 修改环境变量，强制使用规则切分
docker run -e SPLITTER_STRATEGY=rule_only ...
```

或修改代码：

```go
// splitter_adapter.go
Strategy: StrategyRuleOnly,  // 强制使用规则切分
```

然后重新部署。

---

## 📞 需要帮助？

如果以上方法无法解决问题，请提供：

1. 容器日志：`docker logs numind-server-dev > logs.txt`
2. 测试文档样本
3. 实际切分结果截图

联系技术支持进行分析。
