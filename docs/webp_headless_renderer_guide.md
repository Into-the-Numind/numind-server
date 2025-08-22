# WebP无头浏览器渲染器使用指南

## 概述

本项目使用无头浏览器（Chrome Headless）将HTML内容渲染为高质量的WebP图片，专门用于卡片渲染。相比传统的Go图片生成方案，无头浏览器渲染器具有以下优势：

- ✅ **完美支持中文字体**
- ✅ **完整的CSS样式支持**
- ✅ **自动文本换行和布局**
- ✅ **高质量的WebP输出**
- ✅ **支持复杂的HTML结构**

## 技术架构

### 核心组件

1. **chromedp**: Go语言的Chrome DevTools Protocol客户端
2. **Chrome Headless**: 无头浏览器引擎
3. **WebP格式**: 高效的图片压缩格式

### 渲染流程

```
卡片数据 (JSON) → HTML生成 → Chrome渲染 → WebP截图 → 文件保存
```

## 现有渲染器

### 1. SimpleHeadlessRenderer

基础的无头浏览器渲染器，位于 `internal/numind/biz/card/headless_renderer.go`

**特点：**
- 简单的HTML模板
- 基本的样式支持
- 调试信息丰富

**使用示例：**
```go
config := &pagination.PaginationConfig{
    Card: pagination.CardConfig{
        Width:  1080,
        Height: 1440,
        Padding: pagination.Padding{
            Top:    40,
            Bottom: 40,
            Left:   40,
            Right:  40,
        },
    },
}

renderer := card.NewSimpleHeadlessRenderer(config)
renderedCard, err := renderer.RenderCardToImage(card)
```

### 2. EnhancedCardRenderer

增强版卡片渲染器，位于 `internal/numind/biz/card/enhanced_card_renderer.go`

**特点：**
- 更丰富的样式支持
- 动态高度计算
- 更好的布局控制

### 3. LightweightMarkdownRenderer

轻量级Markdown渲染器，位于 `internal/numind/biz/markdown/lightweight_renderer.go`

**特点：**
- 支持Markdown语法
- 自动分页处理
- 封面卡片支持

## 配置选项

### Chrome选项

```go
opts := append(chromedp.DefaultExecAllocatorOptions[:],
    chromedp.Flag("headless", true),           // 无头模式
    chromedp.Flag("disable-gpu", true),        // 禁用GPU
    chromedp.Flag("no-sandbox", true),         // 禁用沙盒
    chromedp.Flag("disable-dev-shm-usage", true), // 禁用共享内存
    chromedp.Flag("disable-web-security", true),  // 禁用Web安全
    chromedp.Flag("window-size", "1080,1440"),    // 窗口大小
    chromedp.Flag("disable-extensions", true),    // 禁用扩展
    chromedp.Flag("disable-plugins", true),       // 禁用插件
)
```

### WebP配置

```go
// 截图配置
imageData, err := page.CaptureScreenshot().
    WithFormat(page.CaptureScreenshotFormatWebp).  // WebP格式
    WithQuality(int64(85)).                        // 质量85%
    Do(ctx)
```

## 样式规范

### 基础样式

```css
body {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', 'Helvetica Neue', Helvetica, Arial, sans-serif;
    font-size: 16px;
    line-height: 1.6;
    color: #333333;
    background-color: #ffffff;
    width: 1080px;
    height: 1440px;
    overflow: hidden;
    padding: 40px;
}
```

### 元素样式

```css
.title {
    font-size: 32px;
    font-weight: 600;
    color: #333333;
    line-height: 1.2;
}

.subtitle {
    font-size: 24px;
    font-weight: 500;
    color: #666666;
    line-height: 1.3;
}

.text {
    font-size: 18px;
    color: #333333;
    line-height: 1.6;
    text-align: justify;
}

.quote {
    font-size: 18px;
    color: #1E90FF;
    font-style: italic;
    line-height: 1.6;
    padding: 16px;
    background: linear-gradient(135deg, #f8f9fa 0%, #e9ecef 100%);
    border-left: 4px solid #1E90FF;
    border-radius: 4px;
}
```

## 使用方法

### 1. 基本使用

```go
package main

import (
    "numind-server/internal/numind/biz/card"
    "numind-server/internal/numind/biz/pagination"
    "numind-server/internal/pkg/model"
)

func renderCard() {
    // 创建配置
    config := &pagination.PaginationConfig{
        Card: pagination.CardConfig{
            Width:  1080,
            Height: 1440,
            Padding: pagination.Padding{
                Top:    40,
                Bottom: 40,
                Left:   40,
                Right:  40,
            },
        },
    }

    // 创建渲染器
    renderer := card.NewSimpleHeadlessRenderer(config)

    // 检查可用性
    if !renderer.IsAvailable() {
        log.Fatal("渲染器不可用")
    }

    // 创建卡片数据
    card := &model.CardM{
        ProcessedText: `[{"type":"title","content":"标题"},{"type":"text","content":"内容"}]`,
        SortOrder:     1,
    }

    // 渲染卡片
    renderedCard, err := renderer.RenderCardToImage(card)
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("渲染成功: %s\n", renderedCard.ImageURL)
}
```

### 2. 集成到现有系统

在 `internal/numind/biz/book/async_processor.go` 中：

```go
func (p *AsyncBookProcessor) generateCardImageAndHTML(ctx context.Context, cardID uint, markdownContent string) error {
    // 使用无头浏览器渲染器
    config := &pagination.PaginationConfig{
        Card: pagination.CardConfig{
            Width:  1080,
            Height: 1440,
            Padding: pagination.Padding{
                Top:    40,
                Bottom: 40,
                Left:   40,
                Right:  40,
            },
        },
    }
    
    renderer := card.NewSimpleHeadlessRenderer(config)
    
    // 创建卡片对象
    card := &model.CardM{
        ProcessedText: markdownContent,
    }
    
    // 渲染图片
    renderedCard, err := renderer.RenderCardToImage(card)
    if err != nil {
        return err
    }
    
    // 保存结果
    // ...
    
    return nil
}
```

## 性能优化

### 1. 内存优化

```go
// 使用临时文件避免内存占用
tempFile, err := os.CreateTemp("", "card_*.html")
defer os.Remove(tempFile.Name())
defer tempFile.Close()
```

### 2. 超时控制

```go
// 设置合理的超时时间
ctx, cancel := context.WithTimeout(taskCtx, 30*time.Second)
defer cancel()
```

### 3. 并发控制

```go
// 限制并发渲染数量
semaphore := make(chan struct{}, 5) // 最多5个并发
semaphore <- struct{}{}
defer func() { <-semaphore }()
```

## 错误处理

### 常见错误及解决方案

1. **Chrome不可用**
   ```
   错误: chrome executable not found
   解决: 安装Chrome或Chromium浏览器
   ```

2. **渲染超时**
   ```
   错误: context deadline exceeded
   解决: 增加超时时间或优化HTML内容
   ```

3. **内存不足**
   ```
   错误: out of memory
   解决: 减少并发数量或增加系统内存
   ```

4. **字体渲染问题**
   ```
   错误: 中文显示为方块
   解决: 确保系统安装了中文字体
   ```

## 调试技巧

### 1. 保存HTML文件

```go
// 保存HTML用于调试
debugFile := fmt.Sprintf("debug_%d.html", time.Now().Unix())
os.WriteFile(debugFile, []byte(htmlContent), 0644)
```

### 2. 启用详细日志

```go
// 在渲染前添加调试信息
fmt.Printf("配置: %+v\n", config)
fmt.Printf("渲染器可用: %v\n", renderer.IsAvailable())
fmt.Printf("版本: %s\n", renderer.GetVersion())
```

### 3. 检查页面内容

```go
// 检查渲染后的页面内容
var bodyText string
chromedp.Text("body", &bodyText).Do(ctx)
fmt.Printf("页面内容: %s\n", bodyText)
```

## 最佳实践

### 1. 配置管理

将渲染配置集中管理：

```go
// configs/renderer.yaml
renderer:
  chrome:
    headless: true
    disable_gpu: true
    no_sandbox: true
    window_size: "1080,1440"
  webp:
    quality: 85
    timeout: 30s
  card:
    width: 1080
    height: 1440
    padding:
      top: 40
      bottom: 40
      left: 40
      right: 40
```

### 2. 错误重试

```go
func renderWithRetry(renderer card.RendererInterface, card *model.CardM, maxRetries int) (*card.RenderedCard, error) {
    for i := 0; i < maxRetries; i++ {
        renderedCard, err := renderer.RenderCardToImage(card)
        if err == nil {
            return renderedCard, nil
        }
        
        log.Printf("渲染失败，重试 %d/%d: %v", i+1, maxRetries, err)
        time.Sleep(time.Duration(i+1) * time.Second)
    }
    
    return nil, fmt.Errorf("渲染失败，已重试 %d 次", maxRetries)
}
```

### 3. 缓存策略

```go
// 对相同内容进行缓存
func getCachedImage(cardHash string) (string, bool) {
    // 检查缓存
    if cachedPath, exists := imageCache[cardHash]; exists {
        return cachedPath, true
    }
    return "", false
}
```

## 总结

无头浏览器渲染器为卡片渲染提供了强大而灵活的解决方案：

1. **高质量渲染**: 支持完整的HTML/CSS功能
2. **中文支持**: 完美支持中文字体和排版
3. **WebP格式**: 高效的图片压缩
4. **易于维护**: 基于标准的Web技术栈
5. **性能优化**: 支持并发和缓存

通过合理配置和优化，无头浏览器渲染器可以满足各种复杂的卡片渲染需求，为用户提供高质量的视觉体验。
