# 提交说明

## 修复内容

### 1. 修复切分策略（核心修复）
**问题**：短文本（<2000字符）只能用规则切分，可能被切碎
**解决**：改为"语义切分优先"策略
- 只要AI模型可用，就用语义切分（不管文本长短）
- 只有文本特别短（<100字符）且只有1个句子时，才用规则切分
- SemanticMinLength 从 2000 改为 0

### 2. 修复强制切分边界选择
**问题**：强制切分时可能切在句子中间
**解决**：修改 findForceSplitPoint 函数，优先寻找句子结束符
- 第一优先级：句子边界（。！？.!?）
- 第二优先级：分词边界（jieba）
- 最后手段：强制切分

### 3. 添加调试日志
**功能**：添加 SPLITTER_DEBUG 环境变量
- 开启后可查看实际使用的切分策略
- 帮助排查切分问题

## 提交命令

```bash
git add internal/numind/biz/salesrag/service/hybrid_splitter.go
git add internal/numind/biz/salesrag/service/splitter_adapter.go
git add internal/numind/biz/salesrag/service/enhanced_splitter.go
git add docs/NEW_SPLITTING_STRATEGY.md
git add docs/COMMIT_MESSAGE.md

git commit -m "fix: 修复切分策略，优先使用语义切分，不再根据文本长度判断

- 将 SemanticMinLength 从 2000 改为 0
- 只要语义切分可用就优先使用（99%情况）
- 只有文本<100字符且只有1个句子时才用规则切分
- 修复强制切分，优先在句子边界切分
- 添加 SPLITTER_DEBUG 环境变量用于调试
- 添加 hasMultipleSentences 函数判断句子数量

修复了短文本可能被切碎的问题，现在不管文章长短，
只要AI模型可用，就用高质量的语义切分。"

git push origin develop
```

## 预期效果

修复前：
- 1999字符的文章用规则切分（可能切碎）
- 2001字符的文章用语义切分（质量好）

修复后：
- 几乎所有文章都用语义切分（质量高）
- 只有特别短的文章（一句话）才用规则切分
- 切分质量整体提升
