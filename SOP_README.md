# SOP 工作流系统使用文档

## 功能概述

SOP (Standard Operating Procedure) 工作流系统是一个基于AI大模型的自动化流程执行平台。系统支持：

- 创建和管理SOP模板
- 配置多个线性/树状节点流程
- 自动执行节点并保持上下文连贯
- 记录每个节点的输入输出和执行状态
- 生成最终笔记

## 数据模型

### 1. SopTemplate (SOP模板)
- `id`: 主键
- `name`: 模板名称
- `description`: 模板描述
- `status`: 状态 (active/inactive)

### 2. SopNode (SOP节点)
- `id`: 主键
- `template_id`: 所属模板ID
- `parent_id`: 父节点ID (NULL表示根节点)
- `name`: 节点名称
- `status`: 状态 (active/inactive)
- `base_url`: AI服务地址
- `model_name`: 模型名称
- `timeout_seconds`: 超时时间（秒）
- `sort`: 排序（执行顺序）
- `is_root`: 是否为根节点
- `prompt`: 节点提示词模板

### 3. SopRun (SOP执行记录)
- `id`: 主键
- `template_id`: 模板ID
- `user_id`: 用户ID
- `status`: 状态 (pending/running/succeeded/failed)
- `conversation_id`: 对话ID（隔离的会话）
- `final_note_id`: 最终笔记ID
- `started_at`: 开始时间
- `finished_at`: 完成时间
- `error_message`: 错误信息

### 4. SopNodeRun (SOP节点执行记录)
- `id`: 主键
- `run_id`: 执行记录ID
- `node_id`: 节点ID
- `status`: 状态
- `input`: 节点输入
- `output`: 节点输出
- `latency_ms`: 执行耗时（毫秒）
- `conversation_id`: 对话ID
- `sort`: 执行顺序
- `started_at`: 开始时间
- `finished_at`: 完成时间
- `error_message`: 错误信息

### 5. SopNote (SOP笔记)
- `id`: 主键
- `content`: 内容（最终输出）
- `title`: 标题
- `user_id`: 用户ID
- `template_id`: 模板ID
- `run_id`: 执行记录ID

## API 接口

### 模板管理

#### 创建模板
```bash
POST /v1/admin/sop/templates
Authorization: Bearer <token>

{
  "name": "文章摘要生成",
  "description": "自动生成文章摘要和关键点"
}
```

#### 获取模板
```bash
GET /v1/admin/sop/templates/:id
Authorization: Bearer <token>
```

#### 获取模板列表
```bash
GET /v1/admin/sop/templates?offset=0&limit=20
Authorization: Bearer <token>
```

#### 更新模板
```bash
PUT /v1/admin/sop/templates/:id
Authorization: Bearer <token>

{
  "name": "新名称",
  "status": "inactive"
}
```

#### 删除模板
```bash
DELETE /v1/admin/sop/templates/:id
Authorization: Bearer <token>
```

### 节点管理

#### 创建节点
```bash
POST /v1/admin/sop/nodes
Authorization: Bearer <token>

{
  "template_id": 1,
  "parent_id": null,
  "name": "提取关键信息",
  "base_url": "https://api.openai.com/v1/chat/completions",
  "model_name": "gpt-4",
  "timeout_seconds": 60,
  "sort": 1,
  "prompt": "请从以下文本中提取关键信息："
}
```

#### 获取模板的所有节点
```bash
GET /v1/admin/sop/templates/:id/nodes
Authorization: Bearer <token>
```

#### 更新节点
```bash
PUT /v1/admin/sop/nodes/:id
Authorization: Bearer <token>

{
  "name": "新节点名称",
  "sort": 2
}
```

#### 删除节点
```bash
DELETE /v1/admin/sop/nodes/:id
Authorization: Bearer <token>
```

### 执行管理

#### 执行SOP模板
```bash
POST /v1/admin/sop/templates/:id/run
Authorization: Bearer <token>

{
  "user_id": 1,
  "initial_input": "这是要处理的原始文本内容..."
}

响应：
{
  "code": 0,
  "data": {
    "id": 1,
    "template_id": 1,
    "user_id": 1,
    "status": "pending",
    "conversation_id": "sop_1_1_1702567890",
    "created_at": "2024-12-14T10:00:00Z"
  }
}
```

#### 查询执行状态
```bash
GET /v1/admin/sop/runs/:id
Authorization: Bearer <token>
```

#### 查询执行详情（包含节点执行记录）
```bash
GET /v1/admin/sop/runs/:id/detail
Authorization: Bearer <token>

响应：
{
  "code": 0,
  "data": {
    "run": {
      "id": 1,
      "status": "succeeded",
      "final_note_id": 5,
      ...
    },
    "node_runs": [
      {
        "id": 1,
        "node_id": 1,
        "status": "succeeded",
        "input": "原始输入",
        "output": "节点1输出",
        "latency_ms": 1500,
        ...
      },
      {
        "id": 2,
        "node_id": 2,
        "status": "succeeded",
        "input": "节点1输出",
        "output": "最终输出",
        "latency_ms": 2000,
        ...
      }
    ]
  }
}
```

#### 查询执行列表
```bash
GET /v1/admin/sop/runs?offset=0&limit=20&user_id=1
Authorization: Bearer <token>
```

### 笔记管理

#### 获取笔记
```bash
GET /v1/admin/sop/notes/:id
Authorization: Bearer <token>
```

#### 获取用户的笔记列表
```bash
GET /v1/admin/sop/users/:user_id/notes?offset=0&limit=20
Authorization: Bearer <token>
```

## 执行流程

### 1. 线性执行（MVP）

```
初始输入 → 节点1 → 节点2 → 节点3 → 最终笔记
```

1. 创建SopRun记录，状态为pending
2. 生成唯一的conversation_id用于隔离对话
3. 按sort顺序逐个执行节点：
   - 第一个节点的输入 = 初始输入
   - 后续节点的输入 = 前一个节点的输出
   - 每个节点保存输入输出、状态、耗时
4. 所有节点成功后，用最后节点的输出创建SopNote
5. 更新SopRun状态为succeeded，关联final_note_id

### 2. 上下文连贯性

- 每个SopRun有独立的conversation_id
- 所有节点共享同一个conversation_id
- 调用大模型时，将前序所有节点的对话历史传递给下一个节点
- 保证上下文连贯，避免与用户其他聊天混淆

### 3. 错误处理

- 任何节点失败，整个SopRun标记为failed
- 记录错误信息到error_message
- 已执行的节点记录保留，状态正常

## 使用示例

### 示例1：文章摘要生成器

**场景**：输入一篇文章，自动生成摘要和关键点

**模板配置**：
- 模板名称：文章摘要生成
- 节点1：提取关键信息（sort=1）
- 节点2：生成摘要（sort=2）
- 节点3：整理输出格式（sort=3）

**执行流程**：
```
原文 → [提取关键信息] → [生成摘要] → [整理格式] → 最终笔记
```

### 示例2：代码审查助手

**场景**：输入代码，自动进行多角度审查

**模板配置**：
- 模板名称：代码审查
- 节点1：语法检查（sort=1）
- 节点2：性能分析（sort=2）
- 节点3：安全审查（sort=3）
- 节点4：生成审查报告（sort=4）

## 扩展性设计

当前MVP实现为线性执行，但数据模型已支持DAG（有向无环图）：

- `parent_id`字段支持多个子节点
- 可扩展为并行执行
- 可实现条件分支
- 支持节点依赖关系

## 注意事项

1. **conversation_id隔离**：每次执行都有独立的对话ID，避免串线
2. **超时设置**：合理设置timeout_seconds，避免长时间等待
3. **错误处理**：节点失败会终止整个流程，需要重新执行
4. **异步执行**：ExecuteTemplate立即返回run_id，实际执行是异步的
5. **API地址**：base_url需要是有效的大模型API地址
6. **认证授权**：所有接口都需要Bearer token认证

## 数据库迁移

系统启动时会自动创建以下表：
- `sop_template`
- `sop_node`
- `sop_run`
- `sop_node_run`
- `sop_note`

无需手动执行迁移脚本。
