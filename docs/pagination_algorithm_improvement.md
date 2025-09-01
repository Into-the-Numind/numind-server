# 分页算法改进文档

## 概述

本次改进主要针对创建卡册时的分页算法，根据提供的样式规范进行了全面优化，并新增了样式配置API。

## 主要改进

### 1. 样式规范更新

根据提供的样式规范，更新了以下配置：

#### 1.1 字体大小规范
- 标题: 64rpx（最大）
- 副标题: 48rpx（中等）
- 正文: 36rpx（标准）（正文、列表）
- 引用: 36rpx（强调）
- 标签: 28rpx（最小）

#### 1.2 颜色规范
- 主标题: #333333（深灰）
- 副标题: #666666（中灰）
- 正文: #333333（深灰）
- 引用: #1E90FF（蓝色）
- 标签: #1E90FF（蓝色）

#### 1.3 对齐方式
- left - 左对齐
- center - 居中对齐
- right - 右对齐

#### 1.4 行高规范
- 正文: 1.6（标准行高）
- 引用: 1.5（紧凑行高）
- 列表: 1.5（紧凑行高）

### 2. 布局规则更新

#### 2.1 卡片尺寸
- 标准尺寸: 1080×1440（3:4比例）
- 屏幕适配: 100vw × 133.33vw

#### 2.2 内边距
- 标准内边距: 60rpx 50rpx
- 图片标题类型: 特殊处理，图片无内边距

#### 2.3 元素间距
- **副标题标准**: 以副标题的上下间距为准，保持一致性
- **副标题上间距**: 30rpx（标准间距）
- **副标题下间距**: 25rpx
- **其他元素下间距**: 30rpx（与副标题上间距保持一致）
- 标题下方: 30rpx（与副标题上间距一致）
- 副标题下方: 25rpx（标准下间距）
- 正文下方: 30rpx（与副标题上间距一致）
- 图片下方: 30rpx（与副标题上间距一致）
- 引用下方: 30rpx（与副标题上间距一致）
- 标签下方: 30rpx（与副标题上间距一致）
- 列表下方: 30rpx（与副标题上间距一致）
- 列表项间距: 8rpx（保持不变，因为是列表内部间距）

### 3. 分页算法优化

#### 3.1 基于高度的分页
- 从简单的字符数分页改为基于实际高度的分页
- 更精确的文本宽度计算
- 考虑不同字体大小和行高的影响

#### 3.2 精确的文本测量
```go
// 更精确的字符宽度计算（基于中文字符）
charWidth := float64(style.FontSize) * 1.1 // 以中文字符为准
charsPerLine := int(float64(availableWidth) / charWidth)
```

#### 3.3 高度计算优化
```go
// 计算可用高度
availableHeight := p.config.Card.Height - p.config.Card.Padding.Top - p.config.Card.Padding.Bottom

// 基于高度进行分页
if currentHeight+elementHeight > availableHeight && len(currentCardElements) > 0 {
    // 创建新卡片
}
```

## 新增API接口

### 1. 获取样式配置 API

**GET** `/v1/pagination/style-config`

响应示例：
```json
{
  "code": 0,
  "message": "",
  "data": {
    "card": {
      "width": 1080,
      "height": 1440,
      "padding": {
        "top": 60,
        "right": 50,
        "bottom": 60,
        "left": 50
      }
    },
    "styles": {
      "title": {
        "fontSize": 64,
        "lineHeight": 96,
        "marginTop": 0,
        "marginBottom": 30,
        "color": "#333333",
        "align": "left"
      },
      "subtitle": {
        "fontSize": 48,
        "lineHeight": 72,
        "marginTop": 0,
        "marginBottom": 25,
        "color": "#666666",
        "align": "left"
      },
      "body": {
        "fontSize": 36,
        "lineHeight": 58,
        "marginTop": 0,
        "marginBottom": 30,
        "color": "#333333",
        "align": "left"
      },
      "list": {
        "fontSize": 36,
        "lineHeight": 54,
        "marginTop": 0,
        "marginBottom": 30,
        "indent": 40,
        "color": "#333333",
        "align": "left"
      },
      "quote": {
        "fontSize": 36,
        "lineHeight": 54,
        "marginTop": 0,
        "marginBottom": 30,
        "color": "#1E90FF",
        "align": "left"
      },
      "tag": {
        "fontSize": 28,
        "lineHeight": 42,
        "marginTop": 0,
        "marginBottom": 20,
        "color": "#1E90FF",
        "align": "left"
      }
    },
    "rules": {
      "fontSizes": {
        "title": 64,
        "subtitle": 48,
        "body": 36,
        "quote": 36,
        "tag": 28
      },
      "colors": {
        "title": "#333333",
        "subtitle": "#666666",
        "body": "#333333",
        "quote": "#1E90FF",
        "tag": "#1E90FF"
      },
      "alignments": ["left", "center", "right"],
      "lineHeights": {
        "body": 1.6,
        "quote": 1.5,
        "list": 1.5
      },
      "spacings": {
        "title_bottom": 30,
        "subtitle_bottom": 25,
        "body_bottom": 30,
        "image_bottom": 30,
        "list_item": 8
      },
      "elementTypes": ["title", "subtitle", "body", "list", "quote", "tag"]
    }
  }
}
```

### 2. 现有API接口

#### 2.1 分页接口
**POST** `/v1/pagination/paginate`

#### 2.2 配置管理
**GET** `/v1/pagination/config` - 获取当前配置
**PUT** `/v1/pagination/config` - 更新配置

#### 2.3 测试接口
**GET** `/v1/pagination/test` - 使用示例数据测试分页功能

## 使用方法

### 1. 获取样式配置
```bash
curl -X GET "http://localhost:8080/v1/pagination/style-config" \
  -H "Authorization: Bearer your_token" \
  -H "Content-Type: application/json"
```

### 2. 执行分页
```bash
curl -X POST "http://localhost:8080/v1/pagination/paginate" \
  -H "Authorization: Bearer your_token" \
  -H "Content-Type: application/json" \
  -d '{
    "elements": [
      {
        "type": "title",
        "content": "标题内容"
      },
      {
        "type": "body",
        "content": "正文内容"
      }
    ]
  }'
```

### 3. 测试分页功能
```bash
curl -X GET "http://localhost:8080/v1/pagination/test" \
  -H "Authorization: Bearer your_token" \
  -H "Content-Type: application/json"
```

## 测试脚本

提供了完整的测试脚本 `test_pagination.sh`，包含：
- 获取配置测试
- 获取样式配置测试
- 自定义分页测试
- JSON字符串分页测试

使用方法：
```bash
chmod +x test_pagination.sh
./test_pagination.sh
```

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