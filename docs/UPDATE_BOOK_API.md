# 编辑笔记API使用说明

## PUT /v1/books/:id

编辑笔记接口，支持更新标题和内容。

**注意**: 还有一个专门的接口 `PUT /v1/books/:id/content` 仅用于更新内容，如需同时更新标题和内容，请使用此接口。

### 请求格式
- **Content-Type**: `application/json`
- **认证**: 需要JWT Token

### 请求参数

| 参数名 | 类型 | 必填 | 说明 |
|--------|------|------|------|
| title | string | 是 | 笔记标题（1-255字符） |
| text | string | 否 | 用户输入的文字内容，用于更新processed_text |

### 请求示例

#### 1. 仅更新标题
```bash
curl -X PUT http://localhost:9091/v1/books/123 \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -d '{
    "title": "更新后的标题"
  }'
```

#### 2. 更新标题和内容
```bash
curl -X PUT http://localhost:9091/v1/books/123 \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -d '{
    "title": "更新后的标题",
    "text": "这是更新后的笔记内容，支持markdown格式"
  }'
```

### 响应格式

#### 成功响应
```json
{
  "code": 0,
  "message": "OK",
  "data": null
}
```

#### 错误响应
```json
{
  "code": 1,
  "message": "Invalid request parameters",
  "data": null
}
```

### 权限说明

- 用户只能编辑自己的笔记
- 需要有效的JWT Token
- 笔记必须存在

### 注意事项

- `title`字段是必填的
- `text`字段是可选的，如果提供则会更新`processed_text`
- 更新操作会自动更新`view_time`字段为当前时间
- 支持markdown格式的内容

## 接口对比

| 接口 | 用途 | 必填字段 | 可选字段 |
|------|------|----------|----------|
| `PUT /v1/books/:id` | 更新标题和内容 | title | text |
| `PUT /v1/books/:id/content` | 仅更新内容 | processed_text | 无 |

**推荐使用**: `PUT /v1/books/:id` 接口，功能更全面，可以同时更新标题和内容。
