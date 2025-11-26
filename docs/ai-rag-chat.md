# AI RAG 聊天功能详细文档

## 文档信息

- **文档版本**: v1.0
- **创建日期**: 2024年
- **最后更新**: 2024年
- **维护者**: 开发团队
- **状态**: 生产环境可用

---

## 目录

1. [功能概述](#功能概述)
2. [架构设计](#架构设计)
3. [技术栈](#技术栈)
4. [核心组件](#核心组件)
5. [工作流程](#工作流程)
6. [API 接口文档](#api-接口文档)
7. [配置说明](#配置说明)
8. [使用示例](#使用示例)
9. [数据模型](#数据模型)
10. [性能优化](#性能优化)
11. [容量规划](#容量规划)
12. [安全与隐私](#安全与隐私)
13. [故障排查](#故障排查)
14. [未来规划](#未来规划)
15. [附录](#附录)

---

## 功能概述

### 1.1 功能简介

AI RAG（Retrieval-Augmented Generation，检索增强生成）聊天功能是一个基于向量数据库的智能对话系统，允许用户基于自己的笔记内容进行智能问答。系统通过语义检索找到最相关的笔记内容，然后结合大语言模型生成准确、相关的回答。

### 1.2 核心特性

- **语义检索**: 使用向量相似度搜索，而非简单的关键词匹配
- **多笔记支持**: 可以基于单篇笔记或所有笔记进行对话
- **数据隔离**: 严格的数据隔离机制，确保用户只能访问自己的笔记
- **轻量级设计**: 使用嵌入式向量数据库，无需额外服务
- **智能过滤**: 自动过滤并选择最相关的 Top 3 条笔记内容
- **流式响应**: 支持流式返回 AI 回答（通过现有聊天系统）

### 1.3 应用场景

1. **笔记问答**: "我的笔记中提到了什么？"
2. **内容总结**: "帮我总结一下这篇笔记的主要内容"
3. **知识检索**: "我在哪篇笔记中记录了关于XX的内容？"
4. **跨笔记查询**: "我所有笔记中关于XX的内容有哪些？"

---

## 架构设计

### 2.1 系统架构图

```
┌─────────────────────────────────────────────────────────────┐
│                        客户端层                              │
│  (小程序/Web/API客户端)                                       │
└──────────────────────┬──────────────────────────────────────┘
                       │ HTTP/WebSocket
┌──────────────────────▼──────────────────────────────────────┐
│                    Controller 层                            │
│  ┌────────────────────────────────────────────────────┐    │
│  │  RagController (RAG聊天接口)                        │    │
│  │  ChatController (聊天记录查询)                       │    │
│  └────────────────────────────────────────────────────┘    │
└──────────────────────┬──────────────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────────────┐
│                     Business 层                              │
│  ┌──────────────────┐  ┌──────────────────┐                │
│  │   RagService     │  │   ChatBiz        │                │
│  │  (RAG核心逻辑)    │  │  (聊天业务逻辑)   │                │
│  └──────────────────┘  └──────────────────┘                │
└──────────────────────┬──────────────────────────────────────┘
                       │
        ┌──────────────┼──────────────┐
        │              │              │
┌───────▼──────┐ ┌─────▼──────┐ ┌────▼──────┐
│  AliBiz      │ │ Chromem DB │ │  Store    │
│ (Embedding)  │ │ (向量数据库)│ │ (MySQL)   │
└──────────────┘ └────────────┘ └───────────┘
```

### 2.2 数据流

```
用户问题
   │
   ├─► 1. Embedding 向量化 (AliBiz.QianwenEmbedding)
   │
   ├─► 2. 向量检索 (Chromem.QueryEmbedding)
   │      ├─ 检索 Top 10 条相似笔记
   │      └─ 过滤出 Top 3 条相关笔记
   │
   ├─► 3. Prompt 组装
   │      ├─ 拼接笔记内容
   │      └─ 构造系统提示词
   │
   ├─► 4. LLM 生成回答 (AliBiz.QianwenTextStream)
   │
   └─► 5. 返回回答给用户
```

### 2.3 组件职责

| 组件 | 职责 |
|------|------|
| RagController | 处理 HTTP 请求，参数验证，调用服务层 |
| RagService | RAG 核心逻辑：向量检索、Prompt 组装、LLM 调用 |
| AliBiz | 提供 Embedding 和文本生成能力 |
| Chromem DB | 向量数据库，存储和检索笔记向量 |
| ChatBiz | 管理聊天会话和消息存储 |

---

## 技术栈

### 3.1 核心技术

- **编程语言**: Go 1.24+
- **Web 框架**: Gin
- **向量数据库**: chromem-go v0.7.0 (嵌入式，轻量级)
- **LLM 服务**: 阿里百炼 (Qwen)
- **Embedding 模型**: text-embedding-v3
- **文本生成模型**: qwen-turbo

### 3.2 依赖库

```go
github.com/philippgille/chromem-go v0.7.0  // 向量数据库
github.com/gin-gonic/gin v1.10.1          // Web 框架
github.com/spf13/viper v1.20.1            // 配置管理
```

### 3.3 设计原则

1. **轻量级**: 使用嵌入式向量数据库，无需额外服务
2. **可扩展**: 模块化设计，易于扩展和维护
3. **高性能**: 向量检索 + 内存过滤，快速响应
4. **安全性**: 严格的数据隔离和权限控制

---

## 核心组件

### 4.1 RagService

**位置**: `internal/numind/biz/rag/rag_service.go`

**主要方法**:

```go
// ChatWithRAG 基于笔记进行RAG对话
func (r *RagService) ChatWithRAG(
    ctx context.Context, 
    userID uint, 
    question string, 
    bookIDs []uint  // 必填，笔记ID数组，用于基于多个笔记进行聊天
) (string, error)
```

**功能**:
1. 将用户问题转化为向量
2. 在向量数据库中检索相似笔记
3. 过滤并选择最相关的 Top 3 条笔记
4. 组装 Prompt 并调用 LLM 生成回答

### 4.2 AliBiz.QianwenEmbedding

**位置**: `internal/numind/biz/ali/ali.go`

**功能**: 调用阿里百炼 Embedding API，将文本转化为向量

**API 端点**: `https://dashscope.aliyuncs.com/compatible-mode/v1/embeddings`

**模型**: `text-embedding-v3`

**返回**: `[]float32` (向量数组)

### 4.3 Chromem 向量数据库

**Collection 名称**: `books`

**文档结构**:
```go
Document {
    ID: "book_{bookID}",           // 文档ID
    Embedding: []float32,          // 向量（1536维）
    Metadata: {
        "user_id": "123",          // 用户ID（用于隔离）
        "book_id": "456",          // 笔记ID
        "content": "笔记内容..."    // 笔记内容
    },
    Content: "笔记内容..."
}
```

### 4.4 向量数据库持久化机制

**存储格式**: `.gob` 文件（Go 二进制序列化格式）

**存储路径**（自动计算）:

系统会根据以下优先级确定存储路径：

1. **显式配置**: `rag.vector_db_path`（如果在配置文件中指定）
2. **自动计算**: 基于 `resource.image_path` 智能计算（推荐）
   ```
   规则1: 如果父目录是 "image"，向上一级后创建 vector_db
          /opt/numind/dev/image/upload => /opt/numind/dev/vector_db
   
   规则2: 否则在父目录下创建 vector_db
          /Users/.../res/upload => /Users/.../res/vector_db
   ```
3. **默认路径**: `./data/vector_db`（仅无配置时）

**各环境路径示例**:
- Dev:  `/opt/numind/dev/vector_db/`
- QA:   `/opt/numind/qa/vector_db/`
- Prod: `/opt/numind/prod/vector_db/`
- Local: `/Users/.../res/vector_db/`

**文件结构**:
```
data/vector_db/
└── 6e317bcd/              # Collection ID (books集合的唯一标识)
    ├── 00000000.gob       # Collection元数据文件 (82B)
    ├── 8c52a8e0.gob       # Document向量文件 (10KB)
    ├── 1944c93e.gob       # Document向量文件 (6.3KB)
    ├── 7af4b5b6.gob       # Document向量文件 (17KB)
    └── ...                # 其他笔记的向量文件
```

**文件说明**:

1. **00000000.gob** - Collection 元数据文件
   - 存储 Collection 的名称、配置等信息
   - 大小：约 82B
   - **必不可少**：删除后无法识别 Collection

2. **其他 .gob 文件** - Document 向量数据文件
   - 每个文件存储一个或多个笔记的向量数据
   - 文件名：随机生成的十六进制哈希值
   - 包含内容：
     - 笔记向量（1536维 float32 数组，约6KB）
     - 笔记元数据（user_id, book_id, content）
     - 笔记原文内容
   - 大小：6KB - 17KB（取决于笔记内容长度）

**重要性级别**: 🔴 **极其重要，不可删除**

**为什么不能删除**:
1. ❌ **数据完全丢失**: 删除 gob 文件会导致所有笔记向量数据永久丢失
2. ❌ **RAG 功能失效**: 没有向量数据，无法进行语义检索，RAG 聊天功能完全不可用
3. ❌ **无法自动恢复**: 虽然笔记原文存储在 MySQL 中，但需要重新调用 Embedding API 向量化所有笔记
   - 成本：每篇笔记约 0.0001 元（Embedding API 费用）
   - 时间：1000篇笔记约需 5-10 分钟重新向量化
4. ❌ **服务中断**: 在重新向量化完成前，RAG 聊天功能完全不可用

**数据恢复方式**:
如果误删除，只能通过以下方式恢复：
```bash
# 1. 重启服务，触发自动向量化
# 系统会检查所有笔记，自动向量化未向量化的笔记

# 2. 手动触发向量化（未来可实现管理接口）
curl -X POST "http://localhost:9091/v1/admin/rag/rebuild" \
  -H "Authorization: Bearer ADMIN_TOKEN"
```

**备份建议**:
```bash
# 定期备份向量数据库
tar -czf vector_db_backup_$(date +%Y%m%d).tar.gz data/vector_db/

# 保留最近7天的备份
find . -name "vector_db_backup_*.tar.gz" -mtime +7 -delete
```

**磁盘空间估算**:
- 单篇笔记向量：6-17KB
- 1000篇笔记：约 6-17MB
- 10000篇笔记：约 60-170MB
- 100000篇笔记：约 600MB-1.7GB

### 4.5 RagController

**位置**: `internal/numind/controller/v1/rag/rag.go`

**路由**: `POST /v1/rag/chat`

**请求结构**:
```json
{
  "question": "用户问题",
  "book_id": 123  // 可选
}
```

**响应结构**:
```json
{
  "code": 0,
  "message": "OK",
  "data": {
    "answer": "AI生成的回答"
  }
}
```

### 4.6 异步向量化机制

**位置**: `internal/numind/biz/rag/rag_service.go`

**核心方法**:
- `AddBookVector`: 添加笔记向量
- `UpdateBookVector`: 更新笔记向量
- `DeleteBookVector`: 删除笔记向量
- `CheckBookVectorExists`: 检查向量是否存在

**触发时机**:
1. **笔记创建时**: 笔记创建成功后，在后台异步向量化
2. **笔记更新时**: 笔记内容更新后，异步更新向量
3. **笔记删除时**: 笔记删除后，异步清理向量
4. **系统启动时**: 检查历史笔记，自动向量化未向量化的笔记

**工作流程**:
```
笔记创建/更新
   │
   ├─► 提取笔记内容（ProcessedText 或 OriginalText）
   │
   ├─► 启动独立 goroutine（不阻塞主流程）
   │
   ├─► 调用 AliBiz.QianwenEmbedding 生成向量
   │      └─ 耗时：200-500ms（取决于文本长度）
   │
   ├─► 存储向量到 chromem 数据库
   │      └─ 耗时：10-50ms（本地数据库操作）
   │
   └─► 记录日志（成功/失败）
```

**性能特点**:
- **异步执行**: 不阻塞笔记创建/更新流程
- **容错性**: 向量化失败不影响笔记操作，只记录错误日志
- **自动重试**: 系统启动时自动检查并向量化历史笔记
- **去重机制**: 检查向量是否已存在，避免重复向量化

---

## 工作流程

### 5.1 完整流程

```
1. 用户发送问题
   │
   ├─► Controller 接收请求
   │   ├─ 验证用户身份
   │   ├─ 解析请求参数
   │   └─ 调用 RagService
   │
2. 向量化问题
   │
   ├─► 调用 AliBiz.QianwenEmbedding
   │   ├─ 构造 Embedding API 请求
   │   ├─ 发送到阿里百炼
   │   └─ 获取问题向量
   │
3. 向量检索
   │
   ├─► 调用 Chromem.QueryEmbedding
   │   ├─ 检索所有用户笔记（Top 50）
   │   ├─ 内存过滤: 筛选出属于指定 bookIDs 的笔记
   │   └─ 按相似度排序，取 Top 3 条
   │
4. Prompt 组装
   │
   ├─► 拼接笔记内容
   │   ├─ 格式: "【笔记 1】\n内容...\n\n"
   │   └─ 构造系统提示词
   │
5. LLM 生成回答
   │
   ├─► 调用 AliBiz.QianwenTextStream
   │   ├─ 构造 messages
   │   ├─ 流式调用 LLM
   │   └─ 返回完整回答
   │
6. 返回结果
   │
   └─► Controller 返回 JSON 响应
```

### 5.2 数据隔离机制

1. **向量检索阶段**: 使用 `where` 条件过滤 `user_id`
2. **内存过滤阶段**: 
   - 再次验证 `Metadata["user_id"]` 匹配
   - 验证 `Metadata["book_id"]` 是否在指定的 `book_ids` 数组中
3. **双重保障**: 确保用户只能访问自己的笔记，且只能访问指定的笔记

### 5.3 检索策略

- **Top 50 检索**: 向量数据库返回相似度最高的 50 条（扩大检索范围以支持多笔记过滤）
- **多笔记过滤**: 内存中筛选出属于指定 `book_ids` 的笔记
- **Top 3 选择**: 从过滤后的结果中选择最相关的 3 条
- **原因**: 支持多笔记整合，同时平衡检索质量和响应速度

### 5.4 异步向量化流程

**笔记创建时的向量化**:
```
1. 用户创建笔记
   │
   ├─► 笔记保存到 MySQL（立即返回）
   │
   └─► 后台异步向量化（不阻塞）
       │
       ├─► 提取笔记内容
       │   ├─ 优先使用 ProcessedText
       │   └─ 如果为空，使用 OriginalText
       │
       ├─► 调用 Embedding API（200-500ms）
       │   └─ 生成 1536 维向量
       │
       ├─► 存储到向量数据库（10-50ms）
       │   ├─ 删除旧向量（如果存在）
       │   └─ 插入新向量
       │
       └─► 记录日志（成功/失败）
```

**笔记更新时的向量化**:
```
1. 用户更新笔记
   │
   ├─► 笔记更新到 MySQL（立即返回）
   │
   └─► 后台异步更新向量（不阻塞）
       │
       ├─► 删除旧向量
       │
       └─► 添加新向量（流程同创建）
```

**系统启动时的历史笔记向量化**:
```
1. 系统启动
   │
   ├─► 初始化 RAG 服务
   │
   └─► 后台异步检查历史笔记
       │
       ├─► 分批获取笔记（每批100条）
       │
       ├─► 检查向量是否已存在
       │   ├─ 已存在：跳过
       │   └─ 不存在：向量化
       │
       └─► 记录统计信息
           ├─ 总处理数
           ├─ 已向量化数
           └─ 已跳过数
```

**性能特点**:
- **非阻塞**: 所有向量化操作在独立 goroutine 中执行
- **容错性**: 向量化失败不影响笔记操作
- **自动恢复**: 系统启动时自动处理未向量化的笔记
- **去重优化**: 避免重复向量化

---

## API 接口文档

### 6.1 基于笔记的 RAG 聊天

**接口**: `POST /v1/rag/chat`

**认证**: 需要 JWT Token

**请求头**:
```
Authorization: Bearer {token}
Content-Type: application/json
```

**请求体**:
```json
{
  "question": "我的笔记中提到了什么？",  // 必填
  "book_ids": [123, 456, 789]          // 必填，笔记ID数组，至少包含1个ID
}
```

**响应**:
```json
{
  "code": 0,
  "message": "OK",
  "data": {
    "answer": "根据您的笔记内容，..."
  }
}
```

**错误响应**:
```json
{
  "code": 1,
  "message": "RAG对话失败: 生成问题向量失败: ...",
  "data": null
}
```

**使用场景**:

1. **单笔记聊天** (book_ids 数组包含1个ID):
```json
{
  "question": "这篇笔记的主要内容是什么？",
  "book_ids": [123]
}
```

2. **多笔记整合聊天** (book_ids 数组包含多个ID):
```json
{
  "question": "这些笔记中关于Python的内容有哪些？",
  "book_ids": [123, 456, 789]
}
```

**注意**: `book_ids` 是必填字段，必须至少包含1个笔记ID。系统会基于指定的多个笔记进行整合检索和回答。

### 6.2 获取笔记聊天记录

**接口**: `GET /v1/chat/book/:book_id/history`

**说明**: 这是现有的聊天接口，用于获取笔记的聊天记录

**查询参数**:
- `limit`: 返回消息数量限制（默认 50，最大 200）

**响应**:
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
      "message_count": 10
    },
    "messages": [
      {
        "id": 1,
        "role": "user",
        "content": "这个笔记讲了什么？",
        "created_at": "2024-01-01T10:00:00Z"
      },
      {
        "id": 2,
        "role": "assistant",
        "content": "根据您的笔记内容...",
        "created_at": "2024-01-01T10:00:01Z"
      }
    ],
    "total": 10
  }
}
```

---

## 配置说明

### 7.1 配置文件

**位置**: `config_local.yaml` / `config_prod.yaml`

**配置项**:

```yaml
# 阿里云配置
ali:
  # 通用配置
  api_key: "sk-xxx"  # 文本服务API密钥
  
  # 文本生成服务
  text:
    model: "qwen-turbo"
    timeout: 180s
    api_key: "sk-xxx"  # 如果单独配置
  
  # Embedding服务（用于RAG）
  embedding:
    model: "text-embedding-v3"
    timeout: 60s

# RAG配置
rag:
  vector_db_path: "./data/vector_db"  # 向量数据库存储路径
```

### 7.2 环境变量

无需额外环境变量，所有配置通过配置文件管理。

### 7.3 配置说明

| 配置项 | 说明 | 默认值 | 必填 |
|--------|------|--------|------|
| `ali.text.api_key` | 阿里百炼 API Key | - | 是 |
| `ali.text.model` | 文本生成模型 | `qwen-turbo` | 是 |
| `ali.embedding.model` | Embedding 模型 | `text-embedding-v3` | 是 |
| `rag.vector_db_path` | 向量数据库路径 | `./data/vector_db` | 否 |

---

## 使用示例

### 8.1 cURL 示例

**单笔记聊天**:
```bash
curl -X POST "http://localhost:8080/v1/rag/chat" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "question": "这篇笔记的主要内容是什么？",
    "book_ids": [123]
  }'
```

**多笔记整合聊天**:
```bash
curl -X POST "http://localhost:8080/v1/rag/chat" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "question": "这些笔记中关于机器学习的内容有哪些？",
    "book_ids": [123, 456, 789]
  }'
```

### 8.2 JavaScript 示例

```javascript
// 基于笔记的 RAG 聊天
async function chatWithRAG(question, bookIds) {
  const response = await fetch('http://localhost:8080/v1/rag/chat', {
    method: 'POST',
    headers: {
      'Authorization': `Bearer ${token}`,
      'Content-Type': 'application/json'
    },
    body: JSON.stringify({
      question: question,
      book_ids: bookIds  // 必填，数组格式，至少包含1个ID
    })
  });
  
  const data = await response.json();
  if (data.code === 0) {
    return data.data.answer;
  } else {
    throw new Error(data.message);
  }
}

// 使用示例
// 单笔记聊天
const answer1 = await chatWithRAG('这篇笔记的主要内容是什么？', [123]);
console.log(answer1);

// 多笔记整合聊天
const answer2 = await chatWithRAG('这些笔记中关于Python的内容有哪些？', [123, 456, 789]);
console.log(answer2);
```

### 8.3 Go 客户端示例

```go
package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
)

type ChatRequest struct {
    Question string `json:"question"`
    BookIDs  []uint `json:"book_ids"`  // 必填，数组格式，至少包含1个ID
}

type ChatResponse struct {
    Code    int    `json:"code"`
    Message string `json:"message"`
    Data    struct {
        Answer string `json:"answer"`
    } `json:"data"`
}

func ChatWithRAG(baseURL, token, question string, bookIDs []uint) (string, error) {
    reqBody := ChatRequest{
        Question: question,
        BookIDs:  bookIDs,
    }
    
    jsonData, _ := json.Marshal(reqBody)
    req, _ := http.NewRequest("POST", baseURL+"/v1/rag/chat", bytes.NewBuffer(jsonData))
    req.Header.Set("Authorization", "Bearer "+token)
    req.Header.Set("Content-Type", "application/json")
    
    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    
    var result ChatResponse
    json.NewDecoder(resp.Body).Decode(&result)
    
    if result.Code != 0 {
        return "", fmt.Errorf(result.Message)
    }
    
    return result.Data.Answer, nil
}
```

---

## 数据模型

### 9.1 向量数据库文档结构

```go
type Document struct {
    ID        string            // "book_{bookID}"
    Embedding []float32         // 1536维向量（text-embedding-v3）
    Metadata  map[string]string // 元数据
    Content   string            // 笔记内容
}
```

**Metadata 字段**:
- `user_id`: 用户ID（用于数据隔离）
- `book_id`: 笔记ID
- `content`: 笔记内容（冗余存储，便于检索）

### 9.2 聊天数据模型

使用现有的 `ChatSession` 和 `ChatMessage` 模型：

```go
type ChatSession struct {
    ID          uint
    UserID     uint
    BookID     *uint  // 关联的笔记ID
    Title      string
    Status     string
    MessageCount int
}

type ChatMessage struct {
    ID        uint
    SessionID uint
    UserID    uint
    Role      string  // "user" | "assistant"
    Content   string
    Status    string
}
```

---

## 性能优化

### 10.1 检索优化

1. **向量检索**: 使用 chromem-go 的近似最近邻搜索（ANN）
2. **Top-K 限制**: 先检索 Top 10，再内存过滤 Top 3
3. **索引优化**: chromem-go 自动维护向量索引

### 10.2 缓存策略

- **向量缓存**: 笔记向量在向量数据库中持久化
- **无内存缓存**: 当前实现未使用内存缓存（可扩展）

### 10.3 并发处理

- **HTTP 客户端**: 使用连接池和重试机制
- **向量检索**: chromem-go 支持并发查询

### 10.4 性能指标

#### 10.4.1 RAG 聊天性能

| 操作 | 平均耗时 | 说明 |
|------|----------|------|
| Embedding 生成（问题） | 200-500ms | 取决于文本长度 |
| 向量检索 | 50-200ms | 取决于数据量 |
| LLM 生成 | 1-5s | 取决于回答长度 |
| **总耗时** | **1.5-6s** | 端到端响应时间 |

#### 10.4.2 异步向量化性能

**单篇笔记向量化耗时**:

| 操作步骤 | 平均耗时 | 说明 |
|---------|----------|------|
| 提取笔记内容 | <1ms | 内存操作 |
| Embedding API 调用 | 200-500ms | 取决于文本长度（500-2000字） |
| 向量存储到数据库 | 10-50ms | 本地 SQLite 操作 |
| **总耗时** | **210-550ms** | 单篇笔记向量化时间 |

**详细分析**:

1. **Embedding API 调用**（主要耗时）:
   - 500字笔记：约 200-300ms
   - 1000字笔记：约 300-400ms
   - 2000字笔记：约 400-500ms
   - 影响因素：
     - 网络延迟（10-50ms）
     - API 处理时间（150-400ms）
     - 文本长度（越长越慢）

2. **向量存储**（次要耗时）:
   - 删除旧向量（如果存在）：5-20ms
   - 插入新向量：5-30ms
   - 影响因素：
     - 数据库大小（越大越慢）
     - 磁盘 I/O 性能
     - 索引维护

**批量向量化性能**:

| 场景 | 处理方式 | 总耗时估算 |
|------|---------|-----------|
| 系统启动时历史笔记向量化 | 分批处理（每批100条） | 100条笔记约 21-55秒 |
| 单篇笔记创建时 | 独立 goroutine | 不阻塞主流程（210-550ms） |
| 单篇笔记更新时 | 独立 goroutine | 不阻塞主流程（210-550ms） |

**性能优化建议**:

1. **并发控制**: 系统启动时的历史笔记向量化采用分批处理，避免一次性处理过多数据
2. **异步执行**: 所有向量化操作在独立 goroutine 中执行，不阻塞主流程
3. **错误处理**: 向量化失败不影响笔记操作，只记录错误日志
4. **去重检查**: 避免重复向量化已存在的笔记

---

## 容量规划

### 11.1 基于笔记数量的容量分析

本系统使用轻量级嵌入式向量数据库（chromem-go，基于SQLite），适合中小型业务场景。以下是基于笔记数量的容量规划分析。

#### 11.1.1 单条笔记存储估算

**存储组成**（假设笔记平均1000字，500-2000字范围）：
- 笔记内容：1000字 ≈ **2KB**（UTF-8编码）
- 向量数据：1536维 × 4字节 = **6KB**
- 元数据：user_id, book_id, content等 ≈ **1KB**
- 索引开销：SQLite索引 ≈ **1KB**
- **总计：约 10KB/条笔记**

**实际存储考虑**（含20-30%放大）：
- 单条笔记：10KB × 1.25 = **12.5KB**（考虑SQLite页大小、索引碎片等）

#### 11.1.2 不同笔记数量的容量需求

| 笔记数量 | 存储大小 | 性能表现 | 检索时间 | 建议 |
|---------|---------|---------|---------|------|
| **1万条** | ~100MB | ✅ 优秀 | <50ms | 无需优化 |
| **10万条** | ~1GB | ✅ 良好 | 50-100ms | 无需优化 |
| **50万条** | ~5GB | ✅ 良好 | 100-200ms | 无需优化 |
| **100万条** | ~10-12.5GB | ⚠️ 可接受 | 200-500ms | 开始监控 |
| **500万条** | ~50-62.5GB | ⚠️ 性能下降 | 500ms-2s | 需要优化 |
| **1000万条** | ~100-125GB | ❌ 性能严重下降 | >2s | 必须升级 |

#### 11.1.3 关键阈值

**阈值1：10万条笔记（1GB）**
- **状态**：✅ 性能良好
- **检索时间**：50-100ms
- **建议**：无需优化，正常使用

**阈值2：100万条笔记（10-12.5GB）**
- **状态**：⚠️ 性能可接受
- **检索时间**：200-500ms
- **建议**：
  - 开始监控数据库大小和查询性能
  - 建立性能基线
  - 准备优化方案

**阈值3：500万条笔记（50-62.5GB）**
- **状态**：⚠️ 性能开始下降
- **检索时间**：500ms-2s
- **建议**：
  - 实施数据归档策略
  - 考虑分库分表
  - 评估迁移到专业向量数据库

**阈值4：1000万条笔记（100-125GB）**
- **状态**：❌ 性能严重下降
- **检索时间**：>2s
- **建议**：
  - 必须升级系统
  - 迁移到分布式向量数据库（Milvus、Qdrant等）

### 11.2 性能与数据量关系

#### 11.2.1 查询性能模型

向量检索性能与数据量的关系：

```
检索时间 ≈ 基础时间 + (数据量 / 10000) × 单位时间

基础时间：50ms（索引查找）
单位时间：0.1ms/万条（向量计算）
```

**实际性能示例**：
- 10万条：50ms + (10/1) × 0.1ms ≈ **51ms**
- 100万条：50ms + (100/1) × 0.1ms ≈ **60ms**
- 500万条：50ms + (500/1) × 0.1ms ≈ **100ms**
- 1000万条：50ms + (1000/1) × 0.1ms ≈ **150ms**

**注意**：实际性能还受以下因素影响：
- 索引质量
- 内存大小
- 并发查询数
- 磁盘I/O性能

#### 11.2.2 chromem-go（SQLite）性能限制

| 数据量范围 | 性能表现 | 建议 |
|-----------|---------|------|
| 0-10GB | ✅ 性能良好 | 正常使用 |
| 10-50GB | ⚠️ 性能可接受 | 开始监控 |
| 50-100GB | ⚠️ 性能下降 | 需要优化 |
| >100GB | ❌ 性能严重下降 | 必须升级 |

**SQLite 理论限制**：
- 单文件最大：281TB（理论值）
- 实际建议：< 50GB（性能考虑）
- 超过50GB后建议迁移到专业向量数据库

### 11.3 升级建议时间点

#### 11.3.1 基于笔记数量的升级时间表

**阶段1：0-100万条笔记**
- **状态**：✅ 无需优化
- **存储**：<10GB
- **性能**：优秀到良好
- **建议**：正常使用，定期备份

**阶段2：100-500万条笔记**
- **状态**：⚠️ 开始监控
- **存储**：10-50GB
- **性能**：可接受到下降
- **建议**：
  - 监控数据库大小和查询性能
  - 实施数据归档（删除旧笔记向量）
  - 定期清理无效数据
  - 考虑分库策略

**阶段3：500-1000万条笔记**
- **状态**：⚠️ 需要优化
- **存储**：50-100GB
- **性能**：明显下降
- **建议**：
  - 必须实施数据归档
  - 评估迁移到专业向量数据库
  - 考虑分库分表（按用户ID或时间）

**阶段4：>1000万条笔记**
- **状态**：❌ 必须升级
- **存储**：>100GB
- **性能**：严重下降
- **建议**：
  - 立即迁移到分布式向量数据库
  - 推荐方案：Milvus、Qdrant、Pinecone

#### 11.3.2 具体升级时间点

**保守建议**：
- **50万条笔记**：开始建立监控体系
- **100万条笔记**：开始准备优化方案
- **300-500万条笔记**：实施优化或开始迁移评估
- **500-800万条笔记**：完成迁移准备
- **1000万条笔记**：必须完成升级

**激进建议**（追求最佳性能）：
- **50万条笔记**：开始监控
- **100万条笔记**：开始优化
- **200-300万条笔记**：评估迁移方案
- **500万条笔记**：完成迁移

**实际建议**：
- 在达到 **300-500万条笔记** 时开始评估升级方案
- 在达到 **800-1000万条笔记** 前完成迁移

### 11.4 优化策略（延长使用时间）

#### 11.4.1 数据归档策略

```yaml
# 只保留最近N年的笔记向量
retention_period: 2  # 年

# 效果示例：
# 1000万条 → 400万条（假设每年200万条）
# 存储：100GB → 40GB
```

**实施建议**：
- 定期清理超过保留期的笔记向量
- 保留原始笔记数据（MySQL），仅删除向量
- 需要时可重新生成向量

#### 11.4.2 分库分表策略

```yaml
# 按用户ID分库，每个库<10GB
shard_by_user_id: true
shard_size: 1000  # 每1000用户一个库

# 效果：分散存储，提高查询性能
```

**实施建议**：
- 按用户ID哈希分库
- 每个库独立维护，互不影响
- 查询时路由到对应库

#### 11.4.3 定期清理策略

```yaml
# 删除已删除笔记的向量
cleanup_interval: "7d"  # 每周清理一次

# 效果：减少无效数据，提高性能
```

**实施建议**：
- 定期扫描已删除的笔记
- 清理对应的向量数据
- 减少数据库膨胀

### 11.5 监控指标

建议监控以下指标，及时发现容量问题：

```yaml
# 监控配置
monitoring:
  # 数据库大小
  db_size_check: "daily"
  db_size_threshold: 10GB  # 告警阈值
  
  # 查询性能
  query_time_threshold: 500ms  # 查询时间告警
  
  # 数据增长
  growth_rate_check: "weekly"
  growth_rate_threshold: 1GB/month  # 月增长告警
  
  # 笔记数量
  note_count_check: "daily"
  note_count_threshold: 1000000  # 100万条告警
```

**监控脚本示例**：

```bash
#!/bin/bash
# 检查向量数据库大小
DB_SIZE=$(du -sh ./data/vector_db | awk '{print $1}')
echo "向量数据库大小: $DB_SIZE"

# 检查笔记数量（需要实现统计接口）
NOTE_COUNT=$(curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost:8080/v1/admin/rag/stats | jq '.note_count')
echo "笔记数量: $NOTE_COUNT"

# 检查查询性能
AVG_QUERY_TIME=$(grep "RAG对话完成" numind.log | \
  grep -oP "duration: \K[0-9.]+" | \
  awk '{sum+=$1; count++} END {print sum/count}')
echo "平均查询时间: ${AVG_QUERY_TIME}ms"
```

### 11.6 迁移方案

当达到容量上限时，建议迁移到专业向量数据库：

#### 11.6.1 推荐方案

**方案1：Milvus（推荐）**
- **优势**：分布式、高性能、开源
- **适用**：大规模数据（百万级以上）
- **部署**：需要独立服务

**方案2：Qdrant**
- **优势**：轻量级、性能优秀、RESTful API
- **适用**：中等规模数据（十万到百万级）
- **部署**：可独立服务或嵌入式

**方案3：Pinecone**
- **优势**：托管服务、无需运维
- **适用**：快速上线、不想运维
- **部署**：云服务，按使用量付费

#### 11.6.2 迁移步骤

1. **评估阶段**（1-2周）
   - 评估数据量和增长趋势
   - 选择合适的向量数据库
   - 设计迁移方案

2. **准备阶段**（2-4周）
   - 搭建新向量数据库环境
   - 开发数据迁移工具
   - 测试迁移流程

3. **迁移阶段**（1-2周）
   - 分批迁移数据
   - 验证数据完整性
   - 性能测试

4. **切换阶段**（1周）
   - 灰度切换
   - 监控性能
   - 全量切换

### 11.7 总结

**关键数字**：

| 笔记数量 | 状态 | 行动 |
|---------|------|------|
| **<100万条** | ✅ 无需优化 | 正常使用 |
| **100-500万条** | ⚠️ 开始监控 | 建立监控，准备优化 |
| **500-1000万条** | ⚠️ 需要优化 | 实施优化或评估迁移 |
| **>1000万条** | ❌ 必须升级 | 迁移到专业向量数据库 |

**建议的升级时间点**：
- **保守方案**：在达到 **500-800万条笔记** 时完成升级准备
- **激进方案**：在达到 **200-300万条笔记** 时开始评估迁移
- **实际建议**：在达到 **300-500万条笔记** 时开始评估升级方案，在达到 **800-1000万条笔记** 前完成迁移

**优化策略**：
- 数据归档：保留最近2年数据
- 分库分表：按用户ID分库
- 定期清理：删除无效向量数据

---

## 数据备份与维护

### 11.8 向量数据库备份

**重要性**: 🔴 极其重要

向量数据库存储在 `.gob` 文件中，这些文件包含了所有笔记的向量数据。一旦丢失，需要重新调用 Embedding API 向量化所有笔记，会产生额外成本和时间。

#### 11.8.1 容器化环境配置

**Docker Compose 示例**:
```yaml
version: '3.8'
services:
  numind-dev:
    image: numind-server:dev
    volumes:
      # 挂载持久化目录（包含图片和向量数据库）
      - /data/numind/dev:/opt/numind/dev
    environment:
      - ENV=dev
    # 系统会自动计算 vector_db 路径:
    # /opt/numind/dev/image/upload => /opt/numind/dev/vector_db
```

**宿主机目录结构**:
```bash
/data/numind/dev/
├── image/
│   └── upload/          # 图片上传目录（持久化）
└── vector_db/           # 向量数据库目录（持久化）✅
    └── 6e317bcd/
        ├── 00000000.gob
        └── *.gob
```

---

#### 11.8.2 备份策略

**每日备份**（推荐）:
```bash
#!/bin/bash
# 备份向量数据库（支持多环境）
ENV=${1:-"dev"}  # 默认 dev 环境
BASE_PATH="/opt/numind/${ENV}"
VECTOR_DB_PATH="${BASE_PATH}/vector_db"
BACKUP_DIR="/backup/vector_db"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)

# 创建备份
tar -czf ${BACKUP_DIR}/vector_db_${ENV}_${TIMESTAMP}.tar.gz ${VECTOR_DB_PATH}

# 保留最近7天的备份
find ${BACKUP_DIR} -name "vector_db_${ENV}_*.tar.gz" -mtime +7 -delete

echo "✅ 备份完成: vector_db_${ENV}_${TIMESTAMP}.tar.gz"
```

**使用方法**:
```bash
# 备份不同环境
./backup_vector_db.sh dev
./backup_vector_db.sh qa
./backup_vector_db.sh prod
```

**增量备份**（高频场景）:
```bash
# 使用 rsync 增量备份（根据环境调整路径）
ENV=dev
rsync -avz --delete /opt/numind/${ENV}/vector_db/ /backup/vector_db_${ENV}_sync/
```

#### 11.8.3 恢复流程

**场景1：数据完全丢失**
```bash
# 1. 停止服务
systemctl stop numind-dev

# 2. 恢复备份（根据环境调整路径）
ENV=dev
tar -xzf /backup/vector_db/vector_db_${ENV}_20241124.tar.gz -C /

# 3. 重启服务
systemctl start numind-dev

# 4. 验证数据
curl -X POST "https://youshu.asia/dev/api/rag/chat" \
  -H "Authorization: Bearer TOKEN" \
  -d '{"question":"测试","book_ids":[469]}'
```

**场景2：部分数据损坏**
```bash
# 1. 停止服务
systemctl stop numind-dev

# 2. 删除损坏的向量数据
ENV=dev
rm -rf /opt/numind/${ENV}/vector_db/

# 3. 重启服务（会自动重新向量化所有笔记）
systemctl start numind-dev

# 注意：自动向量化会调用 Embedding API，产生费用
# 成本估算：每篇笔记约 0.0005 元，1000篇约 0.5 元
```

#### 11.8.4 文件说明

**gob 文件格式**:
- **格式**: Go 标准库 `encoding/gob` 二进制序列化
- **优势**: 高效、类型安全、Go 原生支持
- **劣势**: 只能被 Go 程序读取

**文件命名规则**:
- `00000000.gob`: Collection 元数据文件（必须存在）
- `{hash}.gob`: Document 向量数据文件（文件名为随机哈希）

**单个 gob 文件内容**:
```go
// Document 向量文件包含
{
    ID: "book_469",                    // 文档ID
    Embedding: [0.123, -0.456, ...],   // 1536维向量（约6KB）
    Metadata: {
        "user_id": "2",                // 用户ID
        "book_id": "469",              // 笔记ID
        "content": "笔记完整内容..."    // 笔记文本
    },
    Content: "笔记完整内容..."
}
```

**容量规划**:
- 单篇笔记：6-17KB（取决于笔记长度）
- 1000篇笔记：约 6-17MB
- 10000篇笔记：约 60-170MB
- 实际案例：13个文件，总大小 172KB

#### 11.8.5 监控与告警

**监控指标**:
```yaml
监控项:
  - 向量数据库目录大小
  - gob 文件数量
  - 最后修改时间
  - 磁盘空间使用率

告警阈值:
  - 目录大小超过 10GB
  - 文件数量超过 100万个
  - 磁盘空间使用率超过 80%
```

**监控脚本**:
```bash
#!/bin/bash
# 检查向量数据库状态（支持多环境）

ENV=${1:-"dev"}
DB_PATH="/opt/numind/${ENV}/vector_db"

if [ ! -d "$DB_PATH" ]; then
    echo "❌ 向量数据库目录不存在: $DB_PATH"
    exit 1
fi

DB_SIZE=$(du -sh $DB_PATH | awk '{print $1}')
FILE_COUNT=$(find $DB_PATH -name "*.gob" | wc -l)

echo "向量数据库状态 (${ENV}):"
echo "  路径: $DB_PATH"
echo "  大小: $DB_SIZE"
echo "  文件数: $FILE_COUNT"
echo "  最后修改: $(stat $DB_PATH | grep Modify)"
```

**使用方法**:
```bash
# 检查不同环境
./check_vector_db.sh dev
./check_vector_db.sh qa
./check_vector_db.sh prod
```

#### 11.8.6 灾难恢复

**最坏情况**：向量数据完全丢失且无备份

**恢复步骤**:
1. 系统会在启动时自动检测并向量化历史笔记
2. 进程会在后台运行，不阻塞服务启动
3. 根据笔记数量，恢复时间估算：
   - 100篇笔记：约 1-2 分钟
   - 1000篇笔记：约 5-10 分钟
   - 10000篇笔记：约 50-100 分钟

**费用估算**（重新向量化）:
- Embedding API：约 ¥0.0007/千tokens
- 平均每篇笔记：500字 ≈ 750 tokens
- 单篇成本：约 ¥0.0005
- 1000篇笔记：约 ¥0.5
- 10000篇笔记：约 ¥5

**建议**:
- ✅ 每天备份向量数据库
- ✅ 保留至少7天的备份
- ✅ 重要环境（生产）保留30天备份
- ✅ 定期测试恢复流程

---

## 安全与隐私

### 12.1 数据隔离

1. **向量检索阶段**: 使用 `where` 条件过滤 `user_id`
2. **内存过滤阶段**: 再次验证 `Metadata["user_id"]`
3. **双重保障**: 确保用户只能访问自己的笔记

### 12.2 认证授权

- **JWT Token**: 所有接口需要有效的 JWT Token
- **用户验证**: Controller 层验证用户身份
- **权限检查**: 确保用户只能访问自己的数据

### 12.3 数据安全

- **向量数据库**: 本地存储，不暴露到公网
- **gob 文件**: 存储在 `data/vector_db/` 目录，必须加入备份计划
- **API Key**: 存储在配置文件中，不提交到代码仓库  
- **HTTPS**: 生产环境必须使用 HTTPS

**数据保护措施**:
```bash
# 1. 限制目录权限
chmod 700 data/vector_db/

# 2. 添加到 .gitignore（避免误提交）
echo "data/vector_db/" >> .gitignore

# 3. 定期备份（见 11.8 节）
tar -czf vector_db_backup.tar.gz data/vector_db/
```

**数据保护措施**:
```bash
# 1. 限制目录权限
chmod 700 data/vector_db/

# 2. 添加到 .gitignore（避免误提交）
echo "data/vector_db/" >> .gitignore

# 3. 定期备份
# 见 11.8 节"向量数据库备份"
```

### 12.4 隐私保护

- **数据不出域**: 向量数据库和聊天记录都在用户自己的服务器上
- **无第三方共享**: 不向第三方共享用户数据
- **日志脱敏**: 日志中不记录敏感信息

---

## 故障排查

### 13.1 常见问题

#### 问题 1: Embedding 生成失败

**症状**: 返回错误 "生成问题向量失败"

**可能原因**:
1. API Key 配置错误
2. 网络连接问题
3. 阿里百炼服务异常

**解决方案**:
```bash
# 1. 检查配置
grep "api_key" config_local.yaml

# 2. 测试网络连接
curl -X POST "https://dashscope.aliyuncs.com/compatible-mode/v1/embeddings" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"text-embedding-v3","input":["test"]}'

# 3. 查看日志
tail -f numind.log | grep "Embedding"
```

#### 问题 2: 向量检索返回空结果

**症状**: 检索不到相关笔记

**可能原因**:
1. 向量数据库为空（笔记未向量化）
2. 用户ID不匹配
3. 笔记内容为空

**解决方案**:
```bash
# 1. 检查向量数据库
ls -la ./data/vector_db/

# 2. 检查笔记是否已向量化
# 注意：当前实现需要手动触发向量化（未来可扩展）

# 3. 查看日志
tail -f numind.log | grep "检索"
```

#### 问题 3: LLM 生成失败

**症状**: 返回错误 "LLM生成回答失败"

**可能原因**:
1. API Key 配置错误
2. 模型服务异常
3. 请求超时

**解决方案**:
```bash
# 1. 检查配置
grep "ali.text" config_local.yaml

# 2. 测试 LLM API
curl -X POST "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen-turbo","messages":[{"role":"user","content":"test"}]}'

# 3. 查看日志
tail -f numind.log | grep "LLM"
```

### 13.2 日志分析

**关键日志位置**:
```bash
# RAG 相关日志
grep "RAG\|Embedding\|检索" numind.log

# 错误日志
grep "ERROR\|FAIL" numind.log

# 性能日志
grep "耗时\|duration" numind.log
```

### 13.3 调试模式

**启用详细日志**:
```yaml
# config_local.yaml
log:
  level: debug  # 启用 debug 级别日志
```

---

## 未来规划

### 14.1 短期优化（1-3个月）

1. **自动向量化**: ✅ 已完成 - 笔记创建/更新时自动生成向量
2. **批量向量化**: ✅ 已完成 - 系统启动时自动检查并向量化历史笔记
3. **缓存优化**: 添加向量和回答的缓存机制
4. **流式响应**: 支持流式返回 RAG 回答

### 14.2 中期规划（3-6个月）

1. **多模态支持**: 支持图片、音频等多媒体笔记
2. **语义分块**: 长笔记自动分块，提高检索精度
3. **个性化优化**: 基于用户历史优化检索策略
4. **A/B 测试**: 支持不同的检索和生成策略

### 14.3 长期规划（6-12个月）

1. **知识图谱**: 构建笔记间的关联关系
2. **智能推荐**: 基于笔记内容推荐相关问题
3. **多语言支持**: 支持多语言笔记的检索和回答
4. **离线模式**: 支持本地 LLM 和向量检索

---

## 附录

### A.1 相关文档

- [RAG_CHAT_TEST.md](./RAG_CHAT_TEST.md) - 测试文档
- [API_USAGE_GUIDE.md](./API_USAGE_GUIDE.md) - API 使用指南
- [CONFIG_FINAL_SUMMARY.md](./CONFIG_FINAL_SUMMARY.md) - 配置说明

### A.2 代码位置

| 组件 | 文件路径 |
|------|----------|
| RagService | `internal/numind/biz/rag/rag_service.go` |
| RagController | `internal/numind/controller/v1/rag/rag.go` |
| AliBiz.Embedding | `internal/numind/biz/ali/ali.go` |
| 路由配置 | `internal/numind/router.go` |

### A.3 依赖版本

```go
github.com/philippgille/chromem-go v0.7.0
github.com/gin-gonic/gin v1.10.1
github.com/spf13/viper v1.20.1
```

### A.4 联系方式

- **技术支持**: 开发团队
- **问题反馈**: GitHub Issues
- **文档更新**: 本文档随代码更新

### A.5 更新日志

**v1.1 (2024-XX-XX)**
- ✅ 实现异步向量化机制
- ✅ 笔记创建/更新时自动向量化
- ✅ 系统启动时自动检查并向量化历史笔记
- ✅ 笔记删除时自动清理向量
- ✅ 添加向量化性能指标文档

**v1.0 (2024-XX-XX)**
- 初始版本发布
- 实现基础的 RAG 聊天功能
- 支持单笔记和跨笔记查询
- 完成文档编写

---

## 总结

本文档详细介绍了 AI RAG 聊天功能的实现、使用和维护。这是一个轻量级、高性能、安全可靠的智能对话系统，为用户提供了基于笔记内容的智能问答能力。

**核心优势**:
- ✅ 轻量级设计，无需额外服务
- ✅ 严格的数据隔离和权限控制
- ✅ 高性能的向量检索
- ✅ 易于扩展和维护

**适用场景**:
- 个人笔记管理和查询
- 知识库问答系统
- 内容检索和总结

如有任何问题或建议，欢迎反馈！

---

**文档结束**

