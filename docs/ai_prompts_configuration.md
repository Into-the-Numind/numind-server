# AI提示词配置系统

## 概述

本系统支持通过配置文件来管理AI提示词，避免了硬编码，提高了系统的灵活性和可维护性。

## 配置结构

在配置文件中添加 `ai_prompts` 部分：

```yaml
# AI提示词配置
ai_prompts:
  text_processing: |
    # 文本处理提示词模板
    # 这里放置完整的文本处理提示词
    # 支持多行文本，使用 | 符号
  image_generation: "基于以下文本生成一张精美的配图：{content}"
```

## 配置项说明

### text_processing
- **类型**: 字符串
- **说明**: 用于文本处理的AI提示词模板
- **特点**: 支持多行文本，使用YAML的 `|` 语法
- **用途**: 调用千问大模型进行文本结构化处理

### image_generation
- **类型**: 字符串
- **说明**: 用于图片生成的提示词模板
- **特点**: 支持占位符 `{content}` 用于动态替换内容
- **用途**: 调用万相大模型生成配图

## 使用方法

### 1. 在代码中使用

```go
// 获取文本处理提示词
textPrompt := aliBiz.GetPromptManager().GetTextProcessingPrompt()

// 获取图片生成提示词（带内容替换）
imagePrompt := aliBiz.GetPromptManager().FormatImagePrompt("要生成图片的文本内容")
```

### 2. 修改提示词

直接编辑配置文件中的 `ai_prompts` 部分即可，无需修改代码：

```yaml
ai_prompts:
  text_processing: |
    # 你的新提示词模板
    # 支持完整的提示词内容
  image_generation: "你的图片生成模板：{content}"
```

## 环境配置

系统支持不同环境的配置：

- `config_local.yaml` - 本地开发环境
- `config_dev.yaml` - 开发环境
- `config_qa.yaml` - 测试环境
- `config_prod.yaml` - 生产环境

每个环境都可以有不同的AI提示词配置。

## 默认值

如果配置文件中没有设置 `ai_prompts`，系统会使用内置的默认提示词：

- **文本处理**: 使用原有的文本重构提示词
- **图片生成**: `"基于以下文本生成一张精美的配图：{content}"`

## 提示词模板示例

### 文本处理提示词

```yaml
text_processing: |
  # 角色 (Persona) 你是一位资深的内容编辑与信息架构师...
  # 整体目标 (Overall Goal) 我将为你提供一段通过OCR技术从图片中识别出的原始文本...
  # 核心原则 (Core Principles)
  忠于原文 (Fidelity): 用户的原始文本是核心和基础。
  # ... 更多提示词内容
```

### 图片生成提示词

```yaml
image_generation: "基于以下文本生成一张精美的配图：{content}"
```

## 注意事项

1. **占位符**: 图片生成提示词中的 `{content}` 会被实际内容替换
2. **多行文本**: 使用 `|` 语法支持多行提示词
3. **环境隔离**: 不同环境可以使用不同的提示词策略
4. **热更新**: 修改配置文件后需要重启服务才能生效

## 技术实现

- **配置读取**: 使用 `viper` 库读取配置文件
- **提示词管理**: `PromptManager` 类负责管理提示词
- **接口设计**: `AliBiz` 接口提供 `GetPromptManager()` 方法
- **类型安全**: 使用Go的类型系统确保配置正确性 