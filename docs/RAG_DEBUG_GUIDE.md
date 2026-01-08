# RAG检索调试指南

## 问题排查步骤

### 1. 检查数据库中的笔记数据

使用以下SQL查询检查笔记是否存在且状态正确：

```sql
-- 检查笔记基本信息
SELECT 
    id,
    user_id,
    title,
    status,
    LENGTH(processed_text) AS processed_text_len,
    LENGTH(original_text) AS original_text_len,
    deleted_at
FROM book
WHERE id = 426  -- 替换为实际的book_id
  AND deleted_at IS NULL;
```

**检查点**：
- ✅ `id` 是否存在
- ✅ `user_id` 是否匹配当前用户
- ✅ `status` 是否为 `'success'`
- ✅ `deleted_at` 是否为 `NULL`
- ✅ `processed_text` 或 `original_text` 是否有内容

### 2. 检查日志输出

查看服务器日志，应该能看到以下关键日志：

```
检索指定笔记 book_id=426 user_id=2
成功获取笔记 book_id=426 user_id=2 status=success title=...
提取笔记内容 book_id=426 content_length=xxx has_processed_text=true ...
```

**常见问题**：

#### 问题1: "笔记不存在或已被删除"
- **原因**: 笔记ID不存在，或笔记被软删除（deleted_at不为NULL）
- **解决**: 检查数据库中的笔记是否存在

#### 问题2: "笔记状态不是success"
- **原因**: 笔记状态不是 `'success'`，可能是 `'creating'`, `'ai'`, `'render'`, `'failed'`
- **解决**: 等待笔记处理完成，或检查笔记处理流程

#### 问题3: "笔记内容为空"
- **原因**: `ProcessedText` 和 `OriginalText` 都为空
- **解决**: 检查笔记创建流程，确保内容被正确保存

#### 问题4: "无权访问该笔记"
- **原因**: 笔记的 `user_id` 与当前用户ID不匹配
- **解决**: 检查用户ID是否正确

### 3. 检查SQL查询

`GetByID` 方法执行的SQL查询应该是：

```sql
SELECT * FROM book 
WHERE id = ? 
  AND deleted_at IS NULL
LIMIT 1;
```

如果使用GORM的日志，可以在配置中启用SQL日志：

```yaml
# config_local.yaml
database:
  log_level: info  # 或 debug 查看详细SQL
```

### 4. 测试文本提取

如果笔记存在但内容提取失败，可以手动测试：

```go
// 测试代码
book := &model.BookM{
    ProcessedText: "...", // 从数据库获取的实际值
    OriginalText: "...",
}
content := extractBookText(book)
fmt.Printf("提取的内容长度: %d\n", len(content))
```

### 5. 常见数据结构问题

#### ProcessedText可能是JSON格式

如果 `ProcessedText` 是JSON格式，例如：
```json
{
  "title": "标题",
  "content": "内容",
  "sections": [
    {"text": "段落1"},
    {"text": "段落2"}
  ]
}
```

代码会自动解析JSON并提取所有文本内容。

#### ProcessedText可能是Markdown格式

如果 `ProcessedText` 是Markdown格式，例如：
```markdown
# 标题

这是**加粗**的文本。

- 列表项1
- 列表项2
```

代码会自动移除Markdown标记，提取纯文本。

### 6. 调试建议

1. **启用详细日志**：确保日志级别设置为 `info` 或 `debug`
2. **检查数据库连接**：确保数据库连接正常
3. **验证用户ID**：确保WebSocket消息中的用户ID正确
4. **检查笔记状态**：确保笔记状态为 `'success'`
5. **验证内容字段**：确保 `ProcessedText` 或 `OriginalText` 有内容

### 7. 测试SQL脚本

使用 `scripts/test_book_query.sql` 中的SQL语句直接查询数据库，验证数据是否正确。

### 8. 修复后的改进

1. ✅ 使用显式的 `WHERE id = ?` 条件
2. ✅ 添加详细的日志输出
3. ✅ 优化错误处理，内容为空时返回空结果而不是错误
4. ✅ 改进文本提取逻辑，支持JSON和Markdown格式
5. ✅ 如果ProcessedText提取失败，自动回退到OriginalText

## 快速检查清单

- [ ] 笔记ID在数据库中存在
- [ ] 笔记的 `user_id` 匹配当前用户
- [ ] 笔记的 `status` 为 `'success'`
- [ ] 笔记的 `deleted_at` 为 `NULL`
- [ ] `ProcessedText` 或 `OriginalText` 有内容
- [ ] 服务器日志显示成功获取笔记
- [ ] 服务器日志显示成功提取内容

