# 分类业务API文档

## 概述

分类业务提供了对卡册进行分类管理的功能，支持分类的增删改查操作，并且可以将卡册与分类进行绑定。

## 数据库设计

### 分类表 (category)
- `id`: 主键
- `name`: 分类名称（唯一）
- `description`: 分类描述
- `created_at`: 创建时间

### 卡册表 (books) 新增字段
- `category_id`: 分类ID（外键，可为空）
- `category_name`: 分类名称（兼容旧字段）

## API接口

### 1. 创建分类

**POST** `/v1/categories`

**请求体：**
```json
{
    "name": "技术",
    "description": "技术相关的内容"
}
```

**响应：**
```json
{
    "code": 0,
    "message": "",
    "data": {
        "id": 1,
        "name": "技术",
        "description": "技术相关的内容",
        "created_at": "2024-01-01T00:00:00Z"
    }
}
```

### 2. 获取分类列表

**GET** `/v1/categories?offset=0&limit=10`

**响应：**
```json
{
    "code": 0,
    "message": "",
    "data": {
        "totalCount": 5,
        "categories": [
            {
                "id": 1,
                "name": "技术",
                "description": "技术相关的内容",
                "created_at": "2024-01-01T00:00:00Z"
            }
        ]
    }
}
```

### 3. 获取分类详情

**GET** `/v1/categories/{id}`

**响应：**
```json
{
    "code": 0,
    "message": "",
    "data": {
        "id": 1,
        "name": "技术",
        "description": "技术相关的内容",
        "created_at": "2024-01-01T00:00:00Z"
    }
}
```

### 4. 更新分类

**PUT** `/v1/categories/{id}`

**请求体：**
```json
{
    "name": "技术更新",
    "description": "更新后的技术相关描述"
}
```

### 5. 删除分类

**DELETE** `/v1/categories/{id}`

**响应：**
```json
{
    "code": 0,
    "message": "",
    "data": null
}
```

## 卡册与分类关联

### 按分类查询卡册

**GET** `/v1/books?category_id=1&offset=0&limit=10`

**响应：**
```json
{
    "code": 0,
    "message": "",
    "data": {
        "totalCount": 3,
        "books": [
            {
                "id": 1,
                "title": "Go语言学习笔记",
                "description": "Go语言学习过程中的笔记",
                "category_id": 1,
                "category_name": "技术",
                "category_info": {
                    "id": 1,
                    "name": "技术",
                    "description": "技术相关的内容"
                }
            }
        ]
    }
}
```

### 创建带分类的卡册

**POST** `/v1/books`

**请求体：**
```json
{
    "user_id": 1,
    "title": "Go语言学习笔记",
    "description": "Go语言学习过程中的笔记",
    "category_id": 1,
    "content": "卡册内容",
    "status": "published"
}
```

## 业务逻辑

1. **分类唯一性**：分类名称必须唯一，创建重复名称的分类会返回错误
2. **级联删除**：删除分类时，相关卡册的category_id会被设置为NULL
3. **兼容性**：保留了原有的category字符串字段，确保向后兼容
4. **预加载**：查询卡册时会自动预加载分类信息

## 错误码

- `ErrBind`: 请求参数绑定失败
- `ErrInvalidParameter`: 参数验证失败
- `ErrRecordNotFound`: 记录不存在
- `ErrDatabase`: 数据库操作失败

## 使用示例

### 创建分类并绑定卡册

```bash
# 1. 创建分类
curl -X POST http://localhost:9091/v1/categories \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "学习笔记",
    "description": "学习过程中的笔记整理"
  }'

# 2. 创建带分类的卡册
curl -X POST http://localhost:9091/v1/books \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": 1,
    "title": "Go语言学习笔记",
    "description": "Go语言学习过程中的笔记",
    "category_id": 1,
    "content": "卡册内容",
    "status": "published"
  }'

# 3. 按分类查询卡册
curl -X GET "http://localhost:9091/v1/books?category_id=1&offset=0&limit=10" \
  -H "Authorization: Bearer YOUR_TOKEN"
``` 