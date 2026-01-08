# SOP 最后一个节点直接使用 Input 功能实现总结

## 实现概述

实现了对于 SOP 模板的最后一个节点，直接使用前端传来的 `text` 作为大模型输入的功能，避免重复拼接 `prompt`。

## 修改的文件

### 1. `internal/numind/biz/sop/executor.go`

**修改方法**：`ExecuteNodeStreamWithThinking`

**变更内容**：
1. **方法签名**：新增 `isLastNode bool` 参数
   ```go
   func (e *SopExecutor) ExecuteNodeStreamWithThinking(
       ctx context.Context, 
       node *model.SopNode, 
       input string, 
       history []LLMMessage, 
       handler StreamHandler, 
       isLastNode bool  // 新增参数
   ) (string, string, error)
   ```

2. **逻辑修改**：
   - **最后一个节点**（`isLastNode == true`）：
     - 直接使用 `input`，不拼接 `node.Prompt`
     - 日志：`"Last node: using input directly without prompt"`
   
   - **非最后一个节点**（`isLastNode == false`）：
     - 保持原有逻辑：`node.Prompt + "\n\n" + input`
     - 日志：`"Non-last node: using prompt + input"`

3. **日志增强**：
   - 在日志中添加 `is_last_node` 字段
   - 区分最后节点和非最后节点的处理方式

### 2. `internal/numind/biz/sop/sop.go`

**修改方法**：`ExecuteNodeStream`

**变更内容**：
- 调用 `ExecuteNodeStreamWithThinking` 时传入 `isLastNode` 参数
- `isLastNode` 已在方法中计算：`isLastNode := node.Sort == maxSort`

## 核心逻辑

### 判断是否为最后一个节点

```go
// 找到最大的sort值（最后一个节点）
maxSort := -1
for _, n := range allNodes {
    if n.Sort > maxSort {
        maxSort = n.Sort
    }
}

// 判断当前节点是否是最后一个节点
isLastNode := node.Sort == maxSort
```

### 消息构建逻辑

```go
var userMessage string
if isLastNode {
    // 最后一个节点：直接使用前端传来的 text，不拼接 prompt
    userMessage = input
    log.C(ctx).Infow("Last node: using input directly without prompt", 
        "node_id", node.ID, "input_length", len(input))
} else {
    // 非最后一个节点：使用 prompt + input 拼接
    if node.Prompt != "" {
        userMessage = fmt.Sprintf("%s\n\n%s", node.Prompt, input)
    } else {
        userMessage = input
    }
    log.C(ctx).Debugw("Non-last node: using prompt + input", 
        "node_id", node.ID, "has_prompt", node.Prompt != "")
}
```

## 使用场景

### 场景 1：非最后一个节点

**前端请求**：
```json
{
  "text": "这是用户输入的内容"
}
```

**节点配置**：
- `prompt`: "请分析以下内容："

**发送给大模型的消息**：
```
请分析以下内容：

这是用户输入的内容
```

### 场景 2：最后一个节点

**前端请求**：
```json
{
  "text": "请生成最终报告：\n\n这是前端已经拼接好的完整输入内容"
}
```

**节点配置**：
- `prompt`: "请生成最终报告："

**发送给大模型的消息**：
```
请生成最终报告：

这是前端已经拼接好的完整输入内容
```

**注意**：不会再次拼接 `prompt`，直接使用前端传来的完整内容。

## API 接口

**接口路径**：`POST /v1/sop/runs/{run_id}/nodes/{node_id}/execute`

**行为**：
- 对于最后一个节点：直接使用请求体中的 `text` 字段
- 对于非最后一个节点：使用 `节点prompt + "\n\n" + text` 拼接

## 日志输出

### 最后一个节点

```
INFO Executing node with LLM API (stream with thinking) node_id=3 is_last_node=true
INFO Last node: using input directly without prompt node_id=3 input_length=150
```

### 非最后一个节点

```
INFO Executing node with LLM API (stream with thinking) node_id=1 is_last_node=false
DEBUG Non-last node: using prompt + input node_id=1 has_prompt=true
```

## 向后兼容性

✅ **完全兼容**：
- 非最后一个节点的行为保持不变
- 接口签名不变
- 响应格式不变
- 只是针对最后一个节点优化了输入处理逻辑

## 注意事项

1. **前端职责**：
   - 最后一个节点的完整输入（包括 prompt）应该由前端拼接好
   - 前端需要在 `text` 字段中包含完整的、已经拼接好的内容

2. **数据记录**：
   - 数据库 `sop_node_run` 表的 `input` 字段会记录实际发送给大模型的内容
   - 对于最后一个节点，`input` 就是前端传来的完整内容
   - 对于非最后一个节点，`input` 是原始的用户输入（prompt 是在消息构建时添加的，不会保存到 `input` 字段）

3. **边界情况**：
   - 如果最后一个节点的 `prompt` 为空，直接使用 `input`（不会有问题）
   - 如果模板只有一个节点，该节点会被识别为最后一个节点

4. **测试要点**：
   - 验证最后一个节点不重复拼接 prompt
   - 验证非最后一个节点正常拼接 prompt
   - 验证日志正确显示节点类型

