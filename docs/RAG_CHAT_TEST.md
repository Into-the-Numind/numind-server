# RAG聊天功能测试文档

## 功能概述

本系统实现了基于笔记的RAG（检索增强生成）聊天功能，支持：
1. 基于笔记内容的智能检索
2. 流式AI回答生成
3. 笔记关联的聊天记录存储和查询

## 数据库迁移

在测试前，需要执行数据库迁移，添加`book_id`字段到`chat_session`表：

```sql
-- 添加book_id字段到chat_session表
ALTER TABLE chat_session ADD COLUMN book_id INT UNSIGNED NULL;
ALTER TABLE chat_session ADD INDEX idx_book_id (book_id);
ALTER TABLE chat_session ADD FOREIGN KEY (book_id) REFERENCES book(id) ON DELETE SET NULL;
```

## API测试

### 1. 获取笔记聊天记录

**接口**: `GET /v1/chat/book/:book_id/history`

**请求示例**:
```bash
curl -X GET "http://localhost:8080/v1/chat/book/123/history?limit=50" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json"
```

**响应示例**:
```json
{
  "code": 0,
  "message": "OK",
  "data": {
    "session": {
      "id": 1,
      "user_id": 1,
      "book_id": 123,
      "title": "我的笔记 - AI对话",
      "status": "active",
      "message_count": 10,
      "created_at": "2024-01-01T10:00:00Z",
      "updated_at": "2024-01-01T12:00:00Z"
    },
    "messages": [
      {
        "id": 1,
        "session_id": 1,
        "user_id": 1,
        "role": "user",
        "content": "这个笔记讲了什么？",
        "status": "sent",
        "created_at": "2024-01-01T10:00:00Z"
      },
      {
        "id": 2,
        "session_id": 1,
        "user_id": 1,
        "role": "assistant",
        "content": "根据您的笔记内容...",
        "status": "sent",
        "created_at": "2024-01-01T10:00:01Z"
      }
    ],
    "total": 10
  }
}
```

### 2. 列出笔记的所有会话

**接口**: `GET /v1/chat/book/:book_id/sessions`

**请求示例**:
```bash
curl -X GET "http://localhost:8080/v1/chat/book/123/sessions?offset=0&limit=10" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json"
```

### 3. WebSocket流式聊天

**连接**: `ws://localhost:8080/v1/chat/ws?token=YOUR_TOKEN`

**发送消息**:
```json
{
  "type": "message",
  "content": "这个笔记讲了什么？",
  "book_id": 123
}
```

**接收响应**:

1. **流式chunk** (实时接收):
```json
{
  "type": "message_chunk",
  "session_id": 1,
  "content": "根据",
  "role": "assistant",
  "timestamp": "2024-01-01T10:00:01Z"
}
```

2. **完成消息** (最后接收):
```json
{
  "type": "message_done",
  "session_id": 1,
  "message_id": 2,
  "content": "根据您的笔记内容，这个笔记主要讲述了...",
  "role": "assistant",
  "timestamp": "2024-01-01T10:00:05Z"
}
```

## 测试步骤

### 1. 准备测试数据

确保数据库中至少有一个状态为`success`的笔记（book）。

### 2. 获取认证Token

通过登录接口获取JWT token。

### 3. 运行测试脚本

```bash
./scripts/test_rag_chat.sh http://localhost:8080/v1 YOUR_TOKEN 123
```

### 4. WebSocket测试

使用wscat工具测试WebSocket连接：

```bash
# 安装wscat
npm install -g wscat

# 连接WebSocket
wscat -c "ws://localhost:8080/v1/chat/ws?token=YOUR_TOKEN"

# 发送消息
{"type":"message","content":"你好，这个笔记讲了什么？","book_id":123}
```

## 功能验证点

1. ✅ 笔记聊天记录查询：能够正确获取笔记相关的聊天记录
2. ✅ 会话自动创建：当用户对笔记发起聊天时，自动创建或获取会话
3. ✅ RAG检索：能够基于用户问题检索相关笔记内容
4. ✅ 流式返回：AI回答能够实时流式返回给客户端
5. ✅ 消息保存：用户消息和AI回答都能正确保存到数据库

## 注意事项

1. 确保AI服务（火山方舟或阿里百炼）配置正确
2. 确保数据库连接正常
3. WebSocket连接需要保持活跃状态
4. 流式响应需要客户端正确处理`message_chunk`和`message_done`消息类型

