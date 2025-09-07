# 卡册创建状态管理流程

## 状态定义

卡册创建过程中会经历以下状态：

1. **creating** - 初始创建状态
2. **ai** - 等待语言大模型返回
3. **render** - 正在渲染
4. **success** - 最终成功
5. **failed** - 失败

## 状态流转

```
creating → ai → render → success
    ↓        ↓       ↓
  failed   failed  failed
```

## 详细流程

### 1. 初始创建 (creating)
- 用户提交卡册创建请求
- 立即创建book记录，状态设为`creating`
- 返回book记录给前端，用户可以立即看到

### 2. AI处理 (ai)
- 更新状态为`ai`
- 调用语言大模型（阿里千问/火山引擎）
- 处理用户输入的文本，生成markdown格式内容
- 解析AI返回的markdown内容和图片提示词

### 3. 渲染处理 (render)
- 更新状态为`render`
- 调用文生图大模型生成封面图片
- 使用markdown渲染器处理内容
- 创建卡片记录并生成图片

### 4. 完成状态
- **success**: 所有处理完成，卡册创建成功
- **failed**: 任何步骤失败，记录错误信息

## 前端显示逻辑

前端可以根据book.status字段显示不同的状态：

- `creating`: 显示"创建中..."
- `ai`: 显示"AI处理中..."
- `render`: 显示"渲染中..."
- `success`: 显示卡册内容
- `failed`: 显示错误信息

## 实现要点

1. **异步处理**: 卡册创建是异步的，用户提交后立即返回
2. **状态更新**: 每个关键步骤都会更新状态
3. **错误处理**: 任何步骤失败都会更新为failed状态
4. **用户统计**: 状态变化时会更新用户统计信息

## 代码修改总结

### 1. 模型层修改 (`internal/pkg/model/book.go`)
```go
const (
    BookStatusCreating = "creating" // 创建中
    BookStatusAI       = "ai"       // 等待AI处理
    BookStatusRender   = "render"   // 正在渲染
    BookStatusSuccess  = "success"  // 创建成功
    BookStatusFailed   = "failed"   // 创建失败
)
```

### 2. 业务层修改 (`internal/numind/biz/book/async_processor.go`)

#### AI处理前状态更新
```go
// 🚀 第一步：更新状态为AI处理中
log.C(ctx).Infow("🚀 更新状态为AI处理中", "book_id", bookID)
if err := p.updateBookStatus(ctx, bookID, model.BookStatusAI, ""); err != nil {
    log.C(ctx).Errorw("Failed to update book status to AI processing", "book_id", bookID, "error", err.Error())
}
```

#### 渲染前状态更新
```go
// 🎨 第四步：更新状态为渲染中
log.C(ctx).Infow("🎨 更新状态为渲染中", "book_id", bookID)
if err := p.updateBookStatus(ctx, bookID, model.BookStatusRender, ""); err != nil {
    log.C(ctx).Errorw("Failed to update book status to rendering", "book_id", bookID, "error", err.Error())
}
```

## 状态更新时机

1. **creating** → **ai**: 在调用AI模型之前
2. **ai** → **render**: 在开始渲染处理之前
3. **render** → **success**: 在渲染完成之后
4. 任何步骤失败 → **failed**: 在错误发生时

这样的状态管理让用户能够清楚地看到卡册创建的进度，提升了用户体验。
