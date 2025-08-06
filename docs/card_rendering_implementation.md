# 后端卡片渲染功能实现

## 概述

本功能实现了在创建book时，后端自动将卡片内容渲染为图片的功能。这样前端就可以直接使用渲染好的图片来展示内容，无需在前端进行复杂的文本渲染。

## 功能特点

### 1. 自动渲染
- 在创建book时，每个卡片会自动渲染为图片
- 渲染过程在后台进行，不影响用户创建体验
- 支持多种内容类型：标题、副标题、正文、列表、引用

### 2. 样式规范
严格按照前端渲染规范进行样式设置：

#### 字体大小
- 标题: 64rpx
- 副标题: 48rpx  
- 正文: 36rpx
- 列表: 36rpx
- 引用: 36rpx

#### 颜色规范
- 主标题: #333333
- 副标题: #666666
- 正文: #333333
- 引用: #1E90FF

#### 布局规范
- 卡片尺寸: 100vw × 133.33vw (3:4比例)
- 内边距: 上下60rpx，左右50rpx
- 元素间距: 按规范设置

### 3. 特殊渲染规则

#### 引用样式
- 背景: 渐变背景
- 左边框: 4px蓝色装饰条
- 文字: 斜体样式

#### 列表样式
- 每项前添加项目符号(•)
- 统一的左侧缩进
- 项目间距: 8rpx

## 技术实现

### 1. 渲染引擎
使用Go的image包实现图片渲染：

```go
// 创建图片
img := image.NewRGBA(image.Rect(0, 0, width, height))

// 设置背景色
draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)

// 渲染文本
d := &font.Drawer{
    Dst:  img,
    Src:  image.NewUniform(textColor),
    Face: basicfont.Face7x13,
    Dot:  point,
}
d.DrawString(text)
```

### 2. 数据流程

1. **创建卡片记录** → 在数据库中创建CardM记录
2. **渲染卡片图片** → 使用渲染器将卡片内容渲染为图片
3. **保存图片文件** → 将渲染的图片保存到指定目录
4. **更新卡片记录** → 将图片URL保存到RenderedImage字段
5. **返回完整数据** → 包含原始数据和渲染图片URL

### 3. 数据库结构

```sql
-- 为cards表添加rendered_image字段
ALTER TABLE cards ADD COLUMN rendered_image VARCHAR(255) COMMENT '渲染后的卡片图片URL';
```

## 图片保存路径

### 1. 路径结构
根据配置的`image_path`动态设置保存路径：

```
{image_path}/card/{card_id}/card_{card_id}.png
{image_path}/book/{book_id}/book_{book_id}.png
```

### 2. 配置示例
```yaml
# 生产环境
resource:
  image_path: "/opt/numind/image/upload"

# 开发环境
resource:
  image_path: "./images/upload"
```

### 3. 实际路径示例
- 卡片图片: `/opt/numind/image/upload/card/18/card_18.png`
- 书籍图片: `/opt/numind/image/upload/book/11/book_11.png`

### 4. URL构建
- 卡片URL: `/opt/numind/card/18/card_18.png`
- 书籍URL: `/opt/numind/book/11/book_11.png`

## API接口

### 1. 创建book时自动渲染
创建book时，每个卡片会自动渲染为图片，返回的数据结构包含：

```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 11,
    "title": "联机时代的独立思考者：未来竞争力的进化之路",
    "image_url": "https://...",
    "cards": [
      {
        "id": 18,
        "process_text": [...],
        "rendered_image": "/opt/numind/card/18/card_18.png",
        "sort_order": 1
      }
    ]
  }
}
```

### 2. 手动渲染API

#### 渲染单个卡片
```http
POST /api/v1/cards/render
Content-Type: application/json

{
  "card_id": 18
}
```

#### 渲染书籍所有卡片
```http
POST /api/v1/cards/render/book/{book_id}
```

## 前端使用方式

### 1. 直接使用渲染图片
```javascript
// 获取book数据
const bookData = await fetchBook(bookId);

// 渲染封面页
const coverImage = bookData.image_url;
const coverTitle = bookData.title;

// 渲染内容页
bookData.cards.forEach(card => {
  if (card.rendered_image) {
    // 直接使用渲染好的图片
    const cardImage = card.rendered_image;
    // 显示图片
  } else {
    // 降级到原始文本渲染
    const cardData = card.process_text;
    // 前端渲染文本
  }
});
```

### 2. 异步体验
- 用户创建book后立即返回，无需等待渲染完成
- 渲染过程在后台进行
- 前端可以通过轮询或WebSocket获取渲染进度

## 配置说明

### 1. 输出目录
渲染的图片保存在配置的`image_path`目录下：
- 卡片图片: `{image_path}/card/{card_id}/`
- 书籍图片: `{image_path}/book/{book_id}/`

### 2. 图片格式
- 格式: PNG
- 质量: 无损压缩
- 命名: `card_{card_id}.png` 或 `book_{book_id}.png`

### 3. 访问路径
图片通过构建的URL路径访问：
- 卡片: `{base_path}/card/{card_id}/card_{card_id}.png`
- 书籍: `{base_path}/book/{book_id}/book_{book_id}.png`

## 错误处理

### 1. 渲染失败
- 渲染失败不影响book创建流程
- 记录错误日志，便于排查问题
- 前端可以降级到文本渲染

### 2. 文件存储失败
- 检查目录权限
- 确保磁盘空间充足
- 提供手动重新渲染的API

## 性能优化

### 1. 异步渲染
- 渲染过程在goroutine中进行
- 不阻塞主流程
- 支持并发渲染多个卡片

### 2. 缓存机制
- 已渲染的图片不会重复渲染
- 支持手动重新渲染
- 图片文件可以配置CDN加速

## 测试验证

### 1. 功能测试
```bash
# 创建book测试
curl -X POST http://localhost:8080/api/v1/books \
  -H "Content-Type: application/json" \
  -d '{
    "text": "测试内容",
    "template_id": "1"
  }'

# 手动渲染测试
curl -X POST http://localhost:8080/api/v1/cards/render \
  -H "Content-Type: application/json" \
  -d '{"card_id": 18}'
```

### 2. 图片验证
- 检查生成的图片文件
- 验证图片尺寸和内容
- 测试不同内容类型的渲染效果

## 部署注意事项

### 1. 目录权限
确保配置的`image_path`目录有写入权限

### 2. 磁盘空间
监控磁盘空间使用情况，定期清理旧图片

### 3. 性能监控
监控渲染耗时和成功率，优化渲染算法

## 未来扩展

### 1. 更多样式支持
- 支持更多字体
- 添加图片背景
- 支持动画效果

### 2. 云端渲染
- 支持云端图片生成
- 集成第三方渲染服务
- 支持更多图片格式

### 3. 智能优化
- 根据内容自动调整布局
- 智能文本换行
- 自适应字体大小 