# 语义切分器部署指南

## 快速开始

### 1. 安装依赖

```bash
# 使用安装脚本
bash scripts/install_semantic_deps.sh

# 或手动安装
pip3 install sentence-transformers numpy
```

### 2. 验证安装

```bash
bash scripts/check_semantic_splitter.sh
```

### 3. 首次运行说明

首次使用时会自动下载 `BAAI/bge-small-zh` 模型（约 100MB）：
- 下载时间：取决于网络，通常 1-5 分钟
- 模型缓存位置：`~/.cache/torch/sentence_transformers/`
- 后续使用将直接从缓存加载，无需重新下载

**中国大陆用户**：建议设置镜像源加速下载
```bash
export HF_ENDPOINT=https://hf-mirror.com
```

## 生产环境部署

### Docker 部署

在 Dockerfile 中添加：

```dockerfile
# 安装 Python 和依赖
RUN apt-get update && apt-get install -y python3 python3-pip
RUN pip3 install sentence-transformers numpy

# 预下载模型（可选，避免首次请求时下载）
RUN python3 -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('BAAI/bge-small-zh')"
```

### Kubernetes 部署

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: model-cache
  annotations:
    # 预加载模型到共享存储
    "helm.sh/hook": pre-install
---
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      initContainers:
      - name: download-model
        image: python:3.9-slim
        command:
        - sh
        - -c
        - |
          pip install sentence-transformers
          python3 -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('BAAI/bge-small-zh')"
        volumeMounts:
        - name: model-cache
          mountPath: /root/.cache
      containers:
      - name: app
        volumeMounts:
        - name: model-cache
          mountPath: /root/.cache
      volumes:
      - name: model-cache
        emptyDir: {}
```

### 离线部署

如果服务器无法访问互联网：

1. **在有网络的机器上下载模型**
```bash
pip3 install sentence-transformers
python3 -c "from sentence_transformers import SentenceTransformer; m = SentenceTransformer('BAAI/bge-small-zh')"
# 找到缓存目录
python3 -c "import os; print(os.path.expanduser('~/.cache/torch/sentence_transformers'))"
```

2. **打包并复制到目标服务器**
```bash
# 打包模型
tar czf bge-small-zh.tar.gz ~/.cache/torch/sentence_transformers/BAAI__bge-small-zh*

# 复制到服务器并解压
tar xzf bge-small-zh.tar.gz -C ~/.cache/torch/sentence_transformers/
```

## 配置说明

### 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `HF_ENDPOINT` | HuggingFace 镜像源 | `https://huggingface.co` |
| `HF_TOKEN` | HuggingFace Token（可选） | - |
| `TRANSFORMERS_OFFLINE` | 离线模式 | `0` |

### Go 代码配置

```go
// 使用默认配置（推荐）
splitter := service.NewDefaultHybridSplitter()

// 自定义配置
splitter := service.NewHybridSplitter(service.HybridSplitterConfig{
    RuleConfig: service.EnhancedSplitterConfig{
        MaxChunkSize:    1000,
        MinChunkSize:    200,
        OverlapSize:     100,
        EnableJieba:     true,
        ProtectMarkdown: true,
    },
    SemanticConfig: service.EmbeddingSplitterConfig{
        Threshold:    0.6,   // 相似度阈值
        MinChunkSize: 100,
        MaxChunkSize: 1000,
        OverlapSize:  100,
    },
    Strategy:          service.StrategyAuto,
    SemanticMinLength: 2000,  // 超过此长度使用语义切分
})
```

## 故障排查

### 问题1：模型下载慢/失败

**症状**：首次切分超时或失败

**解决**：
```bash
# 使用国内镜像
export HF_ENDPOINT=https://hf-mirror.com

# 或使用 modelscope 镜像
export HF_ENDPOINT=https://www.modelscope.cn
```

### 问题2：内存不足

**症状**：Python 进程被 OOM killer 终止

**解决**：
- bge-small-zh 需要约 500MB 内存
- 如果内存不足，可以仅使用规则切分（StrategyRuleOnly）

### 问题3：切分结果不理想

**调整阈值**：
```go
// 更严格的切分（更多小块）
cfg.SemanticConfig.Threshold = 0.75

// 更宽松的切分（更少大块）
cfg.SemanticConfig.Threshold = 0.45
```

### 问题4：Go 调用 Python 失败

**检查**：
```bash
# 1. Python 路径
which python3

# 2. 脚本路径
ls -la scripts/semantic_splitter.py

# 3. 权限
python3 scripts/semantic_splitter.py --help
```

## 性能优化

### 1. 模型预热

启动时预热模型，避免首次请求延迟：

```go
// 在应用启动时调用
func init() {
    splitter := service.NewEmbeddingSplitter(service.EmbeddingSplitterConfig{})
    if splitter.IsAvailable() {
        log.Println("Semantic splitter model preloaded")
    }
}
```

### 2. 连接池

对于高并发场景，考虑使用进程池（需要额外开发）

### 3. 异步切分

文档导入是异步的，语义切分不会阻塞 API 响应

## 监控指标

```go
// 切分耗时统计
start := time.Now()
chunks, err := splitter.Split(text)
duration := time.Since(start)

metrics.RecordHistogram("semantic_split_duration_ms", float64(duration.Milliseconds()))
metrics.RecordCounter("semantic_split_chunks", len(chunks))
```

## 回滚方案

如果语义切分出现问题，系统会自动回退到规则切分：

```go
// 检查状态
if !splitter.IsSemanticAvailable() {
    log.Println("Using rule-based fallback")
}
```

要完全禁用语义切分，设置策略为 RuleOnly：
```go
splitter := service.NewHybridSplitter(service.HybridSplitterConfig{
    Strategy: service.StrategyRuleOnly,
})
```

## 验证清单

部署完成后，请检查：

- [ ] `bash scripts/check_semantic_splitter.sh` 通过
- [ ] 上传测试文档，检查切片质量
- [ ] 监控切分耗时（应在 100-500ms 内）
- [ ] 验证重叠内容正确添加
- [ ] 测试大文档（>10MB）切分

## 回滚指令

如需回滚到纯规则切分：

```go
// 修改代码
splitter := service.NewCompatibilitySplitter(service.SplitterConfig{
    MaxChunkSize: 1000,
    MinChunkSize: 200,
})
```

或设置环境变量禁用语义切分：
```bash
export SEMANTIC_SPLITTER_DISABLE=1
```
