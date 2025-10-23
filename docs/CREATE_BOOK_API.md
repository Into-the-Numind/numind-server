# 创建笔记API使用说明

## POST /v1/books

创建笔记接口，支持文字输入和可选的图片上传。

### 请求格式
- **Content-Type**: `multipart/form-data`
- **认证**: 需要JWT Token

### 请求参数

| 参数名 | 类型 | 必填 | 说明 |
|--------|------|------|------|
| text | string | 是 | 用户输入的文字内容（包含OCR结果） |
| images | file[] | 否 | 图片文件，支持多文件上传 |
| files | file[] | 否 | 图片文件（兼容字段名），支持多文件上传 |

**注意**: `images` 和 `files` 字段名都支持，建议使用 `images`。

### 请求示例

#### 1. 仅文字（无图片）
```bash
curl -X POST http://localhost:9091/v1/books \
  -H "Content-Type: multipart/form-data" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -F "text=这是我的笔记内容"
```

#### 2. 文字+图片（使用images字段）
```bash
curl -X POST http://localhost:9091/v1/books \
  -H "Content-Type: multipart/form-data" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -F "text=这是我的笔记内容" \
  -F "images=@image1.jpg" \
  -F "images=@image2.png"
```

#### 3. 文字+图片（使用files字段）
```bash
curl -X POST http://localhost:9091/v1/books \
  -H "Content-Type: multipart/form-data" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -F "text=这是我的笔记内容" \
  -F "files=@image1.jpg" \
  -F "files=@image2.png"
```

### 响应格式

```json
{
  "code": 0,
  "message": "OK",
  "data": {
    "id": 123,
    "user_id": 1,
    "title": "AI生成笔记 - 2025-10-23 23:20:00",
    "original_text": "这是我的笔记内容",
    "processed_text": "",
    "status": "creating",
    "created_at": "2025-10-23T23:20:00Z",
    "updated_at": "2025-10-23T23:20:00Z"
  }
}
```

### 处理流程

1. **立即响应**: API立即返回book记录，状态为"creating"
2. **异步处理**: 后台异步处理以下任务：
   - 如果有图片：上传到COS并创建ImageM记录
   - AI处理：调用大模型处理文字内容
   - 更新状态：处理完成后更新为"success"或"failed"

### 注意事项

- 图片上传是可选的，可以只上传文字
- 支持多张图片同时上传
- 图片格式支持：JPEG, JPG, PNG, WebP
- 单张图片大小限制：10MB
- 处理过程是异步的，可以通过GET /v1/books/:id查看处理状态
