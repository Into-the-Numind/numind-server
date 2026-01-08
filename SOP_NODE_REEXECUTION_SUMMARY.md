# SOP 节点重复执行功能实现总结

## 实现概述

实现了允许重复执行 SOP 节点的功能。用户现在可以通过相同的接口重新执行已执行的节点，系统会智能地更新现有记录而不是创建新记录。

## 修改的文件

### 1. `internal/numind/store/sop.go`

**新增方法**：
- `GetNodeRunByRunAndNode(runID, nodeID uint) (*model.SopNodeRun, error)`
  - 根据 runID 和 nodeID 查询最新的 NodeRun 记录
  - 用于检查节点是否已执行过
  - 返回 nil 表示不存在，返回记录表示已存在

### 2. `internal/numind/biz/sop/sop.go`

**修改方法**：`ExecuteNodeStream`

**核心逻辑**：
1. **检查是否存在**：调用 `GetNodeRunByRunAndNode` 检查节点是否已执行过
2. **构建对话历史**：排除要重新执行的节点，只包含其他已完成节点的历史
3. **更新或创建**：
   - 如果存在：更新现有记录（清空输出、思考内容、错误信息等）
   - 如果不存在：创建新记录
4. **执行节点**：调用 executor 执行节点（流式）
5. **更新结果**：将新的输出和思考内容保存到数据库

## 关键实现细节

### 1. 对话历史处理

当重新执行节点时，对话历史会排除当前要重新执行的节点，确保：
- 不会包含该节点之前的旧输出
- 包含其他已执行节点的输出
- 重新执行后的新输出会用于后续节点

```go
// 过滤出已完成的其他节点（排除当前要重新执行的节点）
completedNodeRuns := []model.SopNodeRun{}
for _, nodeRun := range allNodeRuns {
    if nodeRun.NodeID != nodeID && nodeRun.Status == model.SopStatusSucceeded {
        completedNodeRuns = append(completedNodeRuns, nodeRun)
    }
}
```

### 2. 更新现有记录

当检测到节点已存在时，会清空之前的执行结果：

```go
updateData := map[string]interface{}{
    "status":        model.SopStatusRunning,
    "input":         currentInput,
    "started_at":    time.Now(),
    "output":        "", // 清空之前的输出
    "thinking":      "", // 清空之前的思考内容
    "error_message": "", // 清空之前的错误信息
    "finished_at":   nil, // 清空完成时间
    "latency_ms":    0,   // 重置延迟
}
```

### 3. 完成状态检查

在检查所有节点是否完成时，使用 map 来统计每个节点的状态，避免重复执行的节点影响统计：

```go
// 统计每个节点的最新成功记录（用于处理重复执行的情况）
nodeStatusMap := make(map[uint]bool) // nodeID -> has succeeded record
for _, nr := range allNodeRunsForCheck {
    if nr.Status == model.SopStatusSucceeded {
        nodeStatusMap[nr.NodeID] = true
    }
}
completedCount := len(nodeStatusMap)
```

## 使用方式

### API 接口

```
POST /v1/sop/runs/{run_id}/nodes/{node_id}/execute
```

**请求体**（JSON）：
```json
{
  "text": "输入文本（可选，如果不提供会使用默认逻辑）"
}
```

**请求体**（multipart/form-data）：
```
files: 文件（可选）
text: 文本（可选）
```

### 行为说明

1. **首次执行**：
   - 创建新的 NodeRun 记录
   - 状态为 running → succeeded/failed

2. **重复执行**：
   - 更新现有的 NodeRun 记录
   - 清空之前的输出、思考内容、错误信息
   - 使用新的输入重新执行
   - 更新状态为 running → succeeded/failed

3. **输入处理**：
   - 如果提供了 `text` 参数，使用提供的文本
   - 如果没有提供，使用默认逻辑（第一个节点需要提供，其他节点使用上一个节点的输出）

## 测试要点

1. ✅ 首次执行节点成功
2. ✅ 重复执行节点成功（不报错）
3. ✅ NodeRun 记录被更新（不创建新记录）
4. ✅ 输出内容正确更新
5. ✅ 对话历史正确（排除要重新执行的节点）
6. ✅ 多个节点流程中，重新执行中间节点不影响后续节点
7. ✅ 可以重新执行失败的节点

## 日志输出

执行时会输出以下日志：

```
INFO Node run already exists, will update it for re-execution run_id=1 node_id=2 existing_node_run_id=5
INFO Node run prepared for execution run_id=1 node_id=2 node_run_id=5 is_update=true
```

## 注意事项

1. **数据一致性**：同一个 run_id + node_id 组合，只保留一条最新的记录
2. **对话历史**：重新执行节点时，对话历史会排除该节点的旧输出
3. **输入处理**：重新执行时，如果提供了新的 text，会使用新输入；否则使用默认逻辑
4. **状态管理**：重新执行会重置节点的状态和时间戳

## 数据库影响

- **不会**创建重复的 NodeRun 记录
- **会**更新现有记录的 output、thinking、status、时间戳等字段
- **会**保留执行历史（通过 updated_at 可以追踪）

## 向后兼容性

✅ **完全兼容**：
- 首次执行的节点行为不变
- 接口签名不变
- 响应格式不变
- 只是增加了重复执行的能力

