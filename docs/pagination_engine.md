# 分页引擎文档

## 概述

分页引擎是一个后端服务，用于将结构化的内容自动分页成多个卡片。它解决了前端分页计算的问题，确保数据一致性和流程可靠性。

## 架构优势

### 当前问题
- 前端计算分页导致数据不一致
- 跨平台渲染差异
- 流程脆弱，容易中断
- 职责划分不清

### 解决方案
- 后端中心化处理分页
- 统一的样式配置
- 可靠的数据持久化
- 清晰的职责划分

## 核心组件

### 1. 分页引擎 (PaginationEngine)

```go
type PaginationEngine struct {
    config *PaginationConfig
}
```

主要功能：
- 计算文本高度
- 执行分页逻辑
- 生成卡片数组

### 2. 样式配置 (PaginationConfig)

```go
type PaginationConfig struct {
    Card   CardConfig              `json:"card"`
    Styles map[ElementType]StyleConfig `json:"styles"`
}
```

包含：
- 卡片尺寸配置
- 各种元素类型的样式定义
- 字体、行高、边距等参数

### 3. 元素类型

支持的元素类型：
- `title`: 标题
- `subtitle`: 副标题
- `body`: 正文
- `list`: 列表
- `quote`: 引用

## 使用方法

### 1. 基本使用

```go
// 创建分页引擎
engine := pagination.NewPaginationEngine(pagination.GetDefaultConfig())

// 准备数据
elements := []pagination.Element{
    {
        Type:    pagination.ElementTypeTitle,
        Content: "标题内容",
    },
    {
        Type:    pagination.ElementTypeBody,
        Content: "正文内容",
    },
}

// 执行分页
result, err := engine.Paginate(elements)
if err != nil {
    // 处理错误
}

// 获取分页结果
cards := result.Cards
```

### 2. 从JSON字符串分页

```go
jsonStr := `[
    {
        "type": "title",
        "content": "标题"
    },
    {
        "type": "body",
        "content": "正文"
    }
]`

result, err := pagination.PaginateFromJSON(jsonStr)
```

### 3. 自定义配置

```go
config := &pagination.PaginationConfig{
    Card: pagination.CardConfig{
        Width:  750,
        Height: 1334,
        Padding: struct {
            Top    int `json:"top"`
            Right  int `json:"right"`
            Bottom int `json:"bottom"`
            Left   int `json:"left"`
        }{
            Top:    80,
            Right:  60,
            Bottom: 80,
            Left:   60,
        },
    },
    Styles: map[pagination.ElementType]pagination.StyleConfig{
        pagination.ElementTypeTitle: {
            FontSize:     48,
            LineHeight:   72,
            MarginTop:    0,
            MarginBottom: 40,
        },
        // ... 其他样式
    },
}

engine := pagination.NewPaginationEngine(config)
```

## API接口

### 1. 分页接口

**POST** `/v1/pagination/paginate`

请求体：
```json
{
    "elements": [
        {
            "type": "title",
            "content": "标题"
        },
        {
            "type": "body",
            "content": "正文"
        }
    ]
}
```

响应：
```json
{
    "cards": [
        {
            "elements": [
                {
                    "type": "title",
                    "content": "标题"
                },
                {
                    "type": "body",
                    "content": "正文"
                }
            ]
        }
    ]
}
```

### 2. 配置管理

**GET** `/v1/pagination/config` - 获取当前配置
**PUT** `/v1/pagination/config` - 更新配置

### 3. 测试接口

**GET** `/v1/pagination/test` - 使用示例数据测试分页功能

## 集成到现有系统

### 1. 在业务层使用

```go
// 在 biz.go 中添加
func (b *biz) Pagination() pagination.PaginationBiz {
    return pagination.NewPaginationBiz()
}
```

### 2. 在控制器中使用

```go
// 在控制器中注入分页业务
type SomeController struct {
    paginationBiz pagination.PaginationBiz
}

// 使用分页功能
result, err := c.paginationBiz.PaginateElements(elements)
```

### 3. 在路由中注册

```go
// 在 router.go 中添加分页路由
paginationController := pagination.NewPaginationController(b.Pagination())
authGroup.POST("/pagination/paginate", paginationController.Paginate)
authGroup.GET("/pagination/config", paginationController.GetConfig)
authGroup.PUT("/pagination/config", paginationController.UpdateConfig)
authGroup.GET("/pagination/test", paginationController.TestPagination)
```

## 配置说明

### 默认配置

```go
func GetDefaultConfig() *PaginationConfig {
    return &PaginationConfig{
        Card: CardConfig{
            Width:  750,   // 卡片宽度
            Height: 1334,  // 卡片高度
            Padding: struct {
                Top    int `json:"top"`
                Right  int `json:"right"`
                Bottom int `json:"bottom"`
                Left   int `json:"left"`
            }{
                Top:    80,  // 上边距
                Right:  60,  // 右边距
                Bottom: 80,  // 下边距
                Left:   60,  // 左边距
            },
        },
        Styles: map[ElementType]StyleConfig{
            ElementTypeTitle: {
                FontSize:     48,   // 字体大小
                LineHeight:   72,   // 行高
                MarginTop:    0,    // 上边距
                MarginBottom: 40,   // 下边距
            },
            ElementTypeSubtitle: {
                FontSize:     36,
                LineHeight:   54,
                MarginTop:    0,
                MarginBottom: 30,
            },
            ElementTypeBody: {
                FontSize:     32,
                LineHeight:   48,
                MarginTop:    0,
                MarginBottom: 20,
            },
            ElementTypeList: {
                FontSize:     32,
                LineHeight:   48,
                MarginTop:    0,
                MarginBottom: 20,
                Indent:       40,   // 缩进
            },
            ElementTypeQuote: {
                FontSize:     32,
                LineHeight:   48,
                MarginTop:    0,
                MarginBottom: 20,
            },
        },
    }
}
```

## 测试

### 运行测试

```bash
# 运行单元测试
go test ./internal/numind/biz/pagination/ -v

# 运行独立测试脚本
go run test_pagination.go
```

### 测试数据

测试脚本包含了完整的示例数据，包括：
- 标题、副标题、正文
- 列表内容
- 引用内容
- 长文本内容

## 注意事项

1. **字体配置**: 确保后端和前端使用相同的字体文件
2. **样式一致性**: 配置参数需要与前端渲染参数保持一致
3. **性能考虑**: 大量文本分页时注意性能优化
4. **错误处理**: 妥善处理分页过程中的异常情况

## 未来优化

1. **精确文本测量**: 集成字体渲染库进行精确的文本宽度计算
2. **智能分页**: 优化分页算法，避免单词被截断
3. **缓存机制**: 对分页结果进行缓存
4. **配置热更新**: 支持运行时配置更新
5. **多语言支持**: 支持不同语言的文本分页 