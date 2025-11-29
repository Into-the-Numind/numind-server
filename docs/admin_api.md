# Numind Admin API 文档

## 概述

Numind Admin 是后台管理系统的 API 接口文档，用于管理用户、笔记、图片、订单、支付、模板、系统配置、会话和聊天记录等。

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
3. [笔记管理](#笔记管理)
4. [会话和聊天记录管理](#会话和聊天记录管理)
5. [图片管理](#图片管理)
6. [订单管理](#订单管理)
7. [支付管理](#支付管理)
8. [模板管理](#模板管理)
9. [系统配置管理](#系统配置管理)
10. [统计信息](#统计信息)
11. [反馈管理](#反馈管理)
12. [健康检查](#健康检查)

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

### 管理员登出

**接口地址：** `POST /v1/admin/logout`

**请求头：**
```
Authorization: Bearer {token}
```

**响应示例：**
```json
{
  "code": 0,
  "message": "登出成功",
  "data": null
}
```

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

## 笔记管理

### 获取笔记列表

**接口地址：** `GET /v1/admin/books`

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
        "book_type": "text",
        "status": "success",
        "ai_polish": 1,
        "card_count": 10,
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
  "message": "获取笔记详情成功",
  "data": {
    "id": 1,
    "user_id": 1,
    "title": "笔记标题",
    "original_text": "原始文本",
    "processed_text": "处理后的文本",
    "category_id": 1,
    "category_name": "分类名称",
    "tags": "标签1,标签2",
    "image_url": "https://example.com/image.jpg",
    "book_type": "text",
    "status": "success",
    "ai_polish": 1,
    "card_count": 10,
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z",
    "images": [
      {
        "id": 1,
        "original_url": "https://example.com/original.jpg",
        "thumb_url": "https://example.com/thumb.jpg",
        "file_name": "image.jpg",
        "file_size": 1024000,
        "image_type": "image/jpeg",
        "width": 1920,
        "height": 1080,
        "status": "success"
      }
    ]
  }
}
```

### 获取笔记的会话列表

**接口地址：** `GET /v1/admin/books/:id/sessions`

**路径参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| id | int | 是 | 笔记ID |

**查询参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| offset | int | 否 | 偏移量，默认 0 |
| limit | int | 否 | 每页数量，默认 10 |

**响应示例：**
```json
{
  "code": 0,
  "message": "获取会话列表成功",
  "data": {
    "total": 5,
    "sessions": [
      {
        "id": 1,
        "created_at": "2024-01-01T00:00:00Z",
        "updated_at": "2024-01-01T00:00:00Z",
        "deleted_at": null,
        "user_id": 1,
        "book_id": 10,
        "title": "笔记标题 - AI对话",
        "status": "active",
        "message_count": 10,
        "user": {
          "id": 1,
          "username": "user1",
          "nickname": "用户1"
        }
      }
    ],
    "offset": 0,
    "limit": 10
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

## 会话和聊天记录管理

### 根据会话ID获取聊天记录

**接口地址：** `GET /v1/admin/sessions/:session_id/messages`

**路径参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| session_id | int | 是 | 会话ID |

**响应示例：**
```json
{
  "code": 0,
  "message": "获取聊天记录成功",
  "data": {
    "id": 1,
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z",
    "deleted_at": null,
    "user_id": 1,
    "book_id": 10,
    "title": "笔记标题 - AI对话",
    "status": "active",
    "message_count": 10,
    "user": {
      "id": 1,
      "username": "user1",
      "nickname": "用户1"
    },
    "book": {
      "id": 10,
      "title": "笔记标题",
      "user_id": 1
    },
    "messages": [
      {
        "id": 1,
        "created_at": "2024-01-01T00:00:00Z",
        "updated_at": "2024-01-01T00:00:00Z",
        "deleted_at": null,
        "session_id": 1,
        "user_id": 1,
        "role": "user",
        "content": "用户消息内容",
        "status": "sent"
      },
      {
        "id": 2,
        "created_at": "2024-01-01T00:00:01Z",
        "updated_at": "2024-01-01T00:00:01Z",
        "deleted_at": null,
        "session_id": 1,
        "user_id": 1,
        "role": "assistant",
        "content": "AI回复内容",
        "status": "sent"
      }
    ]
  }
}
```

**错误响应：**
```json
{
  "code": 1,
  "message": "会话不存在",
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
| user_id | int | 否 | 用户ID |
| book_id | int | 否 | 笔记ID |
| status | string | 否 | 状态 |
| keyword | string | 否 | 关键词搜索 |

**响应示例：**
```json
{
  "code": 0,
  "message": "获取图片列表成功",
  "data": {
    "items": [
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
        "status": "success",
        "created_at": "2024-01-01T00:00:00Z",
        "updated_at": "2024-01-01T00:00:00Z"
      }
    ],
    "total": 100,
    "offset": 0,
    "limit": 10
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
    "status": "success",
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
| offset | int | 否 | 偏移量，默认 0 |
| limit | int | 否 | 每页数量，默认 10 |
| user_id | int | 否 | 用户ID |
| status | string | 否 | 支付状态：pending, success, failed, cancelled |
| channel | string | 否 | 支付渠道：wechat, alipay |
| start_date | string | 否 | 开始日期（格式：YYYY-MM-DD） |
| end_date | string | 否 | 结束日期（格式：YYYY-MM-DD） |

**响应示例：**
```json
{
  "code": 0,
  "message": "获取支付记录列表成功",
  "data": {
    "items": [
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
    ],
    "total": 100,
    "offset": 0,
    "limit": 10
  }
}
```

### 获取支付记录详情

**接口地址：** `GET /v1/admin/payments/:out_trade_no`

**路径参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| out_trade_no | string | 是 | 商户订单号（也支持通过ID查询） |

**响应示例：**
```json
{
  "code": 0,
  "message": "获取支付记录成功",
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

### 创建系统配置

**接口地址：** `POST /v1/admin/system-configs`

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

### 获取系统配置列表

**接口地址：** `GET /v1/admin/system-configs`

**查询参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| page | int | 否 | 页码，默认 1 |
| page_size | int | 否 | 每页数量，默认 20 |

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "items": [
      {
        "id": 1,
        "key": "app.name",
        "value": "Numind",
        "description": "应用名称",
        "created_at": "2024-01-01T00:00:00Z",
        "updated_at": "2024-01-01T00:00:00Z"
      }
    ],
    "total": 100,
    "page": 1,
    "page_size": 20
  }
}
```

### 获取单个系统配置

**接口地址：** `GET /v1/admin/system-configs/:key`

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

### 更新系统配置

**接口地址：** `PUT /v1/admin/system-configs/:key`

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

### 删除系统配置

**接口地址：** `DELETE /v1/admin/system-configs/:key`

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": null
}
```

### 初始化默认配置

**接口地址：** `POST /v1/admin/system-configs/init`

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
  "message": "获取统计信息成功",
  "data": {
    "total_users": 1000,
    "total_articles": 500,
    "total_categories": 10,
    "total_proxies": 5,
    "total_feedbacks": 20
  }
}
```

### 获取仪表板统计信息

**接口地址：** `GET /v1/admin/dashboard/stats`

**响应示例：**
```json
{
  "code": 0,
  "message": "获取仪表板统计信息成功",
  "data": {
    "total_users": 1000,
    "new_users_today": 10,
    "total_notes": 5000,
    "new_notes_today": 50,
    "total_payments": 1500,
    "revenue_today": 10000
  }
}
```

### 获取用户增长趋势

**接口地址：** `GET /v1/admin/dashboard/user-trend`

**查询参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| period | string | 否 | 时间范围：week（本周）、month（本月）、year（今年），默认 month |

**说明：**
- `week`: 从本周一开始，按日统计，显示格式：1日、2日...
- `month`: 从本月1日开始，按日统计，显示格式：1日、2日...
- `year`: 从1月1日开始，按月统计，显示格式：1月、2月...

**响应示例：**
```json
{
  "code": 0,
  "message": "获取用户增长趋势成功",
  "data": [
    {
      "date": "1日",
      "count": 5
    },
    {
      "date": "2日",
      "count": 3
    },
    {
      "date": "3日",
      "count": 8
    }
  ]
}
```

### 获取笔记增长趋势

**接口地址：** `GET /v1/admin/dashboard/book-trend`

**查询参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| period | string | 否 | 时间范围：week（本周）、month（本月）、year（今年），默认 month |

**说明：**
- `week`: 从本周一开始，按日统计，显示格式：1日、2日...
- `month`: 从本月1日开始，按日统计，显示格式：1日、2日...
- `year`: 从1月1日开始，按月统计，显示格式：1月、2月...

**响应示例：**
```json
{
  "code": 0,
  "message": "获取笔记增长趋势成功",
  "data": [
    {
      "date": "1月",
      "count": 422
    },
    {
      "date": "2月",
      "count": 0
    },
    {
      "date": "3月",
      "count": 0
    }
  ]
}
```

---

## 反馈管理

### 创建反馈

**接口地址：** `POST /v1/admin/feedbacks`

**请求体：**
```json
{
  "user_id": 1,
  "content": "反馈内容",
  "type": "bug",
  "status": 0
}
```

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 1,
    "user_id": 1,
    "content": "反馈内容",
    "type": "bug",
    "status": 0,
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  }
}
```

### 获取反馈列表

**接口地址：** `GET /v1/admin/feedbacks`

**查询参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| offset | int | 否 | 偏移量，默认 0 |
| limit | int | 否 | 每页数量，默认 10 |
| user_id | int | 否 | 用户ID |
| status | int | 否 | 状态 |
| feedback_type | string | 否 | 反馈类型 |

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "total": 100,
    "items": [
      {
        "id": 1,
        "user_id": 1,
        "content": "反馈内容",
        "type": "bug",
        "status": 0,
        "created_at": "2024-01-01T00:00:00Z",
        "updated_at": "2024-01-01T00:00:00Z"
      }
    ],
    "offset": 0,
    "limit": 10
  }
}
```

### 获取单个反馈

**接口地址：** `GET /v1/admin/feedbacks/:id`

**路径参数：**
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| id | int | 是 | 反馈ID |

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 1,
    "user_id": 1,
    "content": "反馈内容",
    "type": "bug",
    "status": 0,
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  }
}
```

### 更新反馈

**接口地址：** `PUT /v1/admin/feedbacks/:id`

**请求体：**
```json
{
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

### 删除反馈

**接口地址：** `DELETE /v1/admin/feedbacks/:id`

**响应示例：**
```json
{
  "code": 0,
  "message": "",
  "data": null
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
- `笔记ID格式错误` - 笔记ID参数格式不正确
- `会话ID格式错误` - 会话ID参数格式不正确
- `会话不存在` - 指定的会话不存在

---

## 注意事项

1. **认证：** 除登录接口和健康检查接口外，所有接口都需要在请求头中携带 JWT Token
2. **Token 格式：** `Authorization: Bearer {token}`
3. **分页：** 列表接口支持分页，使用 `offset` 和 `limit` 参数，或 `page` 和 `page_size` 参数
4. **时间格式：** 所有时间字段使用 ISO 8601 格式：`2024-01-01T00:00:00Z`
5. **路由顺序：** 注意 `/books/:id/sessions` 路由需要放在 `/books/:id` 之前，避免路由冲突
6. **笔记类型：** `book_type` 字段可能的值：`text`（只带文字）、`text_with_image`（带图带文字）、`todo`（待办）、`done`（已完成）

---

## 更新日志

- 2024-12-XX: 初始版本，包含所有基础管理功能
- 2024-12-XX: 新增笔记会话列表和聊天记录查询 API
- 2024-12-XX: 修复笔记增长趋势和用户增长趋势的 SQL 查询问题
- 2024-12-XX: 在笔记列表和详情 API 中返回 `book_type` 字段

