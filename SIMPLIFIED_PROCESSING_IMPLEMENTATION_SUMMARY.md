# 简化处理模式实现总结

## 功能概述

基于用户需求，删除了旧的cover相关逻辑，简化了卡册创建功能，使用markdown格式和配置文件中的AI提示词进行处理。

## 主要修改

### 1. 删除旧的cover相关逻辑
删除了以下文件：
- `internal/numind/biz/markdown/markdown_processor.go`
- `internal/numind/biz/markdown/prompt_manager.go`
- `internal/numind/biz/markdown/prompt_manager_simple.go`
- `internal/numind/biz/markdown/pagination_adapter.go`
- `internal/numind/biz/markdown/async_processor.go`
- `internal/numind/biz/markdown/integration_adapter.go`
- `internal/numind/controller/v1/book/create_markdown.go`

### 2. 简化处理流程
使用简化的处理模式，不再有cover相关的计算：

#### 新流程：
1. 调用文字大模型，获取返回的 markdown 格式内容
2. 从 markdown 内容中提取 image_prompt 字段
3. 调用文生图大模型生成图片
4. 图片存储规则实现
5. 临时HTML文件生成

### 3. 使用配置文件中的提示词
使用`ai_prompts.text_processing`配置：
```yaml
ai_prompts:
  text_processing: |
    # 角色
    你是一位资深的内容编辑与信息架构师...
    # 整体目标 
    我将为你提供一段通过OCR技术从图片中识别出的原始文本...
```

### 4. 简化的核心方法

#### `parseMarkdownResponse`
解析AI返回的markdown格式响应：
```go
func (p *AsyncBookProcessor) parseMarkdownResponse(response string) (string, string)
```

#### `extractImagePromptFromMarkdown`
从markdown中提取图片提示词：
```go
func (p *AsyncBookProcessor) extractImagePromptFromMarkdown(markdown string) string
```

#### `generateDefaultImagePrompt`
生成默认的图片提示词：
```go
func (p *AsyncBookProcessor) generateDefaultImagePrompt(content string) string
```

## 图片提示词提取策略

### 1. JSON格式解析
首先尝试解析JSON格式的响应：
```json
{
  "text": "markdown内容",
  "image_prompt": "图片提示词"
}
```

### 2. Markdown格式提取
如果不是JSON格式，从markdown中提取图片提示词：
- `图片提示词: 内容`
- `image_prompt: 内容`
- `图片描述: 内容`
- `<!-- image_prompt: 内容 -->`

### 3. 智能生成
如果都没有找到，根据内容智能生成：
- 技术/科技内容 → 现代科技办公环境
- 学习/教育内容 → 温馨的学习环境
- 思考/思维内容 → 抽象思维概念图
- 未来/创新内容 → 未来科技城市

## 配置要求

### 1. 启用简化处理
```yaml
book:
    use_simplified_processing: true
```

### 2. 配置AI提示词
```yaml
ai_prompts:
  text_processing: |
    # 你的提示词内容
    # 待处理的OCR文本: {content}
```

### 3. 图片路径配置
```yaml
resource:
  image_path: /path/to/upload
```

## 测试方法

### 1. 运行测试脚本
```bash
chmod +x scripts/test-enhanced-book-creation.sh
./scripts/test-enhanced-book-creation.sh
```

### 2. 直接API测试
```bash
curl -X POST http://localhost:8080/v1/books \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE" \
  -d '{
    "text": "你的文本内容",
    "template_id": "1"
  }'
```

## 优势特点

### 1. 简化架构
- 删除了复杂的cover相关逻辑
- 减少了代码复杂度
- 提高了维护性

### 2. 使用配置文件提示词
- 统一管理AI提示词
- 便于调整和优化
- 支持环境差异化配置

### 3. Markdown格式处理
- 更自然的文本格式
- 支持多种图片提示词提取方式
- 向后兼容JSON格式

### 4. 智能提示词生成
- 根据内容自动生成合适的图片提示词
- 支持多种内容类型
- 提供默认兜底方案

## 文件路径规则

### 卡册封面图片
```
resource.image_path/{bookid}/book_{id}.webp
```

### 卡片图片
```
resource.image_path/{cardid}/card_{id}.webp
```

### 临时HTML文件
```
resource.image_path/{cardid}/card_{id}.html
```

## 总结

通过这次修改，我们成功简化了卡册创建功能，删除了旧的cover相关逻辑，主要改进包括：

1. **简化架构**：删除了复杂的cover相关逻辑，减少了代码复杂度
2. **配置化管理**：使用配置文件中的提示词，便于统一管理和调整
3. **智能提取**：支持多种方式提取图片提示词，包括JSON、markdown标记和智能生成
4. **向后兼容**：保持对JSON格式的支持，确保系统的稳定性
5. **完整流程**：实现了从文本处理到图片生成再到文件存储的完整流程

这个实现完全基于现有逻辑进行修改，删除了不必要的复杂性，使系统更加简洁和易于维护。
