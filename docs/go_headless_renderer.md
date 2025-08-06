# Go + 无头浏览器渲染器

## 概述

这是一个基于Go + 无头浏览器的渲染系统，用于将书籍数据渲染为HTML页面和图片。系统完全在后端处理，无需前端框架。

## 核心特性

### ✅ 后端渲染
- 完全在Go后端处理
- 使用无头浏览器（Chrome）渲染
- 支持HTML和图片输出

### ✅ 严格遵循设计规范
- 卡片尺寸：100vw × 133.33vw (3:4宽高比)
- 内边距：上下60rpx，左右50rpx
- 元素间距严格按照规范设置

### ✅ 异步体验
- 用户创建操作完全异步
- 无全局阻塞式加载动画
- 用户稍后查看结果

## 系统架构

```
Go后端
├── HTML渲染器 (html_renderer.go)
├── 无头浏览器渲染器 (headless_renderer.go)
├── 控制器 (book.go)
└── 路由配置 (router.go)
```

## API端点

### 1. HTML查看端点
```
GET /api/v1/books/{id}/html
```
返回完整的HTML页面，包含封面页和所有内容页。

### 2. 图片查看端点
```
GET /api/v1/books/{id}/image
```
返回PNG格式的图片文件。

### 3. 单个卡片HTML端点
```
GET /api/v1/cards/{id}/html
```
返回单个卡片的HTML页面。

## 渲染流程

### 1. 数据获取
```go
// 获取书籍信息
book, err := ctrl.b.Books().GetByID(c, uint(bookID))

// 获取所有卡片
_, cards, err := ctrl.b.Cards().ListByBook(c, uint(bookID), 0, 1000)
```

### 2. HTML生成
```go
// 创建HTML渲染器
htmlRenderer := card.NewHTMLRenderer(pagination.GetDefaultConfig())

// 渲染HTML
htmlContent, err := htmlRenderer.RenderBookToHTML(book, cards)
```

### 3. 图片渲染
```go
// 使用无头浏览器渲染为图片
headlessRenderer := card.NewSimpleHeadlessRenderer(pagination.GetDefaultConfig())
renderedCard, err := headlessRenderer.RenderCardToImage(tempCard)
```

## 样式规范

### 文本样式表

| 类型 | 字体大小 | 颜色 | 对齐 | 行高 | 特殊样式 |
|------|----------|------|------|------|----------|
| 标题 (title) | 64rpx | #333333 | justify | 1.4 | - |
| 副标题 (subtitle) | 48rpx | #666666 | justify | 1.5 | - |
| 正文 (body) | 36rpx | #333333 | justify | 1.6 | - |
| 列表 (list) | 36rpx | #333333 | justify | 1.6 | 项目符号 |
| 引用 (quote) | 36rpx | #1E90FF | justify | 1.5 | 渐变背景+左边框 |

### 特殊渲染规则

#### 引用 (quote)
- 背景：`linear-gradient(to right, #EAF2FF, #FAFCFF)`
- 左边框：4px宽，颜色#1E90FF
- 文字：斜体
- 内边距：20rpx

#### 列表 (list)
- content为字符串数组
- 每项前使用项目符号（•）
- 统一左侧缩进

## 使用方法

### 1. 启动服务器
```bash
go run cmd/numind/main.go
```

### 2. 创建书籍
```bash
curl -X POST "http://localhost:8080/api/v1/books" \
  -H "Content-Type: application/json" \
  -d '{
    "text": "您的文本内容",
    "template_id": "1"
  }'
```

### 3. 查看HTML
```bash
curl "http://localhost:8080/api/v1/books/{book_id}/html"
```

### 4. 查看图片
```bash
curl "http://localhost:8080/api/v1/books/{book_id}/image" -o book.png
```

## 测试

运行完整的测试脚本：

```bash
chmod +x scripts/test-html-renderer.sh
./scripts/test-html-renderer.sh
```

## 技术特点

### ✅ 后端优势
- **性能优化**：服务器端渲染，减少客户端负担
- **SEO友好**：完整的HTML输出，搜索引擎友好
- **缓存支持**：可以缓存渲染结果
- **安全性**：敏感逻辑在服务器端处理

### ✅ 无头浏览器优势
- **完整渲染**：支持所有现代CSS特性
- **字体支持**：完美支持中文字体
- **布局准确**：自动文本换行和布局
- **图片生成**：高质量PNG输出

### ✅ 异步处理
- **非阻塞**：用户操作不阻塞
- **后台处理**：渲染在后台进行
- **状态管理**：完整的错误处理和状态跟踪

## 部署要求

### 系统依赖
- Go 1.19+
- Chrome/Chromium浏览器
- 足够的内存（建议4GB+）

### Docker部署
```dockerfile
# 安装Chrome
RUN apt-get update && apt-get install -y \
    chromium-browser \
    && rm -rf /var/lib/apt/lists/*
```

## 性能优化

### 1. 缓存策略
- HTML内容缓存
- 图片文件缓存
- 数据库查询缓存

### 2. 并发处理
- 多goroutine处理
- 连接池管理
- 资源限制

### 3. 监控指标
- 渲染时间
- 内存使用
- 错误率

## 错误处理

### 常见错误
1. **Chrome启动失败**：检查Chrome安装
2. **内存不足**：增加系统内存
3. **渲染超时**：调整超时设置
4. **字体加载失败**：检查字体文件

### 调试方法
```bash
# 查看日志
tail -f logs/numind.log

# 检查Chrome进程
ps aux | grep chrome

# 测试HTML生成
curl "http://localhost:8080/api/v1/books/1/html" | head -20
```

## 更新日志

### v1.0.0
- 初始版本
- 支持HTML和图片渲染
- 完整的样式规范实现
- 异步处理支持

## 总结

这个Go + 无头浏览器渲染系统提供了：

1. **🎯 严格规范**：完全按照设计规范实现
2. **⚡ 高性能**：服务器端渲染，性能优异
3. **🔄 异步体验**：不阻塞用户操作
4. **🎨 完美渲染**：支持所有现代CSS特性
5. **📱 响应式**：适配各种设备
6. **🔒 安全可靠**：完整的错误处理

这是一个完整的后端渲染解决方案，无需前端框架，直接在Go后端完成所有渲染工作！ 