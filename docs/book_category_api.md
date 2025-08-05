# Book分类功能API文档

## 概述

Book分类功能允许用户为卡册设置分类，并在获取卡册列表时显示对应的分类信息。

## 数据库设计

### Book表字段
- `category_id`: 分类ID（外键，可为空）
- `category_name`: 分类名称（兼容旧字段）

### 关联关系
- BookM -> CategoryM (多对一)

## API接口

### 1. 设置卡册分类

**PUT** `/v1/books/{id}/category`

**请求体：**
```json
{
    "category_id": 1  // 分类ID，null表示移除分类
}
```

**响应：**
```json
{
    "code": 0,
    "message": "",
    "data": null
}
```

### 2. 获取卡册列表（包含分类信息）

**GET** `/v1/books?offset=0&limit=10`

**响应：**
```json
{
    "code": 0,
    "message": "",
    "data": {
        "total_count": 5,
        "books": [
            {
                "id": 1,
                "title": "联机时代的独立思考者：未来竞争力进化论",
                "category_id": 1,
                "category_name": "技术",
                "category": {
                    "id": 1,
                    "name": "技术",
                    "color": "#1890ff",
                    "created_at": "2024-01-01T00:00:00Z",
                    "updated_at": "2024-01-01T00:00:00Z"
                },
                "template_id": "1",
                "tags": "",
                "card_count": 7,
                "view_time": "2025-08-05T15:54:28.709+08:00",
                "image_url": "https://example.com/image.png",
                "created_at": "2025-08-05T15:54:28.717+08:00",
                "updated_at": "2025-08-05T15:54:28.785+08:00"
            }
        ]
    }
}
```

### 3. 按分类查询卡册

**GET** `/v1/books?category_id=1&offset=0&limit=10`

**响应：**
```json
{
    "code": 0,
    "message": "",
    "data": {
        "total_count": 3,
        "books": [
            {
                "id": 1,
                "title": "联机时代的独立思考者：未来竞争力进化论",
                "category_id": 1,
                "category_name": "技术",
                "category": {
                    "id": 1,
                    "name": "技术",
                    "color": "#1890ff",
                    "created_at": "2024-01-01T00:00:00Z",
                    "updated_at": "2024-01-01T00:00:00Z"
                },
                "template_id": "1",
                "tags": "",
                "card_count": 7,
                "view_time": "2025-08-05T15:54:28.709+08:00",
                "image_url": "https://example.com/image.png",
                "created_at": "2025-08-05T15:54:28.717+08:00",
                "updated_at": "2025-08-05T15:54:28.785+08:00"
            }
        ]
    }
}
```

### 4. 获取卡册详情（包含分类信息）

**GET** `/v1/books/{id}`

**响应：**
```json
{
    "code": 0,
    "message": "",
    "data": {
        "id": 1,
        "title": "联机时代的独立思考者：未来竞争力进化论",
        "category_id": 1,
        "category_name": "技术",
        "category": {
            "id": 1,
            "name": "技术",
            "color": "#1890ff",
            "created_at": "2024-01-01T00:00:00Z",
            "updated_at": "2024-01-01T00:00:00Z"
        },
        "template_id": "1",
        "tags": "",
        "card_count": 7,
        "view_time": "2025-08-05T15:54:28.709+08:00",
        "image_url": "https://example.com/image.png",
        "created_at": "2025-08-05T15:54:28.717+08:00",
        "updated_at": "2025-08-05T15:54:28.785+08:00"
    }
}
```

## 使用方法

### 1. 设置卡册分类
```bash
curl -X PUT "http://localhost:9091/v1/books/1/category" \
  -H "Authorization: Bearer your_token" \
  -H "Content-Type: application/json" \
  -d '{
    "category_id": 1
  }'
```

### 2. 移除卡册分类
```bash
curl -X PUT "http://localhost:9091/v1/books/1/category" \
  -H "Authorization: Bearer your_token" \
  -H "Content-Type: application/json" \
  -d '{
    "category_id": null
  }'
```

### 3. 获取卡册列表（包含分类信息）
```bash
curl -X GET "http://localhost:9091/v1/books?offset=0&limit=10" \
  -H "Authorization: Bearer your_token" \
  -H "Content-Type: application/json"
```

### 4. 按分类查询卡册
```bash
curl -X GET "http://localhost:9091/v1/books?category_id=1&offset=0&limit=10" \
  -H "Authorization: Bearer your_token" \
  -H "Content-Type: application/json"
```

## 功能特点

1. **分类关联**: Book与Category通过外键关联
2. **权限控制**: 只能设置自己创建的卡册分类
3. **分类验证**: 只能使用自己创建的分类
4. **分类移除**: 支持将分类设置为null来移除分类
5. **分类信息**: 在获取卡册时自动包含完整的分类信息
6. **按分类查询**: 支持按分类ID查询卡册列表

## 错误处理

- `400`: 请求参数错误
- `401`: 未提供认证令牌或令牌无效
- `403`: 无权限操作（卡册不属于当前用户或分类不属于当前用户）
- `404`: 卡册或分类不存在

## 注意事项

1. 分类ID必须属于当前用户
2. 卡册必须属于当前用户
3. 设置分类时会自动更新category_name字段
4. 移除分类时会将category_id设为null，category_name设为空字符串 