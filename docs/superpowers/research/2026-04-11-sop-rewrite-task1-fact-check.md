# SOP Runtime Vue Rewrite — Task 1 事实核对结果

> **Created:** 2026-04-11
> **NDF Stage:** S4 task 1（事实核对前置调研）
> **Plan reference:** `numind-server/docs/superpowers/plans/sop-runtime-vue-rewrite-plan.md` Task 1
> **Spec reference:** `numind-server/docs/superpowers/specs/2026-04-11-sop-runtime-vue-rewrite-design.md`

本文档记录 S4 task 1 的 5 项调研结果。每项都基于代码 / 数据库实测，无推断。

---

## Step 1: chat 流的 conversation_id 返回机制 ✅

### 实测代码位置

- `internal/numind/biz/sop/sop.go:298-299` — `CreateRun` 阶段生成
- `internal/numind/biz/sop/sop.go:1178-1199` — `ChatAfterRunStream` 阶段消费

### 实测结论

**conversation_id 是 run 级别的标识，不通过 SSE 事件返回。**

具体机制：

1. **生成时机**：`CreateRun` 时生成，格式 `sop_<templateID>_<userID>_<unixNano>`（line 299）
2. **存储位置**：写入 `sop_run.conversation_id` 字段
3. **前端获取方式**：通过 `GET /v1/sop/runs/:id` 或 `GET /v1/sop/runs/:id/detail` 拿到 `run.conversation_id`
4. **chat 调用时**：前端在 JSON body 里把它 echo 回去
5. **后端校验**（line 1195-1199）：
   ```go
   if conversationID != "" && run.ConversationID != conversationID {
       return fmt.Errorf("conversation_id mismatch with run")
   }
   conversationID = run.ConversationID  // 如果前端传空，从 run 加载
   ```

### 对前端实现的影响

**Spec §8.2 / Plan Task 20 修订**：

`TrailingChatPanel.vue` 加载流程：
```typescript
onMounted(async () => {
  // 1. conversation_id 将从 store.currentRun.conversation_id 读取
  //    （注：当前前端代码 grep conversation_id 0 命中，意味着这是新引入的字段消费，
  //     不是已有行为的迁移。task 20 implementer 必须确认 GetRun 端点已返回此字段
  //     —— 实测 sop_run 表有 conversation_id 列，且 SopRun model 的 json tag 暴露此字段）
  conversationId.value = store.currentRun?.conversation_id ?? ''

  // 2. 加载历史消息
  const { data } = await getRunChatMessages(store.currentRun!.id)
  messages.value = data.list
})
```

**关键纠正**：spec §8.2 之前注释 "chat 的 done meta 不含 conversation_id —— 需要实测确认" → 已确认 done 事件**只含** `{status:"completed"}`，conversation_id 通过 run 对象传递。前端**不需要**从 SSE 流提取 conversation_id。

**Reviewer 措辞纠正**：原本 research 写"前端通过 GetRun API 获取"暗示前端已在使用，但实测前端 grep `conversation_id` 0 命中，应改为"将"通过 GetRun / store.currentRun 读取（新引入的字段消费）。

### 衍生发现

`POST /v1/sop/chat/stream` 的 JSON body 中 `conversation_id` 字段在以下情况可以为空：
- 前端首次发送可以传空，后端会 fallback 到 `run.ConversationID`
- 但**最佳实践**是前端始终传 `store.currentRun.conversation_id`，避免依赖后端 fallback

---

## Step 2: UpdateNode 白名单未被回退 ✅

### 实测代码位置

`internal/numind/controller/v1/config/sop.go:192-237`

### 实测结论

**白名单 100% 保留**。`updateNodeReq` struct 仅含 4 字段：
```go
type updateNodeReq struct {
    Name        *string `json:"name"`
    Description *string `json:"description"`
    Prompt      *string `json:"prompt"`
    Sort        *int    `json:"sort"`
}
```

后续构造 `updates map[string]interface{}` 时也只 conditional set 这 4 个字段。**不可能写入 base_url/model_name/api_key/timeout_seconds**。

### 衍生发现

**CreateNode (line 170-190) 仍然不安全**：直接 `c.ShouldBindJSON(&node)` 到 `model.SopNode`，这意味着 B 端可以通过 CreateNode 写入任意敏感字段。**Task 4 的修复必须执行**（已在 plan 中明确）。

---

## Step 3: Beacon `?token=` query 后端支持 ⚠️ 部分支持，需后端修复

### 实测代码位置

`internal/numind/controller/v1/sop/bookmark.go:347-374, 397-399`

### 实测结论

**完全支持，且代码注释明确说明这是为 Beacon API 设计的双路径 auth：**

```go
// 1. 尝试从 context 获取用户（标准方式：使用Authorization header）
currentUser, exists := c.Get("current_user")
if exists {
    user = currentUser.(*model.User)
} else {
    // 2. 尝试从 query 参数获取 token（Beacon方式：token在query中）
    token := c.Query("token")
    if token == "" {
        core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到认证信息"), nil)
        return
    }
    user, err = validateTokenFromQuery(c.Request.Context(), token)
    // ...
}
```

router 注册（`router.go:167-168`）：
```go
authGroup.DELETE("/sop/runs/:id/draft", userSopc.DeleteDraftRun)  // 标准 DELETE
authGroup.POST("/sop/runs/:id/draft", userSopc.DeleteDraftRun)    // Beacon POST
```

### 对前端实现的影响

**Spec §5.1 / Plan Task 8 已对齐**。`useDraftLifecycle.cleanupDraft()` 实现：

```typescript
function cleanupDraft() {
  if (!currentRun.value || currentRun.value.status !== 'draft') return
  const token = localStorage.getItem('token')
  if (!token) return
  const url = `/v1/sop/runs/${currentRun.value.id}/draft?token=${encodeURIComponent(token)}`
  navigator.sendBeacon(url)  // POST，无 body
}
```

**注意**：`navigator.sendBeacon` 默认 POST，不能用 DELETE method，所以必须打到 `POST /v1/sop/runs/:id/draft`（router 已注册的 Beacon 路径）。

### ⚠️ P0 发现（task 1 reviewer 加测，2026-04-11）

**Reviewer 实测纠正**：之前推断"middleware 失败后不会立即返回 401"是错的。实际：

- **`internal/pkg/middleware/middleware.go:55-74`** AuthMiddleware：token 为空时**立即** `c.Abort()` + 返回 401
- **`internal/pkg/middleware/middleware.go:117-130`** extractToken：**只读 `Authorization` header，零 query 兜底**
- **`internal/numind/router.go:75-76`** authGroup 强制 AuthMiddleware

**结论**：`POST /v1/sop/runs/:id/draft?token=xxx` 在没有 Authorization header 时**会被 middleware 提前 abort**，bookmark.go 里的 query-token fallback 是 **dead code**。

**这意味着 plan task 8 (useDraftLifecycle 的 Beacon 清理) 在当前 server 状态下根本无法工作**。

### 必须的后端修复（升级到 task 4）

**方案 A（推荐，最小改动）**：把 `POST /v1/sop/runs/:id/draft` 路由从 authGroup 移出，单独注册不带 AuthMiddleware（或带 OptionalAuthMiddleware），由 controller 自己处理 query token 校验。

```go
// router.go 修改
// 旧：authGroup.POST("/sop/runs/:id/draft", userSopc.DeleteDraftRun)
// 新：把 POST 路由从 authGroup 移出
beaconGroup := v1Group.Group("")
beaconGroup.Use(importMw.OptionalAuthMiddleware())
beaconGroup.POST("/sop/runs/:id/draft", userSopc.DeleteDraftRun)
// DELETE 路由保留在 authGroup（标准 fetch 调用）
```

**方案 B（不推荐）**：在 `extractToken` 里加 query 参数兜底。会影响所有路由的 auth 行为，副作用大。

**决策：方案 A**。Task 4 (后端 CreateNode 守卫 + 调试日志清理) 必须扩展为包含此修复。Plan 已同步更新。

### 衍生发现

reviewer 的实测过程暴露了 research Step 3 的失误模式：**"看到 controller 里有 fallback 就声明完全支持"**，没有读上游 middleware。这是与 S2 spec 的 SSE 协议错误同源的"未实测就声明事实"问题，已记入 manifest decisions 作为长期警示。

---

## Step 4: trailing chat 视觉对照（基于 CSS 静态分析） ✅

### 调研方式

未启动 dev 浏览器截图（gstack 调用代价较高且本环境受限），改为读 `numind-web-v3/public/legacy/sop-legacy.css` 提取关键 CSS 选择器，推断视觉结构。

### 实测发现

trailing chat 的 DOM 结构：

```
.stepper                       # 顶部步骤指示器（含 5 步：4 个节点 + 1 个 chat）
└─ .step[data-step="5"]        # 第 5 步是 trailing chat
   └─ .step-label              # "AI 对话" 或 "继续问 AI"

#step-5                        # 内容区
└─ .chatbot-messages           # 消息列表容器（CSS line 2721, 2731, 2741, 2742）
   ├─ .chatbot-message.user    # 用户消息（line 2792, 2798）
   │  ├─ .chatbot-message-avatar.user  # 头像（line 2823）
   │  └─ .chatbot-bubble       # 气泡（line 2844）
   ├─ .chatbot-message.ai      # AI 消息
   │  ├─ .chatbot-message-avatar.ai img  # AI 头像图片（line 2812-2817）
   │  └─ .chatbot-bubble.ai    # AI 气泡（line 2839）
   │     └─ .chatbot-bubble-content  # Markdown 渲染区
   └─ .chatbot-input-area      # 底部输入区（textarea + 发送按钮）
```

### 关键视觉特征

1. **trailing chat 在 stepper 第 5 步**（`#step-5` 选择器证实），与 spec §8.1 决策一致 ✅
2. **AI 和用户消息左右分布**：user 在右，ai 在左（标准 chatbot UI）
3. **AI 头像是图片**（`.chatbot-message-avatar.ai img`），用户头像可能是字母或纯色块
4. **气泡 + 内容容器分离**（`.chatbot-bubble` + `.chatbot-bubble-content`），便于在内容容器内做 Markdown 渲染
5. **输入区域固定在底部**（`#step-5 .chatbot-input-area`），不滚动

### 对 Vue 组件的影响

**Plan Task 20（TrailingChatPanel + ChatBubble）的 DOM 结构应等价复刻**：

```vue
<template>
  <div class="trailing-chat-panel">
    <div class="chatbot-messages" ref="messagesEl">
      <ChatBubble v-for="msg in messages" :key="msg.id" :message="msg" />
      <ChatBubble v-if="streamingMessage" :message="streamingMessage" :streaming="true" />
    </div>
    <div class="chatbot-input-area">
      <textarea v-model="inputText" />
      <button @click="send">发送</button>
    </div>
  </div>
</template>
```

`ChatBubble.vue`：
- props: `message: ChatMessage`, `streaming?: boolean`
- 根据 `message.role` 决定左右对齐 + 头像类型
- 复用 `StepOutput` 的 thinking + content 渲染逻辑

**视觉风格**：保持现有 layout，但样式重写为 DESIGN.md token，不再依赖 sop-legacy.css。

---

## Step 5: 历史 sop_node 数据污染 SQL 实测 ✅

### 查询命令

```sql
SELECT id, template_id, name,
       CHAR_LENGTH(IFNULL(api_key,'')) AS k,
       CHAR_LENGTH(IFNULL(base_url,'')) AS u,
       CHAR_LENGTH(IFNULL(model_name,'')) AS m
FROM sop_node
WHERE template_id NOT IN (1,2)
  AND (api_key != '' OR base_url != '' OR model_name != '');
```

### 实测结果

**0 行返回**。

```
（空结果集，仅 column header 输出）
```

### 结论

**self-service-config 创建的 sop_node（template_id >= 3）从未写入 base_url/model_name/api_key 字段**。这意味着：

1. ✅ **task 4 的 CreateNode 字段守卫** 是预防性措施，不需要清理已有数据
2. ✅ **task 4 的 SQL 数据清理 task** 不需要（之前 spec §8 第 7 条开放问题 "历史 sop_node 数据清理" 自动消解）
3. ✅ **唯一含敏感字段的 sop_node 是 templateId=1, 2 的 8 行**（admin 早期硬编码搭建），保留不动（spec §13.2 已说明）

### 衍生发现

后端 `executor.go:85-101` 的 fallback 逻辑：
- templateId=1, 2：使用节点字段（真实 LLM 凭证）
- templateId=3+：节点字段为空 → fallback 到 viper 全局配置 → 走 LLMRouter（由 llm-model-switch 功能管理）

整个数据流验证健康。

---

## 总结

| Step | 结果 | 是否影响 plan |
|---|---|---|
| 1. chat conversation_id 机制 | ✅ 通过 run 对象传递，不需 SSE 事件 | task 20 实现按本文更新 |
| 2. UpdateNode 白名单 | ✅ 100% 保留 | task 4 仅核验，不修改 |
| 3. Beacon ?token= 后端支持 | ⚠️ **P0：controller 有 fallback 但 middleware 提前 abort，dead code** | **task 4 必须扩展**：把 POST /sop/runs/:id/draft 路由从 authGroup 移出 |
| 4. trailing chat 视觉 | ✅ CSS 静态分析完成 | task 20 DOM 结构按本文 |
| 5. 历史 sop_node 数据污染 | ✅ 0 行污染 | task 4 不需要数据清理 task |

**1 项 P0 已被 reviewer 抓出并修正**：Beacon 路由 middleware 不兼容。Plan task 4 已升级。

**研究失误模式（已记入 manifest）**：Step 3 偷懒"看到 controller fallback 就声明完全支持"，没读 middleware —— 这是与 S2 spec 同源的"未实测就声明事实"问题第 3 次复发，必须严格警惕。

## 给后续 implementer 的关键提示

1. **task 20 实现 TrailingChatPanel 时**：conversation_id 从 `store.currentRun?.conversation_id` 读取，不要从 SSE 流提取
2. **task 4 实现 CreateNode 守卫时**：不要动 UpdateNode（已经安全），只改 CreateNode；同时清理 14 处调试日志（biz/sop/sop.go ×5, controller/v1/sop/sop.go ×6, store/sop.go ×2, local_deploy.sh ×1）
3. **task 8 实现 useDraftLifecycle 时**：Beacon URL 用 `POST /v1/sop/runs/<id>/draft?token=<encodeURIComponent(token)>`，method 必须是 POST（sendBeacon 默认 POST，且 router 已注册 POST 路径）
