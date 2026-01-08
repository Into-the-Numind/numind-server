# SOP API 架构说明

## 架构概述

SOP系统的API分为两个部分：
1. **后台管理系统API** - 管理员使用，用于管理模板和节点
2. **有数Web前端API** - 普通用户使用，用于执行SOP和查看结果

## 登录系统

### 1. 后台管理登录
```
POST /v1/admin/login
```

**用途**: 后台管理员登录

**请求**:
```json
{
  "username": "admin",
  "password": "admin123456"
}
```

**响应**:
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "token_type": "Bearer",
  "admin": {
    "id": 1,
    "username": "admin",
    "nickname": "管理员",
    "email": "admin@example.com"
  }
}
```

### 2. 有数Web登录
```
POST /v1/web/login
```

**用途**: 有数Web端用户登录

**请求**:
```json
{
  "username": "user123",
  "password": "admin123456"
}
```

**响应**:
```json
{
  "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "token_type": "Bearer",
  "user": {
    "id": 1,
    "username": "user123",
    // ... 其他用户信息
  }
}
```

---

## 后台管理系统 API

> **认证**: 需要后台管理员Token  
> **服务**: numind-admin (端口 9099)  
> **前缀**: `/v1/admin/sop/`

### 模板管理

#### 创建模板
```
POST /v1/admin/sop/templates
Authorization: Bearer <admin_token>

{
  "name": "爆款口播仿写",
  "description": "自动生成个性化口播文稿"
}
```

#### 获取模板列表
```
GET /v1/admin/sop/templates?offset=0&limit=20
Authorization: Bearer <admin_token>
```

#### 获取模板详情
```
GET /v1/admin/sop/templates/:id
Authorization: Bearer <admin_token>
```

#### 更新模板
```
PUT /v1/admin/sop/templates/:id
Authorization: Bearer <admin_token>

{
  "name": "新名称",
  "status": "inactive"
}
```

#### 删除模板
```
DELETE /v1/admin/sop/templates/:id
Authorization: Bearer <admin_token>
```

### 节点管理

#### 创建节点
```
POST /v1/admin/sop/nodes
Authorization: Bearer <admin_token>

{
  "template_id": 1,
  "parent_id": null,
  "name": "拆解产品",
  "base_url": "https://api.openai.com/v1/chat/completions",
  "model_name": "gpt-4",
  "timeout_seconds": 60,
  "sort": 1,
  "prompt": "请分析以下产品..."
}
```

#### 获取模板的节点列表
```
GET /v1/admin/sop/templates/:id/nodes
Authorization: Bearer <admin_token>
```

#### 更新节点
```
PUT /v1/admin/sop/nodes/:id
Authorization: Bearer <admin_token>

{
  "name": "新节点名称",
  "prompt": "新的提示词"
}
```

#### 删除节点
```
DELETE /v1/admin/sop/nodes/:id
Authorization: Bearer <admin_token>
```

### 执行管理（管理员视角）

#### 为用户执行模板
```
POST /v1/admin/sop/templates/:id/run
Authorization: Bearer <admin_token>

{
  "user_id": 123,
  "initial_input": "产品介绍内容..."
}
```

#### 查看所有执行记录
```
GET /v1/admin/sop/runs?offset=0&limit=20&user_id=123
Authorization: Bearer <admin_token>
```

#### 查看执行详情
```
GET /v1/admin/sop/runs/:id/detail
Authorization: Bearer <admin_token>
```

---

## 有数Web前端 API

> **认证**: 需要Web用户Token  
> **服务**: numind (端口 9091)  
> **前缀**: `/v1/sop/`  
> **权限**: 只能访问自己的数据

### 模板查询

#### 获取可用模板列表
```
GET /v1/sop/templates?offset=0&limit=20
Authorization: Bearer <user_token>
```

**说明**: 只返回status为active的模板

### SOP执行

#### 执行SOP模板
```
POST /v1/sop/templates/:id/execute
Authorization: Bearer <user_token>

{
  "initial_input": "产品介绍内容..."
}
```

**说明**: 
- user_id自动从token获取
- 立即返回run_id
- 异步执行节点流程

**响应**:
```json
{
  "code": 0,
  "data": {
    "id": 1,
    "template_id": 1,
    "user_id": 123,
    "status": "pending",
    "conversation_id": "sop_1_123_1702567890",
    "created_at": "2024-12-14T10:00:00Z"
  }
}
```

### 执行记录查询

#### 查看我的执行记录列表
```
GET /v1/sop/runs?offset=0&limit=20
Authorization: Bearer <user_token>
```

**说明**: 只返回当前用户的执行记录

#### 查看执行记录详情
```
GET /v1/sop/runs/:id
Authorization: Bearer <user_token>
```

**说明**: 只能查看自己的记录

#### 查看执行详情（包含所有节点）
```
GET /v1/sop/runs/:id/detail
Authorization: Bearer <user_token>
```

**响应**:
```json
{
  "code": 0,
  "data": {
    "run": {
      "id": 1,
      "status": "succeeded",
      "final_note_id": 5
    },
    "node_runs": [
      {
        "id": 1,
        "node_id": 1,
        "status": "succeeded",
        "input": "产品介绍...",
        "output": "产品分析结果...",
        "latency_ms": 1500,
        "sort": 0
      },
      {
        "id": 2,
        "node_id": 2,
        "status": "succeeded",
        "input": "产品分析结果...",
        "output": "文案分析结果...",
        "latency_ms": 2000,
        "sort": 1
      }
    ]
  }
}
```

### 笔记查询

#### 查看我的笔记列表
```
GET /v1/sop/notes?offset=0&limit=20
Authorization: Bearer <user_token>
```

#### 查看笔记详情
```
GET /v1/sop/notes/:id
Authorization: Bearer <user_token>
```

---

## 执行流程（基于sop-detail.html）

### 前端流程
```
用户输入产品介绍
    ↓
点击"下一步" → 调用 POST /v1/sop/templates/1/execute
    ↓
获得 run_id
    ↓
轮询 GET /v1/sop/runs/:id/detail 查看执行进度
    ↓
显示每个节点的执行结果（实时更新）
    ↓
所有节点完成后，显示最终笔记
```

### 上下文连贯性

每次执行都有独立的 `conversation_id`，格式：`sop_{template_id}_{user_id}_{timestamp}`

所有节点共享同一个conversation_id，保证：
- ✅ 上下文连贯（前序节点的对话传递给后续节点）
- ✅ 与用户其他聊天隔离（不会串线）

### 查看历史记录

用户可以随时查看：
- `GET /v1/sop/runs` - 我的所有执行记录
- `GET /v1/sop/runs/:id/detail` - 某次执行的完整详情（所有节点）
- `GET /v1/sop/notes` - 我的所有笔记

---

## 权限说明

| 操作 | 后台管理员 | 普通用户 |
|------|-----------|---------|
| 创建/编辑模板 | ✅ | ❌ |
| 创建/编辑节点 | ✅ | ❌ |
| 查看所有用户记录 | ✅ | ❌ |
| 执行SOP | ✅ | ✅ |
| 查看自己的记录 | ✅ | ✅ |
| 查看自己的笔记 | ✅ | ✅ |

---

## API对比总结

### 后台管理系统
- **登录**: `/v1/admin/login`
- **SOP管理**: `/v1/admin/sop/*`
- **权限**: 管理员token，可管理所有资源

### 有数Web前端  
- **登录**: `/v1/web/login`
- **SOP使用**: `/v1/sop/*`
- **权限**: 用户token，只能访问自己的资源

---

## 数据隔离

1. **执行记录隔离**: 用户只能查看自己的run记录
2. **笔记隔离**: 用户只能查看自己的note
3. **模板共享**: 所有用户共享模板（但只能查看active状态的）
4. **对话隔离**: 每次执行都有独立的conversation_id
