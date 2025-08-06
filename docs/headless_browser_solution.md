# 无头浏览器渲染器解决方案

## 问题背景

原始的 `golang.org/x/image` 渲染方案存在以下问题：

1. **字体问题**：中文字符显示为问号或方块
2. **布局限制**：没有自动布局能力，需要手动计算位置
3. **文本换行**：没有自动文本换行功能
4. **样式支持有限**：无法实现复杂的CSS样式效果

## 解决方案：无头浏览器渲染器

### 1. 技术选型

使用 `chromedp` 库实现无头浏览器渲染，这是Go语言中最成熟的Chrome DevTools Protocol客户端。

**优势：**
- ✅ 完整的HTML/CSS支持
- ✅ 自动文本换行和布局
- ✅ 中文字体完美支持
- ✅ 复杂的样式效果支持
- ✅ 业界标准解决方案

### 2. 实现架构

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   卡片数据      │───▶│   HTML模板生成   │───▶│   Chrome渲染    │
│  (JSON格式)     │    │   (Go Template)  │    │  (Headless)     │
└─────────────────┘    └──────────────────┘    └─────────────────┘
                                                         │
                                                         ▼
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   图片文件      │◀───│   图片保存       │◀───│   PNG截图       │
│  (PNG格式)      │    │   (文件系统)     │    │  (Base64)       │
└─────────────────┘    └──────────────────┘    └─────────────────┘
```

### 3. 核心组件

#### HeadlessRenderer 结构

```go
type HeadlessRenderer struct {
    config *pagination.PaginationConfig
}

func NewHeadlessRenderer(config *pagination.PaginationConfig) *HeadlessRenderer {
    return &HeadlessRenderer{
        config: config,
    }
}
```

#### 主要方法

1. **RenderCardToImage**: 主渲染方法
2. **generateHTML**: 生成HTML内容
3. **renderWithHeadlessBrowser**: 使用Chrome渲染
4. **saveImage**: 保存图片文件

### 4. HTML模板设计

#### 样式系统

```css
.card {
    width: 800px;
    height: 600px;
    padding: 40px;
    background: #ffffff;
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 
                 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei';
}

.title {
    font-size: 64px;
    font-weight: bold;
    color: #333333;
    line-height: 1.4;
    text-align: justify;
}

.subtitle {
    font-size: 48px;
    color: #666666;
    line-height: 1.5;
    text-align: justify;
}

.body {
    font-size: 36px;
    color: #333333;
    line-height: 1.6;
    text-align: justify;
}

.list-item {
    margin-bottom: 8px;
    padding-left: 20px;
    position: relative;
}

.list-item::before {
    content: "•";
    position: absolute;
    left: 0;
    color: #333333;
}

.quote {
    font-size: 36px;
    color: #1E90FF;
    line-height: 1.5;
    padding-left: 20px;
    border-left: 4px solid #1E90FF;
    text-align: justify;
}
```

#### 模板结构

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Card Render</title>
    <style>
        /* CSS样式 */
    </style>
</head>
<body>
    <div class="card">
        {{range .Elements}}
            {{if eq .Type "title"}}
                <div class="title">{{.Content}}</div>
            {{else if eq .Type "subtitle"}}
                <div class="subtitle">{{.Content}}</div>
            {{else if eq .Type "body"}}
                <div class="body">{{.Content}}</div>
            {{else if eq .Type "list"}}
                <div class="list">
                    {{range .Items}}
                        <div class="list-item">{{.}}</div>
                    {{end}}
                </div>
            {{else if eq .Type "quote"}}
                <div class="quote">「{{.Content}}」</div>
            {{else}}
                <div class="body">{{.Content}}</div>
            {{end}}
        {{end}}
    </div>
</body>
</html>
```

### 5. Chrome配置

#### 启动选项

```go
opts := append(chromedp.DefaultExecAllocatorOptions[:],
    chromedp.Flag("headless", true),           // 无头模式
    chromedp.Flag("disable-gpu", true),        // 禁用GPU
    chromedp.Flag("no-sandbox", true),         // 禁用沙盒
    chromedp.Flag("disable-dev-shm-usage", true), // 禁用/dev/shm
    chromedp.Flag("disable-web-security", true),   // 禁用Web安全
    chromedp.Flag("disable-features", "VizDisplayCompositor"), // 禁用特性
)
```

#### 渲染流程

```go
err := chromedp.Run(ctx,
    chromedp.Navigate("data:text/html;charset=utf-8," + htmlContent),
    chromedp.WaitReady("body"),
    chromedp.Sleep(1*time.Second), // 等待渲染完成
    chromedp.ActionFunc(func(ctx context.Context) error {
        // 设置视口大小
        if err := chromedp.EmulateViewport(ctx, width, height).Do(ctx); err != nil {
            return err
        }

        // 截图
        imageData, _, err = page.CaptureScreenshot().
            WithFormat(page.CaptureScreenshotFormatPng).
            WithQuality(90).
            Do(ctx)
        return err
    }),
)
```

### 6. 使用方法

#### 在控制器中使用

```go
// 创建渲染器
renderer := card.NewHeadlessRenderer(pagination.GetDefaultConfig())

// 渲染卡片
renderedCard, err := renderer.RenderCardToImage(card)
if err != nil {
    return err
}

// 返回结果
return &RenderedCard{
    CardID:    renderedCard.CardID,
    ImageURL:  renderedCard.ImageURL,
    Width:     renderedCard.Width,
    Height:    renderedCard.Height,
    SortOrder: renderedCard.SortOrder,
}
```

### 7. 部署要求

#### 系统依赖

**Ubuntu/Debian:**
```bash
# 安装Chrome
wget -q -O - https://dl.google.com/linux/linux_signing_key.pub | apt-key add -
echo "deb [arch=amd64] http://dl.google.com/linux/chrome/deb/ stable main" > /etc/apt/sources.list.d/google-chrome.list
apt-get update
apt-get install -y google-chrome-stable
```

**CentOS/RHEL:**
```bash
# 安装Chrome
yum install -y google-chrome-stable
```

**macOS:**
```bash
# 使用Homebrew安装Chrome
brew install --cask google-chrome
```

#### Docker部署

```dockerfile
# 在Dockerfile中添加Chrome
RUN apt-get update && apt-get install -y \
    google-chrome-stable \
    && rm -rf /var/lib/apt/lists/*
```

### 8. 性能优化

#### 连接池

```go
var chromePool *chromedp.Pool

func init() {
    var err error
    chromePool, err = chromedp.NewPool(
        chromedp.WithExecAllocator(chromedp.NewExecAllocator(context.Background(), opts...)),
        chromedp.WithLog(log.Printf),
    )
    if err != nil {
        log.Fatal(err)
    }
}
```

#### 缓存机制

```go
type RenderCache struct {
    cache map[string][]byte
    mutex sync.RWMutex
}

func (rc *RenderCache) Get(key string) ([]byte, bool) {
    rc.mutex.RLock()
    defer rc.mutex.RUnlock()
    data, exists := rc.cache[key]
    return data, exists
}

func (rc *RenderCache) Set(key string, data []byte) {
    rc.mutex.Lock()
    defer rc.mutex.Unlock()
    rc.cache[key] = data
}
```

### 9. 测试验证

#### 测试脚本

```bash
# 运行测试
./scripts/test-headless-renderer.sh
```

#### 测试内容

- ✅ 中文字符渲染
- ✅ 自动文本换行
- ✅ 复杂布局支持
- ✅ 样式效果验证
- ✅ 性能测试

### 10. 故障排除

#### 常见问题

1. **Chrome启动失败**
   ```bash
   # 检查Chrome是否安装
   google-chrome --version
   
   # 检查权限
   ls -la /usr/bin/google-chrome
   ```

2. **内存不足**
   ```bash
   # 增加系统内存限制
   ulimit -m 1048576
   ```

3. **渲染超时**
   ```go
   // 增加超时时间
   ctx, cancel := context.WithTimeout(taskCtx, 60*time.Second)
   ```

### 11. 监控指标

#### 关键指标

- 渲染成功率
- 渲染耗时
- 内存使用量
- Chrome进程数
- 错误率统计

#### 日志记录

```go
log.Printf("Rendering card %d, size: %dx%d", cardID, width, height)
log.Printf("Render completed in %v", duration)
```

### 12. 总结

无头浏览器渲染器解决了原始方案的以下问题：

1. **✅ 字体问题**：完美支持中文字符
2. **✅ 布局问题**：自动布局和文本换行
3. **✅ 样式问题**：完整的CSS支持
4. **✅ 兼容性**：业界标准解决方案
5. **✅ 可维护性**：清晰的代码结构

这个方案是当前最成熟、最可靠的服务端渲染解决方案，强烈推荐采用。 