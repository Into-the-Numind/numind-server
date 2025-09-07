# Template API 文档

## 概述

Template API 提供了模板管理的完整功能，包括创建、查询、更新和删除模板。

## 基础信息

- **Base URL**: `/v1`
- **认证**: 需要 Bearer Token
- **Content-Type**: `application/json`

## API 端点

### 1. 创建模板

**POST** `/v1/templates`

创建新的模板。

#### 请求参数

```json
{
  "name": "模板名称",
  "file": "模板文件内容",
  "is_member_only": false
}
```

#### 响应示例

```json
{
  "code": 0,
  "message": "OK",
  "data": {
    "id": 1,
    "name": "模板名称",
    "file": "模板文件内容",
    "is_member_only": false,
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  }
}
```

### 2. 获取模板列表

**GET** `/v1/templates`

获取模板列表，支持分页。

#### 查询参数

- `offset` (可选): 偏移量，默认 0
- `limit` (可选): 每页数量，默认 10

#### 请求示例

```
GET /v1/templates?offset=0&limit=10
```

#### 响应示例

```json
{
  "code": 0,
  "message": "OK",
  "data": {
    "total": 100,
    "items": [
      {
        "id": 1,
        "name": "模板名称1",
        "file": "模板文件内容1",
        "is_member_only": false,
        "created_at": "2024-01-01T00:00:00Z",
        "updated_at": "2024-01-01T00:00:00Z"
      },
      {
        "id": 2,
        "name": "模板名称2",
        "file": "模板文件内容2",
        "is_member_only": true,
        "created_at": "2024-01-01T00:00:00Z",
        "updated_at": "2024-01-01T00:00:00Z"
      }
    ]
  }
}
```

### 3. 获取模板详情

**GET** `/v1/templates/{id}`

根据 ID 获取模板详情。

#### 路径参数

- `id`: 模板 ID

#### 请求示例

```
GET /v1/templates/1
```

#### 响应示例

```json
{
  "code": 0,
  "message": "OK",
  "data": {
    "id": 1,
    "name": "模板名称",
    "file": "模板文件内容",
    "is_member_only": false,
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  }
}
```

### 4. 更新模板

**PUT** `/v1/templates/{id}`

更新指定模板的信息。

#### 路径参数

- `id`: 模板 ID

#### 请求参数

```json
{
  "name": "更新后的模板名称",
  "file": "更新后的模板文件内容",
  "is_member_only": true
}
```

#### 响应示例

```json
{
  "code": 0,
  "message": "OK",
  "data": {
    "id": 1,
    "name": "更新后的模板名称",
    "file": "更新后的模板文件内容",
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  }
}
```

### 5. 删除模板

**DELETE** `/v1/templates/{id}`

删除指定的模板。

#### 路径参数

- `id`: 模板 ID

#### 请求示例

```
DELETE /v1/templates/1
```

#### 响应示例

```json
{
  "code": 0,
  "message": "OK",
  "data": {
    "message": "Template deleted successfully"
  }
}
```

## 错误码

| 错误码 | 说明 |
|--------|------|
| 10001 | 参数绑定错误 |
| 10002 | 参数验证错误 |
| 10003 | 记录不存在 |
| 10004 | 数据库操作错误 |

## 数据模型

### Template

```go
type Template struct {
    gorm.Model
    Name         string `gorm:"size:50;uniqueIndex" json:"name" valid:"required,length(1|50)"`
    File         string `gorm:"type:text" json:"file" valid:"required"`
    IsMemberOnly bool   `gorm:"default:false;not null" json:"is_member_only"`
}
```

#### 字段说明

- `id`: 主键 ID
- `name`: 模板名称，必填，最大长度 50 字符，唯一
- `file`: 模板文件内容，必填，文本类型
- `is_member_only`: 是否仅会员可用，布尔类型，默认false
- `created_at`: 创建时间
- `updated_at`: 更新时间
- `deleted_at`: 删除时间（软删除）

## 注意事项

1. 所有接口都需要认证，请在请求头中添加 `Authorization: Bearer <token>`
2. 模板名称必须唯一
3. 模板名称长度限制为 1-50 字符
4. 模板文件内容为必填项
5. `is_member_only` 字段用于区分模板是否仅会员可用，默认为 `false`
6. 删除操作为软删除，不会真正从数据库中删除记录 