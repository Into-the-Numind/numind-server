# 对话功能 API 文档

## 概述

对话功能提供了基于WebSocket的实时聊天功能，支持用户与AI助手进行对话。同时提供了HTTP API用于管理对话会话和消息。

## WebSocket API

### 连接WebSocket

**URL:** `GET /v1/chat/ws`

**Headers:**
```
Authorization: Bearer <token>
```

**说明:**
- 需要JWT认证
- 连接后可以进行实时对话

### WebSocket消息格式

#### 发送消息
```json
{
  "type": "message",
  "session_id": 0,  // 可选，0表示创建新会话
  "content": "你好，我想了解一下人工智能",
  "timestamp": "2024-01-01T12:00:00Z"
}
```

#### 接收消息
```json
{
  "type": "message",
  "session_id": 123,
  "message_id": 456,
  "content": "你好！我很乐意为你介绍人工智能。人工智能是...",
  "role": "assistant",
  "timestamp": "2024-01-01T12:00:01Z"
}
```

#### 心跳消息
```json
{
  "type": "ping",
  "timestamp": "2024-01-01T12:00:00Z"
}
```

#### 心跳响应
```json
{
  "type": "pong",
  "timestamp": "2024-01-01T12:00:00Z"
}
```

#### 错误消息
```json
{
  "type": "error",
  "error": "错误信息",
  "timestamp": "2024-01-01T12:00:00Z"
}
```

## HTTP API

### 1. 创建对话会话

**URL:** `POST /v1/chat/sessions`

**Headers:**
```
Authorization: Bearer <token>
Content-Type: application/json
```

**Request Body:**
```json
{
  "title": "新对话"
}
```

**Response:**
```json
{
  "code": "",
  "message": "",
  "data": {
    "id": 123,
    "user_id": 1,
    "title": "新对话",
    "status": "active",
    "message_count": 0,
    "created_at": "2024-01-01T12:00:00Z",
    "updated_at": "2024-01-01T12:00:00Z"
  }
}
```

### 2. 获取会话列表

**URL:** `GET /v1/chat/sessions?offset=0&limit=10`

**Headers:**
```
Authorization: Bearer <token>
```

**Response:**
```json
{
  "code": "",
  "message": "",
  "data": {
    "total": 5,
    "sessions": [
      {
        "id": 123,
        "user_id": 1,
        "title": "新对话",
        "status": "active",
        "message_count": 10,
        "created_at": "2024-01-01T12:00:00Z",
        "updated_at": "2024-01-01T12:00:00Z"
      }
    ]
  }
}
```

### 3. 获取会话详情

**URL:** `GET /v1/chat/sessions/{id}`

**Headers:**
```
Authorization: Bearer <token>
```

**Response:**
```json
{
  "code": "",
  "message": "",
  "data": {
    "id": 123,
    "user_id": 1,
    "title": "新对话",
    "status": "active",
    "message_count": 10,
    "created_at": "2024-01-01T12:00:00Z",
    "updated_at": "2024-01-01T12:00:00Z"
  }
}
```

### 4. 更新会话

**URL:** `PUT /v1/chat/sessions/{id}`

**Headers:**
```
Authorization: Bearer <token>
Content-Type: application/json
```

**Request Body:**
```json
{
  "title": "更新后的标题"
}
```

**Response:**
```json
{
  "code": "",
  "message": "",
  "data": {
    "message": "Session updated successfully"
  }
}
```

### 5. 删除会话

**URL:** `DELETE /v1/chat/sessions/{id}`

**Headers:**
```
Authorization: Bearer <token>
```

**Response:**
```json
{
  "code": "",
  "message": "",
  "data": {
    "message": "Session deleted successfully"
  }
}
```

### 6. 获取会话消息列表

**URL:** `GET /v1/chat/sessions/{session_id}/messages?offset=0&limit=50`

**Headers:**
```
Authorization: Bearer <token>
```

**Response:**
```json
{
  "code": "",
  "message": "",
  "data": {
    "total": 20,
    "messages": [
      {
        "id": 456,
        "session_id": 123,
        "user_id": 1,
        "role": "user",
        "content": "你好",
        "status": "sent",
        "created_at": "2024-01-01T12:00:00Z",
        "updated_at": "2024-01-01T12:00:00Z"
      },
      {
        "id": 457,
        "session_id": 123,
        "user_id": 1,
        "role": "assistant",
        "content": "你好！有什么可以帮助你的吗？",
        "status": "sent",
        "created_at": "2024-01-01T12:00:01Z",
        "updated_at": "2024-01-01T12:00:01Z"
      }
    ]
  }
}
```

### 7. 获取会话及消息

**URL:** `GET /v1/chat/sessions/{id}/with-messages`

**Headers:**
```
Authorization: Bearer <token>
```

**Response:**
```json
{
  "code": "",
  "message": "",
  "data": {
    "id": 123,
    "user_id": 1,
    "title": "新对话",
    "status": "active",
    "message_count": 2,
    "created_at": "2024-01-01T12:00:00Z",
    "updated_at": "2024-01-01T12:00:01Z",
    "messages": [
      {
        "id": 456,
        "session_id": 123,
        "user_id": 1,
        "role": "user",
        "content": "你好",
        "status": "sent",
        "created_at": "2024-01-01T12:00:00Z",
        "updated_at": "2024-01-01T12:00:00Z"
      },
      {
        "id": 457,
        "session_id": 123,
        "user_id": 1,
        "role": "assistant",
        "content": "你好！有什么可以帮助你的吗？",
        "status": "sent",
        "created_at": "2024-01-01T12:00:01Z",
        "updated_at": "2024-01-01T12:00:01Z"
      }
    ]
  }
}
```

## 错误码

| 错误码 | HTTP状态码 | 说明 |
|--------|------------|------|
| InternalError | 500 | 内部服务器错误 |
| InvalidParameter.BindError | 400 | 参数绑定错误 |
| AuthFailure.Unauthorized | 401 | 未授权访问 |

## 小程序端使用示例

### 连接WebSocket

```javascript
// 获取token
const token = wx.getStorageSync('token');

// 连接WebSocket
const ws = wx.connectSocket({
  url: 'wss://your-domain.com/v1/chat/ws',
  header: {
    'Authorization': `Bearer ${token}`
  }
});

// 监听连接打开
ws.onOpen(() => {
  console.log('WebSocket连接已建立');
});

// 监听消息
ws.onMessage((res) => {
  const message = JSON.parse(res.data);
  console.log('收到消息:', message);
  
  if (message.type === 'message') {
    // 处理聊天消息
    this.addMessage(message);
  }
});

// 发送消息
function sendMessage(content, sessionId = 0) {
  const message = {
    type: 'message',
    session_id: sessionId,
    content: content,
    timestamp: new Date().toISOString()
  };
  
  ws.send({
    data: JSON.stringify(message)
  });
}
```

### 管理会话

```javascript
// 获取会话列表
wx.request({
  url: 'https://your-domain.com/v1/chat/sessions',
  method: 'GET',
  header: {
    'Authorization': `Bearer ${token}`
  },
  success: (res) => {
    console.log('会话列表:', res.data.data.sessions);
  }
});

// 创建新会话
wx.request({
  url: 'https://your-domain.com/v1/chat/sessions',
  method: 'POST',
  header: {
    'Authorization': `Bearer ${token}`,
    'Content-Type': 'application/json'
  },
  data: {
    title: '新对话'
  },
  success: (res) => {
    console.log('创建会话成功:', res.data.data);
  }
});
``` 