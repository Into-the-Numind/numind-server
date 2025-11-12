# Numind Admin API 文档

## 概述

Numind Admin 是后台管理系统的 API 接口文档，用于管理用户、笔记（书籍）、图片、订单、支付、模板和系统配置。

**基础信息：**
- 基础 URL: `http://your-domain:9099`
- API 版本: `v1`
- 认证方式: JWT Bearer Token
- 响应格式: JSON

**统一响应格式：**
```json
{
  "code": 0,        // 0=成功，1=错误
  "message": "",    // 错误信息（成功时为空）
  "data": {}        // 响应数据
}
```

---

## 目录

1. [认证接口](#认证接口)
2. [用户管理](#用户管理)
3. [笔记（书籍）管理](#笔记书籍管理)
4. [图片管理](#图片管理)
5. [订单管理](#订单管理)
6. [支付管理](#支付管理)
7. [模板管理](#模板管理)
8. [系统配置管理](#系统配置管理)
9. [统计信息](#统计信息)
10. [健康检查](#健康检查)

---

## 认证接口

### 管理员登录

**接口地址：** `POST /v1/admin/login`

**请求头：**
```
Content-Type: application/json
```

**请求体：**
```json
{
  "username": "admin",
  "password": "admin123456"
}
```

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
    "token_type": "Bearer",
    "admin": {
      "id": 1,
      "username": "admin",
      "nickname": "管理员",
      "email": "admin@example.com"
    }
  }
}
```

**错误响应：**
```json
{
  "code": 1,
  "message": "用户名或密码错误",
  "data": null
}
```

**说明：**
- 登录成功后，需要在后续请求的 Header 中携带 Token
- Header 格式：`Authorization: Bearer {token}`
- Token 有效期：24小时（可在配置文件中修改）

---

## 用户管理

### 获取用户列表

**接口地址：** `GET /v1/admin/users`

**请求头：**
```
Authorization: Bearer {token}
```

**查询参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| offset | int | 否 | 偏移量，默认 0 |
| limit | int | 否 | 每页数量，默认 10 |

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "total_count": 100,
    "users": [
      {
        "id": 1,
        "username": "user1",
        "nickname": "用户1",
        "email": "user1@example.com",
        "phone": "13800138000",
        "status": 1,
        "created_at": "2024-01-01T00:00:00Z",
        "updated_at": "2024-01-01T00:00:00Z"
      }
    ]
  }
}
```

### 获取用户详情

**接口地址：** `GET /v1/admin/users/:name`

**路径参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| name | string | 是 | 用户名 |

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 1,
    "username": "user1",
    "nickname": "用户1",
    "email": "user1@example.com",
    "phone": "13800138000",
    "status": 1,
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  }
}
```

### 更新用户信息

**接口地址：** `PUT /v1/admin/users/:name`

**请求体：**
```json
{
  "nickname": "新昵称",
  "email": "newemail@example.com",
  "phone": "13900139000",
  "status": 1
}
```

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": null
}
```

### 删除用户

**接口地址：** `DELETE /v1/admin/users/:name`

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": null
}
```

---

## 笔记（书籍）管理

### 获取笔记列表

**接口地址：** `GET /v1/admin/books`

**查询参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| offset | int | 否 | 偏移量，默认 0 |
| limit | int | 否 | 每页数量，默认 10 |
| category_id | int | 否 | 分类ID |
| fields | string | 否 | 字段过滤，逗号分隔 |

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "total_count": 50,
    "books": [
      {
        "id": 1,
        "user_id": 1,
        "title": "笔记标题",
        "original_text": "原始文本",
        "processed_text": "处理后的文本",
        "category_id": 1,
        "category_name": "分类名称",
        "tags": "标签1,标签2",
        "keywords": ["关键词1", "关键词2"],
        "image_url": "https://example.com/image.jpg",
        "created_at": "2024-01-01T00:00:00Z",
        "updated_at": "2024-01-01T00:00:00Z"
      }
    ]
  }
}
```

### 获取笔记详情

**接口地址：** `GET /v1/admin/books/:id`

**路径参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| id | int | 是 | 笔记ID |

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 1,
    "user_id": 1,
    "title": "笔记标题",
    "original_text": "原始文本",
    "processed_text": "处理后的文本",
    "category_id": 1,
    "tags": "标签1,标签2",
    "image_url": "https://example.com/image.jpg",
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  }
}
```

### 更新笔记

**接口地址：** `PUT /v1/admin/books/:id`

**请求体：**
```json
{
  "title": "新标题",
  "category_id": 2,
  "tags": "新标签1,新标签2"
}
```

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": null
}
```

### 删除笔记

**接口地址：** `DELETE /v1/admin/books/:id`

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": null
}
```

### 批量删除笔记

**接口地址：** `DELETE /v1/admin/books`

**请求体：**
```json
{
  "ids": [1, 2, 3]
}
```

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": null
}
```

---

## 图片管理

### 获取图片列表

**接口地址：** `GET /v1/admin/images`

**查询参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| offset | int | 否 | 偏移量，默认 0 |
| limit | int | 否 | 每页数量，默认 10 |

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "total_count": 100,
    "images": [
      {
        "id": 1,
        "user_id": 1,
        "book_id": 1,
        "original_url": "https://example.com/original.jpg",
        "thumb_url": "https://example.com/thumb.jpg",
        "file_name": "image.jpg",
        "file_size": 1024000,
        "image_type": "image/jpeg",
        "width": 1920,
        "height": 1080,
        "created_at": "2024-01-01T00:00:00Z",
        "updated_at": "2024-01-01T00:00:00Z"
      }
    ]
  }
}
```

### 获取图片详情

**接口地址：** `GET /v1/admin/images/:id`

**路径参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| id | int | 是 | 图片ID |

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 1,
    "user_id": 1,
    "book_id": 1,
    "original_url": "https://example.com/original.jpg",
    "thumb_url": "https://example.com/thumb.jpg",
    "file_name": "image.jpg",
    "file_size": 1024000,
    "image_type": "image/jpeg",
    "width": 1920,
    "height": 1080,
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  }
}
```

### 更新图片信息

**接口地址：** `PUT /v1/admin/images/:id`

**请求体：**
```json
{
  "book_id": 2
}
```

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": null
}
```

### 删除图片

**接口地址：** `DELETE /v1/admin/images/:id`

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": null
}
```

---

## 订单管理

### 获取订单列表

**接口地址：** `GET /v1/admin/orders`

**查询参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| user_id | int | 否 | 用户ID |
| offset | int | 否 | 偏移量，默认 0 |
| limit | int | 否 | 每页数量，默认 20 |

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": [
    {
      "id": 1,
      "user_id": 1,
      "out_trade_no": "wx_1_1234567890",
      "amount": 10000,
      "description": "订单描述",
      "status": "pending",
      "created_at": "2024-01-01T00:00:00Z",
      "updated_at": "2024-01-01T00:00:00Z"
    }
  ]
}
```

---

## 支付管理

### 获取支付记录列表

**接口地址：** `GET /v1/admin/payments`

**查询参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| page | int | 否 | 页码，默认 1 |
| page_size | int | 否 | 每页数量，默认 20 |
| status | string | 否 | 支付状态：pending, success, failed, cancelled |
| channel | string | 否 | 支付渠道：wechat, alipay |

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "total": 100,
    "payments": [
      {
        "id": 1,
        "out_trade_no": "wx_1_1234567890",
        "transaction_id": "4200001234567890",
        "user_id": 1,
        "amount": 10000,
        "description": "商品描述",
        "channel": "wechat",
        "status": "success",
        "pay_method": "native",
        "openid": "",
        "code_url": "",
        "created_at": "2024-01-01T00:00:00Z",
        "updated_at": "2024-01-01T00:00:00Z",
        "paid_at": "2024-01-01T00:05:00Z"
      }
    ]
  }
}
```

### 获取支付记录详情

**接口地址：** `GET /v1/admin/payments/:out_trade_no`

**路径参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| out_trade_no | string | 是 | 商户订单号 |

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 1,
    "out_trade_no": "wx_1_1234567890",
    "transaction_id": "4200001234567890",
    "user_id": 1,
    "amount": 10000,
    "description": "商品描述",
    "channel": "wechat",
    "status": "success",
    "pay_method": "native",
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z",
    "paid_at": "2024-01-01T00:05:00Z"
  }
}
```

---

## 模板管理

### 获取模板列表

**接口地址：** `GET /v1/admin/templates`

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": [
    {
      "id": 1,
      "name": "模板1",
      "description": "模板描述",
      "template_id": "template1",
      "preview_url": "https://example.com/preview.jpg",
      "created_at": "2024-01-01T00:00:00Z",
      "updated_at": "2024-01-01T00:00:00Z"
    }
  ]
}
```

### 获取模板详情

**接口地址：** `GET /v1/admin/templates/:id`

**路径参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| id | int | 是 | 模板ID |

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 1,
    "name": "模板1",
    "description": "模板描述",
    "template_id": "template1",
    "preview_url": "https://example.com/preview.jpg",
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  }
}
```

### 创建模板

**接口地址：** `POST /v1/admin/templates`

**请求体：**
```json
{
  "name": "新模板",
  "description": "模板描述",
  "template_id": "new_template",
  "preview_url": "https://example.com/preview.jpg"
}
```

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 1,
    "name": "新模板",
    "description": "模板描述",
    "template_id": "new_template",
    "preview_url": "https://example.com/preview.jpg",
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  }
}
```

### 更新模板

**接口地址：** `PUT /v1/admin/templates/:id`

**请求体：**
```json
{
  "name": "更新后的模板名",
  "description": "更新后的描述"
}
```

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": null
}
```

### 删除模板

**接口地址：** `DELETE /v1/admin/templates/:id`

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": null
}
```

---

## 系统配置管理

### 获取配置列表

**接口地址：** `GET /v1/admin/configs`

**查询参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| fields | string | 否 | 字段过滤，逗号分隔 |

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": [
    {
      "id": 1,
      "key": "app.name",
      "value": "Numind",
      "description": "应用名称",
      "created_at": "2024-01-01T00:00:00Z",
      "updated_at": "2024-01-01T00:00:00Z"
    }
  ]
}
```

### 获取配置详情

**接口地址：** `GET /v1/admin/configs/:key`

**路径参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| key | string | 是 | 配置键 |

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 1,
    "key": "app.name",
    "value": "Numind",
    "description": "应用名称",
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  }
}
```

### 创建配置

**接口地址：** `POST /v1/admin/configs`

**请求体：**
```json
{
  "key": "app.version",
  "value": "1.0.0",
  "description": "应用版本"
}
```

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 2,
    "key": "app.version",
    "value": "1.0.0",
    "description": "应用版本",
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  }
}
```

### 更新配置

**接口地址：** `PUT /v1/admin/configs/:key`

**请求体：**
```json
{
  "value": "1.0.1",
  "description": "更新后的应用版本"
}
```

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 2,
    "key": "app.version",
    "value": "1.0.1",
    "description": "更新后的应用版本",
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T01:00:00Z"
  }
}
```

### 删除配置

**接口地址：** `DELETE /v1/admin/configs/:key`

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": null
}
```

### 初始化默认配置

**接口地址：** `POST /v1/admin/configs/init`

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "message": "默认配置初始化成功"
  }
}
```

---

## 统计信息

### 获取统计信息

**接口地址：** `GET /v1/admin/stats`

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "total_users": 1000,
    "total_books": 5000,
    "total_images": 10000,
    "total_orders": 2000,
    "total_payments": 1500,
    "total_revenue": 1000000
  }
}
```

---

## 健康检查

### 健康检查

**接口地址：** `GET /healthz`

**请求头：** 无需认证

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "status": "ok"
  }
}
```

---

## 错误码说明

| 错误码 | 说明 |
|--------|------|
| 0 | 成功 |
| 1 | 错误（具体错误信息在 message 字段中） |

**常见错误信息：**
- `用户名或密码错误` - 登录失败
- `用户未登录` - Token 无效或过期
- `无权访问` - 权限不足
- `参数错误` - 请求参数格式错误
- `资源不存在` - 请求的资源不存在

---

## 注意事项

1. **认证：** 除登录接口和健康检查接口外，所有接口都需要在请求头中携带 JWT Token
2. **Token 格式：** `Authorization: Bearer {token}`
3. **分页：** 列表接口支持分页，使用 `offset` 和 `limit` 参数，或 `page` 和 `page_size` 参数
4. **时间格式：** 所有时间字段使用 ISO 8601 格式：`2024-01-01T00:00:00Z`
5. **字段过滤：** 部分接口支持 `fields` 参数，用于指定返回的字段，多个字段用逗号分隔
6. **响应压缩：** 部分接口响应可能使用 gzip 压缩，客户端需要支持解压

---

## 更新日志

- 2024-01-01: 初始版本

