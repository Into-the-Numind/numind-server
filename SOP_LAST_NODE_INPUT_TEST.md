# SOP 最后一个节点直接使用 Input 功能测试指南

## 功能说明

对于 SOP 模板的最后一个节点，现在支持直接使用前端传来的 `text` 作为大模型的输入，而不需要再拼接节点表中的 `prompt`。这是因为前端已经帮后端做好了拼接处理。

**规则**：
- **最后一个节点**：直接使用前端传来的 `text`，不拼接 `sop_node` 表中的 `prompt`
- **非最后一个节点**：保持原有逻辑，使用 `sop_node` 表中的 `prompt + "\n\n" + text` 拼接

## 实现细节

### 代码修改

1. **`internal/numind/biz/sop/executor.go`**
   - `ExecuteNodeStreamWithThinking` 方法新增 `isLastNode bool` 参数
   - 当 `isLastNode == true` 时，直接使用 `input`，不拼接 `node.Prompt`
   - 当 `isLastNode == false` 时，使用 `node.Prompt + "\n\n" + input` 拼接

2. **`internal/numind/biz/sop/sop.go`**
   - `ExecuteNodeStream` 方法中已经判断了 `isLastNode`
   - 调用 `ExecuteNodeStreamWithThinking` 时传入 `isLastNode` 参数

## 测试前准备

### 1. 确保服务正常运行

```bash
# 启动服务
cd /Users/neozhang/go/src/github.com/Into-the-Numind/numind-server
./numind
```

### 2. 准备测试数据

- 获取用户登录 token
- 准备一个 SOP Template，至少包含 2 个节点
- 在数据库中查看节点的配置：
  ```sql
  -- 查看模板的所有节点
  SELECT id, template_id, name, sort, prompt 
  FROM sop_node 
  WHERE template_id = <template_id>
  ORDER BY sort ASC;
  ```

**重要**：
- 确保最后一个节点（sort 值最大的节点）有 `prompt` 字段
- 确保非最后一个节点也有 `prompt` 字段，以便对比测试

## 完整测试流程

### 步骤 1：创建 SOP Run

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

记录返回的 `run_id`。

### 步骤 2：执行非最后一个节点（验证原有逻辑）

#### 2.1 获取节点信息

```bash
# 查看 Run 状态，了解有哪些节点
curl -X GET http://localhost:9091/v1/sop/runs/<run_id>/status \
  -H "Authorization: Bearer <token>"
```

#### 2.2 执行第一个节点（非最后一个）

假设第一个节点的 `node_id` 是 1，它的 `prompt` 是 "请分析以下内容："。

```bash
curl -X POST http://localhost:9091/v1/sop/runs/<run_id>/nodes/1/execute \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{
    "text": "这是用户输入的内容"
  }'
```

**预期行为**：
- 发送给大模型的消息应该是：`"请分析以下内容：\n\n这是用户输入的内容"`
- 即：`prompt + "\n\n" + text`

#### 2.3 验证日志

查看服务日志，应该看到类似：

```
INFO Non-last node: using prompt + input node_id=1 has_prompt=true
```

### 步骤 3：执行最后一个节点（验证新逻辑）

#### 3.1 确认最后一个节点

根据步骤 2.1 的返回结果，找到 `total_nodes` 和当前已完成节点数，确认最后一个节点的 `node_id`。

假设最后一个节点的 `node_id` 是 3，它的 `prompt` 是 "请生成最终报告："。

#### 3.2 执行最后一个节点

```bash
curl -X POST http://localhost:9091/v1/sop/runs/<run_id>/nodes/3/execute \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{
    "text": "请生成最终报告：\n\n这是前端已经拼接好的完整输入内容"
  }'
```

**预期行为**：
- 发送给大模型的消息应该直接是：`"请生成最终报告：\n\n这是前端已经拼接好的完整输入内容"`
- **不会**再拼接一次 `prompt`
- 即：直接使用前端传来的 `text`，不拼接 `node.Prompt`

#### 3.3 验证日志

查看服务日志，应该看到类似：

```
INFO Last node: using input directly without prompt node_id=3 input_length=XXX
INFO Executing node with LLM API (stream with thinking) node_id=3 is_last_node=true
```

### 步骤 4：对比验证

#### 4.1 查看数据库中的输入记录

```sql
-- 查看节点的执行记录，对比 input 字段
SELECT id, node_id, sort, input, output, status, created_at
FROM sop_node_run
WHERE run_id = <run_id>
ORDER BY sort ASC;
```

**预期结果**：
- 非最后一个节点的 `input`：应该是原始的用户输入（如："这是用户输入的内容"）
- 最后一个节点的 `input`：应该是前端传来的完整内容（如："请生成最终报告：\n\n这是前端已经拼接好的完整输入内容"）

#### 4.2 验证大模型收到的实际消息

由于我们无法直接查看发送给大模型的消息，可以通过以下方式验证：

1. **查看日志**：服务日志中会记录相关信息
2. **对比输出**：如果 prompt 被重复拼接，大模型的输出可能会异常

### 步骤 5：边界情况测试

#### 5.1 最后一个节点没有 prompt

测试场景：最后一个节点的 `prompt` 字段为空或 NULL。

```bash
# 先更新节点，清空 prompt
# UPDATE sop_node SET prompt = '' WHERE id = <last_node_id>;

# 执行最后一个节点
curl -X POST http://localhost:9091/v1/sop/runs/<run_id>/nodes/<last_node_id>/execute \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{
    "text": "这是前端传来的完整输入"
  }'
```

**预期行为**：
- 直接使用前端传来的 `text`
- 不会因为 prompt 为空而出现问题

#### 5.2 只有一个节点的模板

测试场景：SOP Template 只有一个节点。

```bash
# 创建只有一个节点的模板的 Run
# 执行这个唯一的节点（它既是第一个也是最后一个）
curl -X POST http://localhost:9091/v1/sop/runs/<run_id>/nodes/<node_id>/execute \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <token>" \
  -d '{
    "text": "前端已经拼接好的完整输入"
  }'
```

**预期行为**：
- `isLastNode == true`
- 直接使用前端传来的 `text`，不拼接 prompt

#### 5.3 使用文件上传

测试场景：最后一个节点使用文件上传。

```bash
curl -X POST http://localhost:9091/v1/sop/runs/<run_id>/nodes/<last_node_id>/execute \
  -H "Authorization: Bearer <token>" \
  -F "files=@test.txt" \
  -F "text=这是附加的文本内容，前端已经拼接好了prompt"
```

**预期行为**：
- 提取文件内容后，与 `text` 合并
- 合并后的内容直接作为输入，不拼接 prompt

## 日志验证要点

### 关键日志信息

1. **非最后一个节点**：
   ```
   INFO Non-last node: using prompt + input node_id=X has_prompt=true/false
   INFO Executing node with LLM API (stream with thinking) node_id=X is_last_node=false
   ```

2. **最后一个节点**：
   ```
   INFO Last node: using input directly without prompt node_id=X input_length=XXX
   INFO Executing node with LLM API (stream with thinking) node_id=X is_last_node=true
   ```

### 检查要点

- ✅ `is_last_node` 字段是否正确（true/false）
- ✅ 是否正确输出 "Last node" 或 "Non-last node" 日志
- ✅ `input_length` 是否合理

## 数据库验证

### SQL 查询

```sql
-- 查看节点的 prompt 配置
SELECT id, template_id, name, sort, 
       CASE WHEN prompt IS NULL OR prompt = '' THEN '空' ELSE '有内容' END as prompt_status,
       LEFT(prompt, 50) as prompt_preview
FROM sop_node
WHERE template_id = <template_id>
ORDER BY sort ASC;

-- 查看执行记录的 input 内容
SELECT nr.id, nr.node_id, n.name, n.sort, n.prompt,
       LEFT(nr.input, 100) as input_preview,
       LENGTH(nr.input) as input_length,
       nr.status
FROM sop_node_run nr
JOIN sop_node n ON nr.node_id = n.id
WHERE nr.run_id = <run_id>
ORDER BY n.sort ASC;
```

### 验证逻辑

1. **非最后一个节点**：
   - 检查 `input` 是否不包含 `prompt` 的内容（或只包含部分，取决于前端是否也做了拼接）
   - 正常逻辑下，`input` 应该是原始的用户输入

2. **最后一个节点**：
   - 检查 `input` 是否包含 `prompt` 的内容（因为前端已经拼接好了）
   - `input` 应该是完整的、已经拼接好的内容

## 测试检查清单

### 基本功能

- [ ] 非最后一个节点：使用 prompt + text 拼接
- [ ] 最后一个节点：直接使用 text，不拼接 prompt
- [ ] 日志正确显示 `is_last_node` 状态
- [ ] 日志正确显示 "Last node" 或 "Non-last node" 信息

### 边界情况

- [ ] 最后一个节点没有 prompt 时，能正常处理
- [ ] 只有一个节点的模板，能正确识别为最后一个节点
- [ ] 文件上传场景，最后一个节点也能正确处理

### 数据验证

- [ ] 数据库中的 `input` 字段符合预期
- [ ] 非最后一个节点的 `input` 不包含完整的 prompt
- [ ] 最后一个节点的 `input` 包含前端拼接好的完整内容

### 功能验证

- [ ] 大模型返回的结果正常（没有因为重复拼接 prompt 导致异常）
- [ ] 最后一个节点的输出质量正常
- [ ] 整个 SOP 流程能正常完成

## 故障排查

### 问题 1：最后一个节点仍然拼接了 prompt

**症状**：日志显示 `is_last_node=true`，但实际发送给大模型的消息中 prompt 被重复了。

**排查**：
1. 检查 `isLastNode` 的判断逻辑是否正确
2. 检查是否有其他地方也在拼接 prompt
3. 查看完整的日志，确认消息构建过程

### 问题 2：非最后一个节点没有拼接 prompt

**症状**：非最后一个节点直接使用了 text，没有拼接 prompt。

**排查**：
1. 检查节点的 `prompt` 字段是否有值
2. 检查 `isLastNode` 的判断是否正确
3. 查看日志确认 `is_last_node=false`

### 问题 3：判断最后一个节点的逻辑错误

**症状**：`isLastNode` 的判断结果不正确。

**排查**：
```sql
-- 验证节点的 sort 值
SELECT id, name, sort, 
       (SELECT MAX(sort) FROM sop_node WHERE template_id = n.template_id) as max_sort,
       CASE 
         WHEN sort = (SELECT MAX(sort) FROM sop_node WHERE template_id = n.template_id) 
         THEN '是最后一个' 
         ELSE '不是最后一个' 
       END as is_last
FROM sop_node n
WHERE template_id = <template_id>
ORDER BY sort ASC;
```

## 预期结果总结

### ✅ 应该发生的

1. **非最后一个节点**：
   - 日志显示 `is_last_node=false`
   - 日志显示 "Non-last node: using prompt + input"
   - 发送给大模型的消息 = `prompt + "\n\n" + text`

2. **最后一个节点**：
   - 日志显示 `is_last_node=true`
   - 日志显示 "Last node: using input directly without prompt"
   - 发送给大模型的消息 = `text`（直接使用，不拼接 prompt）

### ❌ 不应该发生的

1. 最后一个节点重复拼接 prompt
2. 非最后一个节点没有拼接 prompt（当 prompt 存在时）
3. `isLastNode` 判断错误
4. 大模型输出异常（由于消息格式错误）

## 注意事项

1. **前端职责**：最后一个节点的完整输入应该由前端拼接好，包括 prompt 和实际内容
2. **向后兼容**：非最后一个节点保持原有逻辑不变
3. **日志记录**：关键步骤都有日志记录，便于排查问题
4. **数据一致性**：数据库中的 `input` 字段记录的是实际发送给大模型的内容（对于最后一个节点，就是前端传来的完整内容）

