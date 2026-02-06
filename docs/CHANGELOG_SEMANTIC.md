# 语义切分功能更新日志

## 2024-XX-XX - 语义切分功能上线

### 新增功能

#### 1. Embedding-based 语义切分
- 使用 BAAI/bge-small-zh 模型计算句子相似度
- 自动检测主题切换点（语义断崖）
- 支持动态阈值调整

#### 2. 混合切分策略 (HybridSplitter)
- **Auto**: 自动选择（<2000字符用规则，≥2000字符用语义）
- **RuleOnly**: 仅使用规则切分
- **SemanticOnly**: 仅使用语义切分
- **Hybrid**: 规则+语义优化

#### 3. 增强版规则切分
- 中文分词边界检测（gojieba）
- Markdown 结构保护
- 100字符前后重叠
- 代码块/表格保护

### 文件变更

```
新增:
├── scripts/semantic_splitter.py          # Python 语义切分模块
├── scripts/install_semantic_deps.sh      # 依赖安装脚本
├── scripts/check_semantic_splitter.sh    # 部署检查脚本
├── internal/numind/biz/salesrag/service/
│   ├── embedding_splitter.go             # Embedding 切分器
│   ├── hybrid_splitter.go                # 混合切分器
│   └── enhanced_splitter.go              # 增强规则切分器
└── docs/
    ├── SEMANTIC_SPLITTER.md              # 使用文档
    ├── DEPLOYMENT_SEMANTIC.md            # 部署指南
    └── CHANGELOG_SEMANTIC.md             # 本文件

修改:
├── internal/numind/biz/salesrag/service/
│   └── splitter_adapter.go               # 适配器更新
└── internal/numind/biz/salesrag/
    └── salesrag.go                       # 添加状态检查函数
```

### 部署步骤

1. **安装 Python 依赖**
   ```bash
   bash scripts/install_semantic_deps.sh
   ```

2. **验证安装**
   ```bash
   bash scripts/check_semantic_splitter.sh
   ```

3. **重启服务**
   ```bash
   # 根据你的部署方式重启
   ```

### 预期效果

| 指标 | 规则切分 | 语义切分 | 提升 |
|------|----------|----------|------|
| 切分准确率 | 82% | ~90% | +8% |
| 主题一致性 | 中等 | 高 | 显著 |
| 平均耗时 | <10ms | ~100ms | - |

### 回滚方案

如果出现问题，系统自动回退到规则切分。要完全禁用：

```go
// 修改初始化代码
splitter := service.NewHybridSplitter(service.HybridSplitterConfig{
    Strategy: service.StrategyRuleOnly,
})
```

### 注意事项

1. **首次模型下载**：约 100MB，需要 1-5 分钟
2. **内存占用**：模型加载后约 500MB 内存
3. **兼容性**：完全向后兼容，无需修改业务代码

### 后续优化计划

- [ ] 模型常驻内存，减少加载时间
- [ ] GPU 加速支持
- [ ] 批量并行切分
- [ ] 自定义本地模型路径
