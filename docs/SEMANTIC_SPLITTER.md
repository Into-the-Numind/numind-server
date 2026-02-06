# 语义切分器 (Semantic Splitter)

## 概述

语义切分器使用 **BAAI/bge-small-zh** 中文 Embedding 模型，通过计算句子之间的语义相似度来检测主题切换点（语义断崖），实现更智能的文档切分。

## 工作原理

1. **句子切分**：将文本按中英文标点切分为句子
2. **Embedding 计算**：使用 bge-small-zh 模型计算每个句子的向量表示
3. **相似度计算**：计算相邻句子的余弦相似度
4. **边界检测**：在相似度低于阈值的位置切分（主题切换点）
5. **动态阈值**：根据文本整体的相似度分布自动调整阈值

## 切分策略

系统提供四种切分策略：

| 策略 | 描述 | 适用场景 |
|------|------|----------|
| `RuleOnly` | 仅使用规则切分 | 短文本、追求速度 |
| `SemanticOnly` | 仅使用语义切分 | 长文档、高质量要求 |
| `Hybrid` | 规则 + 语义优化 | 复杂文档结构 |
| `Auto` | 自动选择（默认） | 短文本用规则，长文本用语义 |

### Auto 策略阈值

- 文本长度 < 2000 字符：使用规则切分
- 文本长度 ≥ 2000 字符：使用语义切分（如果可用）

## 配置参数

### EmbeddingSplitterConfig

```go
type EmbeddingSplitterConfig struct {
    Threshold    float64 // 相似度阈值（默认 0.6）
    MinChunkSize int     // 最小切片大小（默认 100）
    MaxChunkSize int     // 最大切片大小（默认 1000）
    OverlapSize  int     // 重叠大小（默认 100）
}
```

### 阈值说明

- **Threshold（0.6）**：相邻句子相似度低于此值认为是主题切换
  - 值越高（如 0.8）：切分更严格，产生更多小块
  - 值越低（如 0.4）：切分更宽松，产生更少大块

## 依赖安装

### 系统要求

- Python 3.8+
- sentence-transformers 库

### 安装命令

```bash
# 方式1：使用安装脚本
bash scripts/install_semantic_deps.sh

# 方式2：手动安装
pip3 install sentence-transformers
```

### 模型下载

首次使用时会自动从 HuggingFace 下载 `BAAI/bge-small-zh` 模型（约 100MB）。

如网络受限，可手动下载后放入 `~/.cache/torch/sentence_transformers/` 目录。

## 使用方法

### 基础使用

```go
import "numind-server/internal/numind/biz/salesrag/service"

// 创建混合切分器（推荐）
splitter := service.NewDefaultHybridSplitter()

// 切分文本
chunks, err := splitter.Split(text)
if err != nil {
    log.Fatal(err)
}

fmt.Printf("切分为 %d 个片段\n", len(chunks))
```

### 高级配置

```go
splitter := service.NewHybridSplitter(service.HybridSplitterConfig{
    RuleConfig: service.EnhancedSplitterConfig{
        MaxChunkSize:    1000,
        MinChunkSize:    200,
        OverlapSize:     100,
        EnableJieba:     true,
        ProtectMarkdown: true,
    },
    SemanticConfig: service.EmbeddingSplitterConfig{
        Threshold:    0.6,
        MinChunkSize: 100,
        MaxChunkSize: 1000,
        OverlapSize:  100,
    },
    Strategy:          service.StrategyAuto,
    SemanticMinLength: 2000,
})
```

### 获取详细信息

```go
chunks, details, err := splitter.SplitWithDetails(text)
fmt.Printf("策略: %s\n", details["strategy"])
fmt.Printf("是否使用语义切分: %v\n", details["semantic_available"])
```

## 性能对比

| 切分方式 | 速度 | 质量 | 适用场景 |
|----------|------|------|----------|
| 规则切分 | <10ms | ⭐⭐⭐ | 简单文档、短文本 |
| 语义切分 | ~100ms | ⭐⭐⭐⭐⭐ | 复杂文档、高要求 |
| 混合切分 | 智能选择 | ⭐⭐⭐⭐ | 通用场景（推荐） |

*注：语义切分首次加载模型需要 ~2-3 秒，后续切分更快。*

## 回退机制

当语义切分不可用时（Python/模型未安装），系统会自动回退到规则切分，确保服务可用性：

```go
// 检查语义切分是否可用
if !splitter.IsSemanticAvailable() {
    log.Println("语义切分不可用，已回退到规则切分")
}
```

## 切分效果示例

### 输入文本（产品说明 + 价格 + 技术支持）

```
我们的智能销售助手可以帮助您管理客户关系。
它能够自动分析客户行为并提供个性化建议。

月付价格是99元，年付可享受8折优惠。
企业版支持私有化部署和定制开发。

技术架构基于大语言模型和向量数据库。
客服团队提供7x24小时技术支持。
```

### 语义切分结果

```
Chunk 1: 产品介绍（管理客户、个性化建议）
Chunk 2: 价格方案（月付、年付、企业版）
Chunk 3: 技术架构（大模型、向量数据库）
Chunk 4: 客服支持（24小时在线）
```

### 传统规则切分结果

```
Chunk 1: 产品说明（可能切断价格信息）
Chunk 2: 价格 + 技术（主题混杂）
Chunk 3: 客服支持
```

## 故障排查

### 语义切分不可用

```bash
# 检查 Python 是否安装
python3 --version

# 检查 sentence-transformers
python3 -c "from sentence_transformers import SentenceTransformer; print('OK')"

# 检查模型加载
python3 scripts/semantic_splitter.py --check
```

### 模型下载失败

```bash
# 设置镜像源（中国大陆）
export HF_ENDPOINT=https://hf-mirror.com

# 重新运行
python3 scripts/semantic_splitter.py test.txt
```

## 架构图

```
┌─────────────────┐
│   Document      │
│   (PDF/DOCX)    │
└────────┬────────┘
         ▼
┌─────────────────┐
│  MarkItDown     │
│  (Parser)       │
└────────┬────────┘
         ▼
┌─────────────────┐
│  HybridSplitter │
│  (Auto Select)  │
└────────┬────────┘
         │
    ┌────┴────┐
    ▼         ▼
┌───────┐  ┌──────────┐
│ Rule  │  │ Semantic │
│Based  │  │ (bge-small)
└────┬──┘  └────┬─────┘
     │          │
     └────┬─────┘
          ▼
   ┌────────────┐
   │   Chunks   │
   │  +Overlap  │
   └────────────┘
```

## 未来优化

1. **并行处理**：批量处理多个文档的切分
2. **模型缓存**：保持模型常驻内存，减少加载时间
3. **GPU 加速**：支持 CUDA 加速 Embedding 计算
4. **增量切分**：只对新内容重新切分
5. **自定义模型**：支持加载本地模型文件
