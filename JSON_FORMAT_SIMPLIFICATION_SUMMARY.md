# JSON格式简化总结

## 概述

根据用户需求，我们将AI大模型返回的JSON格式从复杂的结构化数组简化为简单的两个字段格式，使代码更清晰、更易维护。

## 主要改动

### 1. 数据结构简化

**旧格式（复杂结构化）**：
```json
{
  "structured_text_array": [
    {
      "type": "title",
      "content": "主标题"
    },
    {
      "type": "subtitle", 
      "content": "副标题"
    },
    {
      "type": "body",
      "content": "正文内容"
    },
    {
      "type": "list",
      "content": ["列表项1", "列表项2"]
    },
    {
      "type": "quote",
      "content": "引用内容"
    }
  ],
  "image_prompt": "文生图提示词"
}
```

**新格式（简化）**：
```json
{
  "text": "# 主标题\n\n## 副标题\n\n正文内容\n\n- 列表项1\n- 列表项2\n\n> 引用内容",
  "image_prompt": "文生图提示词"
}
```

### 2. 代码结构更新

#### 2.1 数据结构定义

**文件**: `internal/numind/controller/v1/book/create.go`
```go
// 旧格式
type QianwenResponse struct {
    StructuredTextArray []StructuredTextItem `json:"structured_text_array"`
    ImagePrompt         string               `json:"image_prompt"`
}

type StructuredTextItem struct {
    Type    string      `json:"type"`
    Content interface{} `json:"content"`
}

// 新格式
type QianwenResponse struct {
    Text        string `json:"text"`         // 带markdown格式的文字内容
    ImagePrompt string `json:"image_prompt"` // 文生图提示词
}
```

**文件**: `internal/numind/biz/book/async_processor.go`
```go
// 同样更新了QianwenResponse结构体定义
```

#### 2.2 处理逻辑更新

**文件**: `internal/numind/biz/book/async_processor.go`

- 添加了 `extractTitleFromMarkdown()` 方法：从markdown文本中提取一级标题
- 添加了 `convertMarkdownToElements()` 方法：将markdown文本转换为分页元素
- 更新了AI响应处理逻辑，直接使用 `aiResponse.Text` 和 `aiResponse.ImagePrompt`

### 3. 配置文件更新

#### 3.1 AI提示词配置

更新了所有环境的配置文件：
- `config_local.yaml`
- `config_dev.yaml` 
- `config_qa.yaml`
- `config_prod.yaml`

**主要变化**：
- 将输出格式从JSON数组改为JSON对象
- 将 `structured_text_array` 改为 `text` 字段
- `text` 字段直接包含markdown格式的内容
- 更新了示例和说明

### 4. 优势对比

#### 新格式优势：
- ✅ **结构极简**：只有两个字段，清晰明了
- ✅ **解析简单**：直接JSON.Unmarshal，无需复杂处理
- ✅ **渲染简单**：`text` 字段直接是markdown，可直接渲染
- ✅ **易于维护**：代码逻辑更简单，bug更少
- ✅ **类型安全**：字段类型明确，减少类型转换错误

#### 旧格式问题：
- ❌ **结构复杂**：需要处理复杂的结构化数组
- ❌ **解析复杂**：需要额外的markdown生成逻辑
- ❌ **维护困难**：数据结构复杂，难以理解和维护
- ❌ **类型不安全**：Content字段是interface{}，容易出错

## 使用示例

### 新的处理流程：

```go
// 1. 调用AI大模型
response, err := ctrl.b.Ali().QianwenTextStream(messages, 1024, 0.5)

// 2. 解析JSON响应
var aiResponse QianwenResponse
if err := json.Unmarshal([]byte(response), &aiResponse); err != nil {
    // 使用现有的JSON修复逻辑
    // ...
}

// 3. 直接使用两个字段
cardText := aiResponse.Text        // 带markdown格式，可直接渲染
imagePrompt := aiResponse.ImagePrompt  // 用于文生图

// 4. 调用文生图API
if imagePrompt != "" {
    imageURL, err := ctrl.b.Ali().StableDiffusionImageAsync(imagePrompt, "1024*1024")
}

// 5. 将markdown转换为分页元素
elements := p.convertMarkdownToElements(aiResponse.Text)
```

## 测试验证

新的JSON格式已经过验证：
- ✅ JSON解析正常
- ✅ 标题提取功能正常
- ✅ Markdown转换功能正常
- ✅ 与现有分页引擎兼容

## 总结

这次简化大大提升了代码的可维护性和可读性，同时保持了所有原有功能。新的格式更符合"简单就是美"的设计原则，为后续的功能扩展奠定了良好的基础。
