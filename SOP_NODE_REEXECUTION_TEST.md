# SOP 节点重复执行功能测试指南

## 功能说明

现在支持对已执行的节点进行重新执行。当你调用 `/v1/sop/runs/{run_id}/nodes/{node_id}/execute` 接口时：

- **首次执行**：如果节点从未执行过，会创建新的 NodeRun 记录
- **重复执行**：如果节点已经执行过，会更新现有的 NodeRun 记录（清空之前的输出，重新执行）

## 测试前准备

### 1. 确保服务正常运行

```bash
# 启动服务
cd /Users/neozhang/go/src/github.com/Into-the-Numind/numind-server
./numind
```

### 2. 准备测试数据和 Token

- 获取用户登录 token
- 准备一个 SOP Template 和对应的 Nodes
- 创建一个 SOP Run

## 完整测试流程

### 步骤 1：创建 SOP Run

首先需要创建一个 SOP Run（如果还没有的话）：

```bash
# 替换 <template_id> 和 <token> 为实际值
curl -X POST http://localhost:9091/v1/sop/runs \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{
    "template_id": <template_id>,
    "text": "这是测试文本"
  }'
```

响应示例：
```json
{
  "code": 0,
  "data": {
    "id": 1,
    "template_id": 1,
    "user_id": 1,
    "status": "pending",
    "conversation_id": "sop_1_1_1234567890",
    "created_at": "2024-01-01T00:00:00Z"
  }
}
```

### 步骤 2：获取 Run 的状态和节点信息

```bash
# 替换 <run_id> 和 <token> 为实际值
curl -X GET http://localhost:9091/v1/sop/runs/<run_id>/status \
  -H "Authorization: Bearer <token>"
```

查看有哪些节点可以执行：
```json
{
  "code": 0,
  "data": {
    "status": "pending",
    "current_node_sort": -1,
    "completed_nodes": [],
    "next_node": {
      "node_id": 1,
      "node_name": "节点1",
      "sort": 0,
      "is_first": true,
      "has_next": true
    },
    "total_nodes": 3,
    "completed_count": 0
  }
}
```

### 步骤 3：首次执行节点

执行第一个节点：

```bash
# 替换 <run_id>、<node_id> 和 <token> 为实际值
curl -X POST http://localhost:9091/v1/sop/runs/<run_id>/nodes/<node_id>/execute \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{
    "text": "这是输入文本"
  }'
```

这是一个流式接口（SSE），会实时返回执行结果：
```
data: "这是"
data: "第一"
data: "部分"
data: "输出"
event: done
data: {"status":"completed","uploaded_file_ids":[]}
```

### 步骤 4：查看执行结果

```bash
# 查看 Run 详情
curl -X GET http://localhost:9091/v1/sop/runs/<run_id> \
  -H "Authorization: Bearer <token>"
```

```bash
# 查看 Run 的详细节点执行记录
curl -X GET http://localhost:9091/v1/sop/runs/<run_id>/detail \
  -H "Authorization: Bearer <token>"
```

响应中应该能看到节点的输出（output）字段。

### 步骤 5：重复执行同一个节点（核心测试）

这是我们要测试的核心功能 - 重新执行已经执行过的节点：

```bash
# 使用相同的 run_id 和 node_id，重新调用执行接口
curl -X POST http://localhost:9091/v1/sop/runs/<run_id>/nodes/<node_id>/execute \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{
    "text": "这是新的输入文本（或者保持原输入）"
  }'
```

**预期行为**：
1. 接口应该正常响应，不会报错说"节点已执行"
2. 流式输出新的结果
3. 数据库中该节点的 NodeRun 记录会被**更新**（不是创建新记录）
4. 之前的输出会被清空，替换为新的输出
5. 如果提供了新的 text，会使用新输入；如果没有提供，会使用原来的输入逻辑

### 步骤 6：验证更新结果

再次查看 Run 详情，确认：
1. 节点的 output 字段已经更新为新值
2. 节点的 updated_at 时间已更新
3. 节点的 started_at 和 finished_at 时间已更新
4. 没有创建重复的 NodeRun 记录（同一个 run_id + node_id 只有一条记录）

```bash
curl -X GET http://localhost:9091/v1/sop/runs/<run_id>/detail \
  -H "Authorization: Bearer <token>"
```

### 步骤 7：测试对话历史是否正确

如果 SOP 有多个节点，测试重新执行中间节点时，对话历史是否正确：

1. 执行节点1 → 成功
2. 执行节点2 → 成功
3. **重新执行节点1** → 应该只包含节点1之前的对话历史（如果有），不应该包含节点2的输出
4. 执行节点3 → 应该包含重新执行后的节点1的输出，而不是旧的节点1输出

### 步骤 8：测试边界情况

#### 8.1 测试重新执行失败的节点

```bash
# 如果某个节点执行失败，可以重新执行它
curl -X POST http://localhost:9091/v1/sop/runs/<run_id>/nodes/<failed_node_id>/execute \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{
    "text": "修正后的输入"
  }'
```

#### 8.2 测试使用文件上传重新执行

```bash
curl -X POST http://localhost:9091/v1/sop/runs/<run_id>/nodes/<node_id>/execute \
  -H "Authorization: Bearer <token>" \
  -F "files=@test.txt" \
  -F "text=附加文本"
```

## 数据库验证

如果需要直接查看数据库验证：

```sql
-- 查看指定 Run 的所有 NodeRun 记录
SELECT id, run_id, node_id, status, output, created_at, updated_at 
FROM sop_node_run 
WHERE run_id = <run_id>
ORDER BY node_id, created_at DESC;

-- 应该看到：同一个 run_id + node_id 组合，应该只有一条最新的记录
-- 如果重复执行了，updated_at 应该比 created_at 更新
```

## 日志验证

查看服务日志，应该能看到类似这样的日志：

```
INFO Node run already exists, will update it for re-execution run_id=1 node_id=2 existing_node_run_id=5
INFO Node run prepared for execution run_id=1 node_id=2 node_run_id=5 is_update=true
```

## 预期结果总结

✅ **应该发生的**：
1. 接口允许重复执行同一个节点
2. 更新现有记录，而不是创建新记录
3. 清空之前的输出和错误信息
4. 使用新的输入重新执行
5. 对话历史正确（排除要重新执行的节点）
6. 日志清晰显示是更新还是创建

❌ **不应该发生的**：
1. 创建重复的 NodeRun 记录
2. 报错说节点已执行过
3. 保留旧的输出内容
4. 对话历史混乱（包含要重新执行的节点的旧输出）

## 测试检查清单

- [ ] 首次执行节点成功
- [ ] 重复执行节点成功（不会报错）
- [ ] NodeRun 记录被更新（不是创建新记录）
- [ ] 输出内容被正确更新
- [ ] 时间戳（updated_at, started_at, finished_at）正确更新
- [ ] 对话历史正确（排除要重新执行的节点）
- [ ] 多个节点执行流程中，重新执行中间节点不影响后续节点
- [ ] 重新执行失败的节点可以成功
- [ ] 日志信息清晰

## 故障排查

如果测试失败，检查：

1. **编译错误**：确保代码已正确编译
   ```bash
   go build ./cmd/numind
   ```

2. **数据库连接**：确保数据库连接正常

3. **日志**：查看服务日志，确认错误信息

4. **接口路径**：确认使用的是正确的接口路径和参数

5. **权限**：确认 token 有效且有权限访问该 Run

