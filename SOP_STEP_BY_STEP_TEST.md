# SOP逐步执行功能测试步骤

## 功能概述

实现了SOP模板的逐步执行功能，支持：
1. 创建Run（不立即执行）
2. 获取下一个待执行节点
3. 流式执行指定节点（支持SSE）
4. 查询Run执行状态

## API接口

### 1. 创建Run（不立即执行）

**接口**: `POST /v1/sop/runs`

**请求体**:
```json
{
  "template_id": 1,
  "text": "这是产品介绍内容..."
}
```

**响应**:
```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 123,
    "template_id": 1,
    "user_id": 1,
    "status": "pending",
    "conversation_id": "sop_1_1_1234567890",
    "created_at": "2025-01-01T00:00:00Z"
  }
}
```

### 2. 获取下一个待执行节点

**接口**: `GET /v1/sop/runs/:run_id/next-node`

**响应**:
```json
{
  "code": 0,
  "message": "",
  "data": {
    "node_id": 1,
    "node_name": "AI拆解产品",
    "sort": 0,
    "is_first": true,
    "has_next": true
  }
}
```

如果所有节点都执行完成：
```json
{
  "code": 0,
  "message": "",
  "data": {
    "node": null,
    "has_next": false,
    "message": "所有节点已执行完成"
  }
}
```

### 3. 流式执行指定节点

**接口**: `POST /v1/sop/runs/:run_id/nodes/:node_id/execute`

**请求体**（可选）:
```json
{
  "text": "新的输入内容"  // 可选，如果不提供则使用上一个节点的输出
}
```

**响应**: Server-Sent Events (SSE) 流式输出

**SSE事件格式**:
```
data: 这是AI返回的内容块1

data: 这是AI返回的内容块2

data: 这是AI返回的内容块3

event: done
data: {"status":"completed"}

```

**错误事件**:
```
event: error
data: 错误信息
```

### 4. 获取Run执行状态

**接口**: `GET /v1/sop/runs/:run_id/status`

**响应**:
```json
{
  "code": 0,
  "message": "",
  "data": {
    "status": "running",
    "current_node_sort": 1,
    "completed_nodes": [
      {
        "node_id": 1,
        "node_name": "AI拆解产品",
        "sort": 0,
        "output_preview": "已深入理解产品，摘要：[STAGE_1_CACHE] = {..."
      }
    ],
    "next_node": {
      "node_id": 2,
      "node_name": "AI拆解爆款文案",
      "sort": 1,
      "is_first": false,
      "has_next": true
    },
    "total_nodes": 4,
    "completed_count": 1
  }
}
```

## 测试步骤

### 前置条件

1. 确保已登录，获取有效的JWT token
2. 确保SOP模板ID为1的模板存在，且有4个节点（sort: 0, 1, 2, 3）

### 步骤1: 创建Run

```bash
curl -X POST http://localhost:9091/v1/sop/runs \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "template_id": 1,
    "text": "这是一款智能手表，具有健康监测、运动追踪、消息提醒等功能。"
  }'
```

**预期结果**: 返回run_id，status为"pending"

**记录**: `run_id = <返回的ID>`

### 步骤2: 获取第一个节点

```bash
curl -X GET http://localhost:9091/v1/sop/runs/<run_id>/next-node \
  -H "Authorization: Bearer YOUR_TOKEN"
```

**预期结果**: 返回第一个节点信息（node_id=1, sort=0, is_first=true）

**记录**: `node_id = <返回的节点ID>`

### 步骤3: 执行第一个节点（流式）

```bash
curl -X POST http://localhost:9091/v1/sop/runs/<run_id>/nodes/<node_id>/execute \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{}' \
  --no-buffer
```

**预期结果**: 
- 实时接收SSE流式输出
- 最后收到 `event: done` 事件
- 输出应包含 `[STAGE_1_CACHE]` 相关内容

### 步骤4: 查询执行状态

```bash
curl -X GET http://localhost:9091/v1/sop/runs/<run_id>/status \
  -H "Authorization: Bearer YOUR_TOKEN"
```

**预期结果**:
- status为"running"
- completed_count为1
- next_node指向第二个节点（node_id=2, sort=1）

### 步骤5: 获取第二个节点

```bash
curl -X GET http://localhost:9091/v1/sop/runs/<run_id>/next-node \
  -H "Authorization: Bearer YOUR_TOKEN"
```

**预期结果**: 返回第二个节点信息（node_id=2, sort=1, is_first=false）

### 步骤6: 执行第二个节点（流式，使用新输入）

```bash
curl -X POST http://localhost:9091/v1/sop/runs/<run_id>/nodes/2/execute \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "text": "这是一篇小红书爆款文案：..."
  }' \
  --no-buffer
```

**预期结果**: 
- 实时接收SSE流式输出
- 输出应包含 `[STAGE_2_CACHE]` 相关内容

### 步骤7: 执行第三个节点（不提供text，使用上一个节点的输出）

```bash
curl -X POST http://localhost:9091/v1/sop/runs/<run_id>/nodes/3/execute \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{}' \
  --no-buffer
```

**预期结果**: 
- 使用第二个节点的输出作为输入
- 输出应包含 `[STAGE_3_CACHE]` 相关内容

### 步骤8: 执行第四个节点（最后一个节点）

```bash
curl -X POST http://localhost:9091/v1/sop/runs/<run_id>/nodes/4/execute \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "text": "本次视频的新主题：如何选择智能手表"
  }' \
  --no-buffer
```

**预期结果**: 
- 实时接收SSE流式输出
- 输出应包含完整口播文案
- 最后收到 `event: done` 事件

### 步骤9: 验证最终状态

```bash
curl -X GET http://localhost:9091/v1/sop/runs/<run_id>/status \
  -H "Authorization: Bearer YOUR_TOKEN"
```

**预期结果**:
- status为"succeeded"
- completed_count为4
- next_node为null

### 步骤10: 验证最终Note

```bash
curl -X GET http://localhost:9091/v1/sop/runs/<run_id> \
  -H "Authorization: Bearer YOUR_TOKEN"
```

**预期结果**: 
- final_note_id不为空
- 可以通过 `/v1/sop/notes/<note_id>` 查看最终笔记

## 前端集成示例（JavaScript）

### 使用EventSource接收SSE流

```javascript
// 执行节点并接收流式输出
async function executeNode(runId, nodeId, text = '') {
  const response = await fetch(`/v1/sop/runs/${runId}/nodes/${nodeId}/execute`, {
    method: 'POST',
    headers: {
      'Authorization': `Bearer ${token}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ text }),
  });

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;

    buffer += decoder.decode(value, { stream: true });
    const lines = buffer.split('\n');
    buffer = lines.pop() || '';

    for (const line of lines) {
      if (line.startsWith('data: ')) {
        const data = line.slice(6);
        if (data === '[DONE]') continue;
        
        // 处理数据块
        handleChunk(data);
      } else if (line.startsWith('event: ')) {
        const event = line.slice(7);
        if (event === 'done') {
          handleDone();
        } else if (event === 'error') {
          // 下一个data行会是错误信息
        }
      }
    }
  }
}

function handleChunk(chunk) {
  // 实时显示内容块
  console.log('Received chunk:', chunk);
  // 更新UI显示
}

function handleDone() {
  console.log('Stream completed');
  // 更新UI状态
}
```

### 完整流程示例

```javascript
async function executeSopStepByStep(templateId, initialText) {
  // 1. 创建Run
  const run = await createRun(templateId, initialText);
  const runId = run.id;

  // 2. 循环执行所有节点
  while (true) {
    // 获取下一个节点
    const nextNode = await getNextNode(runId);
    if (!nextNode || !nextNode.node_id) {
      console.log('所有节点执行完成');
      break;
    }

    // 执行节点（流式）
    await executeNode(runId, nextNode.node_id, '');
    
    // 等待用户输入或自动继续
    // 这里可以添加用户交互逻辑
  }

  // 3. 获取最终结果
  const finalStatus = await getRunStatus(runId);
  console.log('执行完成', finalStatus);
}
```

## 注意事项

1. **流式输出**: 使用SSE格式，前端需要使用EventSource或手动解析流
2. **上下文传递**: 每个节点的输出会自动作为下一个节点的输入（如果用户不提供新text）
3. **节点顺序**: 严格按照sort字段顺序执行，不能跳过或乱序执行
4. **错误处理**: 如果某个节点执行失败，Run状态会变为"failed"，需要重新创建Run
5. **并发控制**: 同一个Run不能同时执行多个节点，需要等待当前节点完成后再执行下一个

## 错误处理

### 常见错误

1. **节点已执行**: 如果尝试重复执行已完成的节点，会返回错误
2. **节点顺序错误**: 如果尝试跳过节点执行，会返回错误
3. **Run不存在**: 如果run_id无效，会返回404错误
4. **权限错误**: 如果Run不属于当前用户，会返回403错误

### 错误响应格式

```json
{
  "code": 10001,
  "message": "错误信息",
  "data": null
}
```
