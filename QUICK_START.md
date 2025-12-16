# SOP 系统快速启动指南

## 系统架构

本系统包含两个独立的服务：

| 服务 | 端口 | 用途 | 启动命令 |
|------|------|------|---------|
| **numind** | 9091 | 用户端API | `go run cmd/numind/main.go` |
| **numind-admin** | 9099 | 后台管理API | `go run cmd/numind-admin/main.go` |

## 启动步骤

### 1. 启动后台管理服务（推荐先启动）

```bash
# 终端1
cd /Users/neozhang/go/src/github.com/Into-the-Numind/numind-server
go run cmd/numind-admin/main.go
```

服务启动后，后台管理API可用：
- 登录: `http://localhost:9099/v1/admin/login`
- 健康检查: `http://localhost:9099/healthz`

### 2. 启动用户端服务

```bash
# 终端2
cd /Users/neozhang/go/src/github.com/Into-the-Numind/numind-server
go run cmd/numind/main.go
```

服务启动后，用户端API可用：
- 登录: `http://localhost:9091/v1/web/login`
- 健康检查: `http://localhost:9091/healthz`

## 测试流程

### 步骤1：后台管理员登录

```bash
curl -X POST http://localhost:9099/v1/admin/login \
  -H "Content-Type: application/json" \
  -d '{
    "username": "admin",
    "password": "admin123456"
  }'
```

**响应**:
```json
{
  "code": 0,
  "data": {
    "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
    "token_type": "Bearer",
    "admin": {
      "id": 1,
      "username": "admin"
    }
  }
}
```

保存 token，后续请求需要使用。

### 步骤2：创建SOP模板

```bash
export ADMIN_TOKEN="你的admin_token"

curl -X POST http://localhost:9099/v1/admin/sop/templates \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "爆款口播仿写",
    "description": "AI自动生成个性化口播文稿"
  }'
```

### 步骤3：创建SOP节点

```bash
# 节点1：拆解产品
curl -X POST http://localhost:9099/v1/admin/sop/nodes \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "template_id": 1,
    "parent_id": null,
    "name": "拆解产品",
    "base_url": "https://api.openai.com/v1/chat/completions",
    "model_name": "gpt-4",
    "timeout_seconds": 60,
    "sort": 1,
    "prompt": "请分析以下产品信息..."
  }'

# 节点2：拆解爆款文案
curl -X POST http://localhost:9099/v1/admin/sop/nodes \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "template_id": 1,
    "parent_id": 1,
    "name": "拆解爆款文案",
    "base_url": "https://api.openai.com/v1/chat/completions",
    "model_name": "gpt-4",
    "timeout_seconds": 60,
    "sort": 2,
    "prompt": "请分析以下爆款文案..."
  }'

# 节点3：拆解语言风格
curl -X POST http://localhost:9099/v1/admin/sop/nodes \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "template_id": 1,
    "parent_id": 2,
    "name": "拆解语言风格",
    "base_url": "https://api.openai.com/v1/chat/completions",
    "model_name": "gpt-4",
    "timeout_seconds": 60,
    "sort": 3,
    "prompt": "请分析用户的语言风格..."
  }'

# 节点4：生成文稿
curl -X POST http://localhost:9099/v1/admin/sop/nodes \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "template_id": 1,
    "parent_id": 3,
    "name": "生成文稿",
    "base_url": "https://api.openai.com/v1/chat/completions",
    "model_name": "gpt-4",
    "timeout_seconds": 60,
    "sort": 4,
    "prompt": "请根据以上信息生成口播文稿..."
  }'
```

### 步骤4：用户登录

```bash
curl -X POST http://localhost:9091/v1/web/login \
  -H "Content-Type: application/json" \
  -d '{
    "username": "testuser",
    "password": "admin123456"
  }'
```

保存 user_token。

### 步骤5：用户执行SOP

```bash
export USER_TOKEN="你的user_token"

# 查看可用模板
curl -X GET http://localhost:9091/v1/sop/templates \
  -H "Authorization: Bearer $USER_TOKEN"

# 执行模板
curl -X POST http://localhost:9091/v1/sop/templates/1/execute \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "initial_input": "这是一款智能手表，具有健康监测、运动追踪、消息提醒等功能..."
  }'

# 响应会返回 run_id
```

### 步骤6：查看执行进度

```bash
# 查看执行详情（包含所有节点）
curl -X GET http://localhost:9091/v1/sop/runs/1/detail \
  -H "Authorization: Bearer $USER_TOKEN"

# 返回所有节点的执行状态和结果
```

### 步骤7：查看历史记录

```bash
# 查看我的执行记录
curl -X GET http://localhost:9091/v1/sop/runs \
  -H "Authorization: Bearer $USER_TOKEN"

# 查看我的笔记
curl -X GET http://localhost:9091/v1/sop/notes \
  -H "Authorization: Bearer $USER_TOKEN"
```

## API路由总览

### 后台管理服务 (9099)

**登录**:
- `POST /v1/admin/login` - 后台登录

**SOP管理** (需要admin token):
```
POST   /v1/admin/sop/templates           # 创建模板
GET    /v1/admin/sop/templates           # 查询模板列表
GET    /v1/admin/sop/templates/:id       # 查询模板详情
PUT    /v1/admin/sop/templates/:id       # 更新模板
DELETE /v1/admin/sop/templates/:id       # 删除模板

POST   /v1/admin/sop/nodes               # 创建节点
GET    /v1/admin/sop/nodes/:id           # 查询节点详情
GET    /v1/admin/sop/templates/:id/nodes # 查询模板的所有节点
PUT    /v1/admin/sop/nodes/:id           # 更新节点
DELETE /v1/admin/sop/nodes/:id           # 删除节点

POST   /v1/admin/sop/templates/:id/run   # 为用户执行模板
GET    /v1/admin/sop/runs                # 查询所有执行记录
GET    /v1/admin/sop/runs/:id            # 查询执行记录
GET    /v1/admin/sop/runs/:id/detail     # 查询执行详情

GET    /v1/admin/sop/notes/:id                   # 查询笔记
GET    /v1/admin/sop/users/:user_id/notes        # 查询用户的笔记
```

### 用户端服务 (9091)

**登录**:
- `POST /v1/web/login` - Web端登录

**SOP使用** (需要user token):
```
GET    /v1/sop/templates                 # 获取可用模板
POST   /v1/sop/templates/:id/execute     # 执行模板
GET    /v1/sop/runs                      # 我的执行记录
GET    /v1/sop/runs/:id                  # 查询执行记录
GET    /v1/sop/runs/:id/detail           # 查询执行详情
GET    /v1/sop/notes                     # 我的笔记列表
GET    /v1/sop/notes/:id                 # 查询笔记详情
```

## 常见问题

### Q: 为什么访问 /v1/admin/login 返回 404？

**A**: 你可能启动了 numind 服务（端口9091），但 `/v1/admin/login` 只在 numind-admin 服务（端口9099）中。请启动 `go run cmd/numind-admin/main.go`。

### Q: 两个服务必须都启动吗？

**A**: 
- 只测试后台管理功能：只启动 numind-admin (9099)
- 只测试用户端功能：启动 numind (9091)，但需要提前配置好模板
- 完整测试：建议都启动

### Q: 如何确认服务启动成功？

**A**: 访问健康检查接口：
```bash
# 后台管理服务
curl http://localhost:9099/healthz

# 用户端服务
curl http://localhost:9091/healthz
```

## 相关文档

- [SOP_README.md](./SOP_README.md) - SOP功能详细说明
- [SOP_API_ARCHITECTURE.md](./SOP_API_ARCHITECTURE.md) - API架构文档
- [API_SUMMARY.md](./API_SUMMARY.md) - API总览
