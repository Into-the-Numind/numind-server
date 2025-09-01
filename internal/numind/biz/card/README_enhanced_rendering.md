# 增强版卡片渲染系统

## 概述

本系统实现了基于用户需求的增强版卡片渲染功能，包括：
- 第一张卡片的特殊布局（阿里文生图 + 标题）
- 基于无头浏览器的精确高度测量
- 智能分页算法（确保卡片不跨页）
- 超长图拼接和切分功能
- 多种渲染策略选择

## 核心组件

### 1. EnhancedCardRenderer - 增强版卡片渲染器
```go
renderer := NewEnhancedCardRenderer(config)
cards, err := renderer.RenderBookWithPagination(book, structuredTextArray, imagePromptURL)
```

特点：
- 实现第一张卡片特殊布局（上半部分：阿里文生图60%，下半部分：标题40%）
- 后续卡片按 structured_text_array 顺序渲染
- 每张卡片固定尺寸 1080×1440px
- 严格按照样式规则渲染不同类型的内容

### 2. SuperLongImageProcessor - 超长图处理器
```go
processor := NewSuperLongImageProcessor(config)
cards, err := processor.ProcessBookAsSuperLongImage(book, structuredTextArray, imagePromptURL)
```

特点：
- 先生成整本书的超长图
- 精确计算切分点，确保卡片完整性
- 按1080×1440px切分输出
- 适合内容较多的复杂书籍

### 3. PreciseMeasurementEngine - 精确测量引擎
```go
engine := NewPreciseMeasurementEngine(config)
measurements, err := engine.MeasureAllElements(structuredTextArray, imagePromptURL)
optimizedPages, err := engine.OptimizePagination(measurements)
```

特点：
- 使用无头浏览器进行精确高度测量
- 基于实际渲染结果优化分页
- 提供详细的测量元数据
- 最高的渲染精度

### 4. CardRendererCoordinator - 渲染协调器
```go
coordinator := NewCardRendererCoordinator(config)
strategy, options := coordinator.GetOptimalStrategy(structuredTextArray)
cards, err := coordinator.RenderBookWithStrategy(book, structuredTextArray, imagePromptURL, options)
```

特点：
- 统一的渲染接口
- 智能策略选择
- 结果验证和优化
- 支持多种渲染模式

## 样式规则

### 第一张卡片
- 上半部分（60%）：阿里文生图，完全填充，无留白
- 下半部分（40%）：标题内容，居中显示，字体36px加粗，背景#F5F5F5

### 后续卡片样式
- **卡片尺寸**：1080×1440px
- **边距**：左右60px统一
- **subtitle**：字体24px，颜色#4A4A4A，左对齐，上下留白25px，底部1px灰色分割线
- **body**：字体18px，行间距1.8倍，颜色#333333，两端对齐，段落间留白20px
- **list**：前缀・（颜色#FF6B35），左侧缩进30px，项间留白15px，字体18px
- **quote**：背景色#F0F7FF，左侧5px蓝色边框，内边距20px，斜体，字体18px，颜色#2D3748

## 使用示例

### 基本使用
```go
package main

import (
    "numind-server/internal/numind/biz/card"
    "numind-server/internal/numind/biz/pagination"
)

func main() {
    // 配置
    config := &pagination.PaginationConfig{
        Card: pagination.CardConfig{
            Width:  1080,
            Height: 1440,
            Padding: pagination.PaddingConfig{
                Top:    60,
                Bottom: 60,
                Left:   60,
                Right:  60,
            },
        },
    }
    
    // 创建协调器
    coordinator := card.NewCardRendererCoordinator(config)
    
    // 自动选择最佳策略
    strategy, options := coordinator.GetOptimalStrategy(structuredTextArray)
    
    // 渲染
    renderedCards, err := coordinator.RenderBookWithStrategy(
        book, 
        structuredTextArray, 
        imagePromptURL, 
        options,
    )
    
    if err != nil {
        log.Fatal(err)
    }
    
    // 验证结果
    if err := coordinator.ValidateRenderingResult(renderedCards); err != nil {
        log.Fatal(err)
    }
    
    fmt.Printf("成功渲染 %d 张卡片\n", len(renderedCards))
}
```

### 指定策略使用
```go
// 使用增强渲染策略
options := card.RenderBookOptions{
    Strategy:           card.StrategyEnhanced,
    EnableMeasurement:  true,
    EnableOptimization: true,
    DebugMode:         false,
}

renderedCards, err := coordinator.RenderBookWithStrategy(
    book, 
    structuredTextArray, 
    imagePromptURL, 
    options,
)
```

### 精确测量使用
```go
// 使用精确测量策略
options := card.RenderBookOptions{
    Strategy:           card.StrategyPreciseMeasurement,
    EnableMeasurement:  true,
    EnableOptimization: true,
}

renderedCards, err := coordinator.RenderBookWithStrategy(
    book, 
    structuredTextArray, 
    imagePromptURL, 
    options,
)
```

## 数据结构

### 输入数据
```go
// structured_text_array 示例
elements := []pagination.Element{
    {
        Type:    pagination.ElementTypeTitle,
        Content: "这是标题",
    },
    {
        Type:    pagination.ElementTypeSubtitle,
        Content: "这是副标题",
    },
    {
        Type:    pagination.ElementTypeBody,
        Content: "这是正文内容，支持长文本自动换行...",
    },
    {
        Type:    pagination.ElementTypeList,
        Content: []string{"列表项1", "列表项2", "列表项3"},
    },
    {
        Type:    pagination.ElementTypeQuote,
        Content: "这是引用内容",
    },
}
```

### 输出数据
```go
type RenderedCard struct {
    CardID    uint   `json:"card_id"`
    ImageURL  string `json:"image_url"`
    Width     int    `json:"width"`
    Height    int    `json:"height"`
    SortOrder int    `json:"sort_order"`
}
```

## 技术特性

1. **精确测量**：使用Chrome无头浏览器进行实际渲染测量
2. **智能分页**：确保内容完整性，避免跨页切分
3. **多策略支持**：根据内容复杂度自动选择最佳渲染策略
4. **高度兼容**：与现有图片存储和URL管理系统完全兼容
5. **错误处理**：完善的错误处理和降级机制
6. **性能优化**：支持并行渲染和缓存机制

## 性能考虑

- 简单内容（≤5个元素）：使用增强策略，快速渲染
- 中等复杂度（6-20个元素）：使用精确测量策略，平衡质量和性能
- 复杂内容（>20个元素）：使用超长图策略，确保一致性

## 注意事项

1. 确保Chrome/Chromium已安装且可访问
2. 临时文件会在渲染完成后自动清理
3. 图片文件保存在配置的路径中
4. 支持WebP和PNG格式输出
5. 建议在服务器环境中使用无头模式

## 扩展说明

本系统设计为可扩展架构，可以轻松添加：
- 新的渲染策略
- 自定义样式模板
- 不同的输出格式
- 更多测量指标
- 渲染优化算法
