# SOP字段更新说明

## 更新概述

本次更新为SOP系统添加了两个重要字段：

1. **SopTemplate.Prompt** - 模板预处理提示词
2. **SopNode.APIKey** - 节点API密钥

## 详细说明

### 1. SopTemplate.Prompt（模板预处理提示词）

**字段信息**:
- 字段名: `prompt`
- 类型: `text`
- 可选: 是
- 位置: `sop_template` 表

**用途**:
在执行第一个节点之前，会将此提示词作为 `system` 角色消息添加到对话历史中，用于设置整个SOP流程的全局上下文。

**使用场景**:
```
模板：爆款口播仿写
Prompt: "你是一位专业的内容创作助手，精通小红书口播文案创作。接下来你将帮助用户分析产品、学习爆款结构、提炼语言风格，最终创作出个性化的口播文稿。请保持专业、友好的态度，提供高质量的分析和建议。"

执行时对话历史：
1. [system] 你是一位专业的内容创作助手...（模板prompt）
2. [user] 请分析这个产品...（节点1输入）
3. [assistant] 产品分析结果...（节点1输出）
4. [user] 请分析这个爆款文案...（节点2输入）
5. [assistant] 文案分析结果...（节点2输出）
...
```

**API示例**:

创建模板:
```bash
POST /v1/admin/sop/templates
{
  "name": "爆款口播仿写",
  "description": "自动生成个性化口播文稿",
  "prompt": "你是一位专业的内容创作助手，接下来你将帮助用户完成一系列创作任务。"
}
```

更新模板:
```bash
PUT /v1/admin/sop/templates/1
{
  "prompt": "更新后的预处理提示词"
}
```

### 2. SopNode.APIKey（节点API密钥）

**字段信息**:
- 字段名: `api_key`
- 类型: `string(255)`
- 可选: 是
- 位置: `sop_node` 表

**用途**:
调用大模型API时使用的密钥。如果配置了此字段，系统会自动在HTTP请求头中添加：
```
Authorization: Bearer <api_key>
```

**使用场景**:
- 不同节点调用不同的AI服务（OpenAI、Claude、国产大模型等）
- 不同节点使用不同的API密钥（成本控制、权限隔离）
- 某些节点需要认证，某些不需要

**API示例**:

创建节点:
```bash
POST /v1/admin/sop/nodes
{
  "template_id": 1,
  "name": "拆解产品",
  "base_url": "https://api.openai.com/v1/chat/completions",
  "model_name": "gpt-4",
  "api_key": "sk-xxxxxxxxxxxxxxxx",
  "timeout_seconds": 60,
  "sort": 1,
  "prompt": "请分析以下产品信息..."
}
```

更新节点:
```bash
PUT /v1/admin/sop/nodes/1
{
  "api_key": "sk-new-key-xxxxxxxx"
}
```

## 执行流程更新

### 对话历史构建流程

```
1. 初始化对话历史 conversationHistory = []

2. 如果模板有 prompt：
   conversationHistory.append({
     role: "system",
     content: template.prompt
   })

3. 循环执行每个节点：
   a. 构建当前节点的用户消息：
      if node.prompt:
        userMessage = node.prompt + "\n\n" + currentInput
      else:
        userMessage = currentInput
   
   b. 添加到对话历史：
      conversationHistory.append({
        role: "user",
        content: userMessage
      })
   
   c. 调用大模型API：
      - URL: node.base_url
      - Model: node.model_name
      - Messages: conversationHistory
      - Headers: 
        * Content-Type: application/json
        * Authorization: Bearer <node.api_key> (如果有)
      - Timeout: node.timeout_seconds
   
   d. 获取响应后添加到对话历史：
      conversationHistory.append({
        role: "assistant",
        content: response.output
      })
   
   e. 下一个节点的输入 = 当前节点的输出
      currentInput = response.output

4. 所有节点完成后，创建最终笔记
```

## 后台管理API更新

### 模板管理

**创建模板** - 新增 `prompt` 参数:
```http
POST /v1/admin/sop/templates
{
  "name": "模板名称",
  "description": "模板描述",
  "prompt": "预处理提示词"  // 新增字段
}
```

**更新模板** - 支持更新 `prompt`:
```http
PUT /v1/admin/sop/templates/:id
{
  "prompt": "新的预处理提示词"  // 新增字段
}
```

**查询模板** - 返回包含 `prompt`:
```http
GET /v1/admin/sop/templates/:id

Response:
{
  "id": 1,
  "name": "模板名称",
  "description": "描述",
  "status": "active",
  "prompt": "预处理提示词",  // 新增字段
  "created_at": "2024-12-14T10:00:00Z"
}
```

### 节点管理

**创建节点** - 新增 `api_key` 参数:
```http
POST /v1/admin/sop/nodes
{
  "template_id": 1,
  "name": "节点名称",
  "base_url": "https://api.example.com",
  "model_name": "gpt-4",
  "api_key": "sk-xxxxxxxx",  // 新增字段
  "timeout_seconds": 60,
  "sort": 1,
  "prompt": "节点提示词"
}
```

**更新节点** - 支持更新 `api_key`:
```http
PUT /v1/admin/sop/nodes/:id
{
  "api_key": "sk-new-key"  // 新增字段
}
```

**查询节点** - 返回包含 `api_key`:
```http
GET /v1/admin/sop/nodes/:id

Response:
{
  "id": 1,
  "template_id": 1,
  "name": "节点名称",
  "base_url": "https://api.example.com",
  "model_name": "gpt-4",
  "api_key": "sk-xxxxxxxx",  // 新增字段
  "timeout_seconds": 60,
  "sort": 1,
  "prompt": "节点提示词"
}
```

## 数据库迁移

系统会自动创建这两个新字段（使用Gorm AutoMigrate）：

```sql
ALTER TABLE sop_template ADD COLUMN prompt TEXT;
ALTER TABLE sop_node ADD COLUMN api_key VARCHAR(255);
```

**注意**: 
- 这两个字段都是可选的（允许为空）
- 旧的模板和节点不受影响，可以继续正常运行
- 新创建的模板和节点可以选择是否配置这些字段

## 测试建议

### 测试场景1：模板预处理提示词

```bash
# 1. 创建带预处理提示词的模板
curl -X POST http://localhost:9099/v1/admin/sop/templates \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{
    "name": "测试模板",
    "prompt": "你是一个测试助手，接下来会有多个测试任务。"
  }'

# 2. 创建节点（不需要修改）
curl -X POST http://localhost:9099/v1/admin/sop/nodes \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{
    "template_id": 1,
    "name": "测试节点",
    "base_url": "https://api.openai.com/v1/chat/completions",
    "model_name": "gpt-4",
    "api_key": "sk-xxxxx",
    "sort": 1
  }'

# 3. 执行SOP，查看是否带上了预处理提示词
# 检查 sop_node_run 表的 input 字段或日志
```

### 测试场景2：节点API密钥

```bash
# 1. 创建带API密钥的节点
curl -X POST http://localhost:9099/v1/admin/sop/nodes \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{
    "template_id": 1,
    "name": "OpenAI节点",
    "base_url": "https://api.openai.com/v1/chat/completions",
    "model_name": "gpt-4",
    "api_key": "sk-your-openai-key",
    "sort": 1
  }'

# 2. 执行SOP
# 检查是否成功调用OpenAI API
# 查看日志确认请求头包含 Authorization: Bearer sk-your-openai-key
```

## 兼容性说明

- ✅ **向后兼容**: 旧的模板和节点不受影响
- ✅ **可选字段**: 两个新字段都是可选的
- ✅ **零影响**: 不配置这些字段时，行为与之前完全一致
- ✅ **灵活配置**: 可以只配置其中一个字段

## 相关文件

**模型定义**:
- `internal/pkg/model/sop.go`

**API请求结构**:
- `pkg/api/numind/v1/sop.go`

**业务逻辑**:
- `internal/numind/biz/sop/sop.go`
- `internal/numind/biz/sop/executor.go`

**控制器**:
- `internal/numind/controller/v1/admin_sop/sop.go`
