# SOP 运行页 Vue 3 完整重写 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 退役 7518 行 legacy vanilla JS（`numind-web-v3/public/legacy/sop-legacy.js`）+ 1019 行 Vue hydration wrapper（`SOPView.vue`），用 Vue 3 + Composition API + TypeScript 完整重写 SOP 运行页。**核心原则：数据库 = 唯一真相源，前端零硬编码**。顺手修复 P0 安全漏洞（GetTemplateNodes 泄露 5 字段）。

**Architecture:**
- 后端：新建 SopNodePublicDTO/SopTemplatePublicDTO，GetTemplateNodes 改用 DTO，CreateNode 加字段白名单守卫，清理调试日志
- 前端：8 个 composables + 1 个 Pinia store + 11 个 Vue 组件 + 2 个新建公共组件（ConfirmModal、AppNotification）+ 1 个 stores/uiDialogs（事件总线）+ markdown.ts 工具
- 删除：sop-legacy.js (7518 行) + sop-legacy.css + 旧 SOPView.vue (1019 行) + 旧 stores/sop.ts (273 行)
- 策略：全量切换，无 feature flag，git revert 紧急回退

**Tech Stack:** Go/Gin/GORM (backend), Vue 3.4 / TypeScript 5.4 / Vite 5 / Pinia 2 / Vue Router 4 (frontend), Playwright (E2E)

**Repos:** numind-server（Tasks 2-4, 含调试日志清理）, numind-web-v3（Tasks 1, 5-25）

**Spec:** `numind-server/docs/superpowers/specs/2026-04-11-sop-runtime-vue-rewrite-design.md`

**Branches:** `feature/sop-runtime-vue-rewrite`（两仓库各自一支）

---

## 依赖图（决定并行性）

```
Task 1 (事实核对) ─┬─ 阻塞所有
                  │
Task 2 (DTO) ──┬─→ Task 3 (GetTemplateNodes) ──→ Task 25 (curl 验证安全)
              │
              └─→ Task 4 (CreateNode 守卫 + 调试日志清理)
                       │
                       └─→ 后端完成 ─────────────→ Task 25
                                                       │
Task 5 (types + store 骨架) ──→ Task 6 (useSSEStream)  │
                              ├─→ Task 7 (useScrollFollow)
                              ├─→ Task 8 (useDraftLifecycle)
                              ├─→ Task 9 (useInputPersistence)
                              ├─→ Task 10 (useFileUpload + useBookmarks)
                              ├─→ Task 11 (useStepNavigation)
                              ├─→ Task 12 (markdown.ts)
                              ├─→ Task 13 (ConfirmModal + AppNotification)
                              └─→ Task 14 (uiDialogs store + App.vue 接入)
                                       │
              composables 完成 ────────┴───→ Task 15-21 (Vue 组件)
                                                  │
                                            Task 21 (SOPRunView 主集成)
                                                  │
                                            Task 22 (删除 legacy 文件)
                                                  │
                                            Task 23 (Playwright E2E)
                                                  │
                                            Task 24 (lint/typecheck/test)
                                                  │
                                            Task 25 (部署 dev + 冒烟)
```

**可并行组：**
- 后端 Task 2-4 与前端 Task 5 后的所有 composables 可完全并行（不同仓库）
- composables Task 6-12 之间无依赖，可同时分发给多个 implementer subagent
- Task 13-14 与 composables 并行
- 组件 Task 15-20 之间大部分独立，可并行（仅 Task 21 主集成需等所有）

---

## NDF Rule 6: 每个 Task 必须双 review

**每完成一个 task → commit → dispatch Sonnet subagent 1 (Spec Compliance Review) → dispatch Sonnet subagent 2 (Code Quality Review) → 全部 PASS 才能开始下一 task**

P0 必须立即修复并重新 review。P1 必须立即修复。P2 能现在修则现在修（仅当依赖未就绪才推迟）。

---

## Task 1: 事实核对（前置阻塞）

**依赖：** 无
**仓库：** 仅调研，不改代码
**约工作量：** 0.5 天

### 目的

S2 spec reviewer 标记的若干"待实测"事项，必须先核对完毕，否则后续 task 可能基于错误前提。

### 核对清单

- [ ] **Step 1: chat 流的 conversation_id 返回机制**
  - 读 `numind-server/internal/numind/biz/sop/sop.go` 的 `ChatAfterRunStream` 函数实现，找出 conversation_id 何时通过哪个 SSE 事件返回给前端
  - 输出：在本 task 的最后写一个 `numind-server/docs/superpowers/research/2026-04-11-sop-chat-conversation-id-mechanism.md`，说明 conversation_id 的传递机制（done meta? 自定义 event? 通过其他方式?）

- [ ] **Step 2: 核验 UpdateNode 白名单未被回退**
  - 读 `numind-server/internal/numind/controller/v1/config/sop.go:192-237`
  - 确认 `updateNodeReq` struct 字段集合 = `{Name, Description, Prompt, Sort}` 且没有 base_url/model_name/api_key 字段
  - 如已被回退或未严格 binding：标记为 P0，task 4 必须修复

- [ ] **Step 3: Beacon `?token=` query 后端实测**
  - 读 `numind-server/internal/numind/controller/v1/sop/bookmark.go` 的 `DeleteDraftRun` 函数
  - 确认是否真的支持 `?token=<jwt>` query 参数提取 token（middleware 是否对此 endpoint 跳过 Authorization header 检查？）
  - 如不支持：标记为 P0，必须先在后端补丁让 beacon endpoint 接受 query token

- [ ] **Step 4: trailing chat 视觉对照**
  - 启动 dev 环境（或 local），用 `gstack /qa` 截图当前 legacy SOP 运行页 templateId=1 的 trailing chat 第 5 步样式
  - 保存截图到 `numind-server/docs/superpowers/research/screenshots/2026-04-11-trailing-chat-legacy.png`
  - Vue 组件实现时作为视觉对照参考

- [ ] **Step 5: 历史 sop_node 数据污染检查**
  - SQL 实测：`SELECT id, template_id, name, CHAR_LENGTH(IFNULL(api_key,'')) AS k, CHAR_LENGTH(IFNULL(base_url,'')) AS u FROM sop_node WHERE template_id NOT IN (1,2) AND (api_key != '' OR base_url != '');`
  - 确认 self-service-config 之前 B 端是否写入过 base_url/model_name/api_key
  - 如有：在本 task 报告中列出 row 数 + 决定是否在本次清理（推荐：本次不清理，记入 deferred）

### DoD

- [ ] 5 项核对全部完成
- [ ] 核对结果写入 research 目录
- [ ] 任何 P0 / P1 发现已上报，相关 task 已调整

### Commit

```
chore(sop): S3 task 1 - 事实核对前置调研

5 项核对完成：
- chat 流 conversation_id 机制
- UpdateNode 白名单未被回退
- Beacon ?token= query 后端支持
- trailing chat 视觉截图
- 历史 sop_node 数据污染情况
```

---

## Task 2: 后端 DTO 定义 + 单测

**依赖：** Task 1
**仓库：** numind-server
**文件：**
- 新建：`internal/pkg/model/dto/sop.go`
- 新建：`internal/pkg/model/dto/sop_test.go`

**约工作量：** 0.5 天

### Steps

- [ ] **Step 1：创建 DTO 文件**

参考 spec §1.2 和 §1.3。定义：
- `SopNodePublicDTO`（C 端用）：id / template_id / name / description / sort / status / created_at / updated_at —— 隐藏 5 字段
- `SopNodeEditDTO`（B 端用，本次本不需要但可顺手定义）：上述 + prompt
- `SopTemplatePublicDTO`：id / name / description / status / publish_status / trailing_chat_enabled / created_at / updated_at
- `ToSopNodePublicDTO(node *model.SopNode) SopNodePublicDTO`
- `ToSopTemplatePublicDTO(t *model.SopTemplate) SopTemplatePublicDTO`

- [ ] **Step 2：单元测试**

`sop_test.go` 测试用例：
- 给一个含全部敏感字段（api_key="secret"、base_url="https://..."、model_name="x"、prompt="y"）的 SopNode → 调用 ToSopNodePublicDTO → assert json.Marshal 后**不包含** "api_key", "base_url", "model_name", "timeout_seconds", "prompt" 任何子串
- 同理测试 SopTemplate → SopTemplatePublicDTO 不暴露 prompt 和 creator_user_id

### DoD

- [ ] DTO 文件存在
- [ ] 单测通过 `go test ./internal/pkg/model/dto/...`
- [ ] `task lint` 通过

### Commit

```
feat(sop): 添加 SopNodePublicDTO 和 SopTemplatePublicDTO

新建 dto 包，定义 C 端 SOP 运行时所需的 DTO 类型。
隐藏后端敏感字段：api_key/base_url/model_name/timeout_seconds/prompt。
含单元测试验证字段不泄露。

NDF: sop-runtime-vue-rewrite task 2
```

---

## Task 3: 后端 GetTemplateNodes 改造 + curl 验证

**依赖：** Task 2
**仓库：** numind-server
**文件：**
- 修改：`internal/numind/controller/v1/sop/sop.go`（GetTemplateNodes 函数）

**约工作量：** 0.3 天

### Steps

- [ ] **Step 1：改写 GetTemplateNodes**

参考 spec §1.4 和 §2.3。新返回结构：
```go
core.WriteResponse(c, nil, gin.H{
    "template": dto.ToSopTemplatePublicDTO(template),
    "nodes":    nodeDTOs,
    "total":    len(nodeDTOs),
})
```

注意：当前函数顶层是 `template_id` / `template_name` / `trailing_chat_enabled`，新版改为 `template` 对象。这是 breaking change，但实测 legacy 前端 0 消费 nodes 中的 5 字段（grep 验证），且老版字段被新版 template 对象包含。

- [ ] **Step 2：curl 验证敏感字段消失**

```bash
# 启动 dev 后
TOKEN=<dev token>
curl -s -H "Authorization: Bearer $TOKEN" \
  http://49.233.219.254:9091/v1/sop/templates/1/nodes | jq

# 断言 nodes[0] 不含敏感字段
curl -s -H "Authorization: Bearer $TOKEN" \
  http://49.233.219.254:9091/v1/sop/templates/1/nodes \
  | jq '.data.nodes[0] | keys' \
  | grep -Ec '(api_key|base_url|model_name|timeout_seconds|prompt)'
# 期望输出: 0
```

### DoD

- [ ] 函数重写完成
- [ ] curl 实测：response 顶层有 `template` 对象，nodes[*] 不含 5 个敏感字段
- [ ] `task lint` 通过
- [ ] 现有 test 通过 `go test ./internal/numind/controller/v1/sop/...`

### Commit

```
fix(sop)(security): GetTemplateNodes 改用 DTO，修复 P0 字段泄露

旧实现直接序列化 model.SopNode，将 LLM 凭证（api_key/base_url/model_name）
和 B 端核心 IP（prompt）暴露给所有 C 端登录用户。

新实现：
- 使用 SopNodePublicDTO 隐藏 5 个敏感字段
- 使用 SopTemplatePublicDTO 扩展 template 元信息
- 顶层结构改为 { template, nodes, total }（breaking change，但 legacy 前端实测 0 消费）

NDF: sop-runtime-vue-rewrite task 3
```

---

## Task 4: 后端 CreateNode 字段守卫 + Beacon 路由修复 + 调试日志清理

**依赖：** Task 1, Task 2
**仓库：** numind-server
**文件：**
- 修改：`internal/numind/controller/v1/config/sop.go`（CreateNode 白名单）
- 修改：`internal/numind/router.go`（**Beacon POST 路由从 authGroup 移出**，task 1 reviewer 发现的 P0）
- 修改：`internal/numind/biz/sop/sop.go`（清理 **5 处** `~/Desktop/...` 调试日志，行 262/275/311/323/337）
- 修改：`internal/numind/controller/v1/sop/sop.go`（清理 **6 处** `~/Desktop/...` 调试日志）
- 修改：`internal/numind/store/sop.go`（清理 **2 处** `~/Desktop/...` 调试日志）
- 修改：`local_deploy.sh`（清理 1 处 `Desktop/莫小派合作` 引用，如适用）

**约工作量：** 0.6 天（+0.1 来自 Beacon 路由修复）

> 实测：`grep -rn "Desktop/莫小派合作" numind-server/` 命中 **14 处跨 4 个 .go/.sh 文件**，spec §2.1 和初版 plan 错把行号 262/275/311/323/337 标为 `controller/v1/sop/sop.go`，实际是 `biz/sop/sop.go`。已修正。
>
> **task 1 reviewer 发现 P0（必须包含在本 task）**：`POST /v1/sop/runs/:id/draft?token=xxx` 当前在 authGroup 内，AuthMiddleware 在 token header 缺失时 c.Abort() 提前 401，bookmark.go 里的 query token fallback 是 dead code。task 8 的 Beacon 清理依赖修复此问题。修复方案：把 POST 路由从 authGroup 移到 OptionalAuthMiddleware 组（DELETE 路由保留 authGroup）。详见 task 1 research 文档 Step 3。

### Steps

- [ ] **Step 1：CreateNode 改为白名单 binding**

参考 spec §2.2。把直接 `c.ShouldBindJSON(&node)` 到 `model.SopNode` 改为 anonymous struct 白名单：
```go
var req struct {
    TemplateID  uint   `json:"template_id" binding:"required"`
    Name        string `json:"name" binding:"required"`
    Description string `json:"description"`
    Prompt      string `json:"prompt" binding:"required"`
    Sort        int    `json:"sort"`
}
```

然后构造 `&model.SopNode{...}` 时只填白名单字段。即使前端发了 base_url/model_name/api_key/timeout_seconds，也被丢弃。

- [ ] **Step 2：单元测试**

测试用例：
- 给 CreateNode 发送一个含 `api_key="evil"` 的 JSON body → 调用后从 DB 读出新建的 node → assert `node.APIKey == ""`
- 同理测 base_url / model_name / timeout_seconds

- [ ] **Step 3：Beacon 路由修复（task 1 reviewer 发现的 P0）**

修改 `internal/numind/router.go`：

```go
// 找到当前的：
// authGroup.DELETE("/sop/runs/:id/draft", userSopc.DeleteDraftRun)
// authGroup.POST("/sop/runs/:id/draft", userSopc.DeleteDraftRun)

// 改为：
authGroup.DELETE("/sop/runs/:id/draft", userSopc.DeleteDraftRun)  // 保留：标准 fetch 调用走 header
// POST 路由独立出来，使用 OptionalAuthMiddleware（不强制 header），
// 由 controller 自己处理 query token fallback
beaconGroup := v1Group.Group("")
beaconGroup.Use(importMw.OptionalAuthMiddleware())
beaconGroup.POST("/sop/runs/:id/draft", userSopc.DeleteDraftRun)
```

**curl 实测验证：**
```bash
# 启动 dev 后
TOKEN=<dev token>

# 测试 1：标准 header（DELETE 路由），应 200
curl -X DELETE -H "Authorization: Bearer $TOKEN" \
  "http://49.233.219.254:9091/v1/sop/runs/<draft_run_id>/draft" -i
# 期望：200 OK 或 200 错误信息（如 run 不存在）

# 测试 2：Beacon 路径（POST + query token，无 header），应 200
curl -X POST "http://49.233.219.254:9091/v1/sop/runs/<draft_run_id>/draft?token=$TOKEN" -i
# 期望：200（不是 401！）

# 测试 3：POST 既无 header 又无 query token，应 401
curl -X POST "http://49.233.219.254:9091/v1/sop/runs/<draft_run_id>/draft" -i
# 期望：401 未提供认证信息
```

- [ ] **Step 4：清理调试日志**

实测位置（共 14 处跨 4 文件）：删除所有 `func() { logFile, _ := os.OpenFile("/Users/zhiyuchen/Desktop/...") }()` 块及包裹它的 `// #region agent log` / `// #endregion` 注释。

| 文件 | 行号（grep 实测） | 处数 |
|---|---|---|
| `internal/numind/biz/sop/sop.go` | 262, 275, 311, 323, 337 | 5 |
| `internal/numind/controller/v1/sop/sop.go` | 535, 550, 569, 583, 608, 633（grep 实测可能略有偏移，以实际为准） | 6 |
| `internal/numind/store/sop.go` | 177, 189 | 2 |
| `local_deploy.sh` | 1 处 | 1 |

```bash
# 实测命中位置
grep -rn "Desktop/莫小派合作" numind-server/ --include="*.go" --include="*.sh"

# 清理后验证彻底
grep -rn "Desktop/莫小派合作" numind-server/ --include="*.go" --include="*.sh"
# 期望：0 results
```

### DoD

- [ ] CreateNode 白名单生效
- [ ] 单测通过
- [ ] 调试日志全部清理
- [ ] `task lint` 通过

### Commit

```
feat(sop)(security): CreateNode 字段白名单 + 清理调试日志

- CreateNode 改为白名单 binding，拒绝写入 base_url/model_name/api_key/timeout_seconds
  到 sop_node 表（与 GetTemplateNodes 的 DTO 隐藏对齐）
- 清理 sop.go 中 5+ 处硬编码 ~/Desktop/... 调试日志（reviewer P2）

NDF: sop-runtime-vue-rewrite task 4
```

---

## Task 5: 前端 vitest 基础设施 + types.ts + Pinia store 骨架

**依赖：** Task 1
**仓库：** numind-web-v3
**文件：**
- 修改：`numind-web-v3/package.json`（新增 vitest + @vue/test-utils + jsdom）
- 新建：`numind-web-v3/vitest.config.ts`
- 新建：`numind-web-v3/src/views/sop/types.ts`
- 新建：`numind-web-v3/src/stores/sopRun.ts`

**约工作量：** 0.7 天（+ 0.2 天来自 vitest 基础设施）

> ⚠️ **本 task 是 Task 6/7/8/9/11/15/19 的硬依赖**，因为它们的 DoD 都包含单测，而 numind-web-v3 当前**完全没有 vitest 基础设施**（实测 package.json 无 vitest / @vue/test-utils；无 vitest.config.ts；scripts 仅 type-check/lint/test:e2e）

### Steps

- [ ] **Step 1：安装 vitest 基础设施**

```bash
cd numind-web-v3
npm install -D vitest @vue/test-utils jsdom @vitest/coverage-v8
```

新增 npm script 到 package.json：
```json
"scripts": {
  "test:unit": "vitest run",
  "test:unit:watch": "vitest"
}
```

- [ ] **Step 2：创建 vitest.config.ts**

```typescript
import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  plugins: [vue()],
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.{test,spec}.{ts,vue}', 'src/**/__tests__/**/*.{test,spec}.{ts,vue}'],
    exclude: ['node_modules', 'e2e', 'dist'],
  },
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    }
  }
})
```

- [ ] **Step 3：冒烟测试**

创建一个空测试 `src/__tests__/sanity.test.ts`：
```typescript
import { describe, it, expect } from 'vitest'
describe('vitest sanity', () => {
  it('runs', () => { expect(1 + 1).toBe(2) })
})
```

```bash
npm run test:unit
# 期望：1 passed
```

确认通过后，本 sanity 文件可以保留（无害）或删除。

- [ ] **Step 4：创建 types.ts**

参考 spec §3.3。定义：
- `SopRunStatus = 'draft' | 'pending' | 'running' | 'succeeded' | 'failed'`
- `SopTemplatePublic`、`SopNodePublic`、`SopRun`、`SopNodeRun`、`ExecutedTemplate`、`BookmarkItem`

注意 SopNodeRun 的 input/output/thinking 是 `string | null`（longtext 可空）。

- [ ] **Step 5：创建 sopRun.ts store 骨架**

参考 spec §3.2。state + computed + actions 签名定义，但 actions 只写空函数体（后续 task 填充）。`isDraftRun = computed(() => currentRun.value?.status === 'draft')`。

### DoD

- [ ] vitest + @vue/test-utils + jsdom 已安装
- [ ] vitest.config.ts 存在且 alias 正确
- [ ] `npm run test:unit` 通过 sanity 测试
- [ ] types.ts 和 sopRun.ts 文件存在
- [ ] `npm run type-check` 通过
- [ ] `npm run lint` 通过

### Commit

```
feat(sop): vitest 基础设施 + sopRun store 骨架 + types

- 安装 vitest + @vue/test-utils + jsdom（前端单测基础）
- 新增 vitest.config.ts，alias 与 vite.config 对齐
- 新增 test:unit npm script
- 创建 SOP 运行页 types.ts 和 sopRun store 骨架
- SopRunStatus 包含 'draft' 状态（后端独立常量 SopStatusDraft）

NDF: sop-runtime-vue-rewrite task 5
```

---

## Task 6: useSSEStream composable + 单测

**依赖：** Task 5
**仓库：** numind-web-v3
**文件：**
- 新建：`src/views/sop/composables/useSSEStream.ts`
- 新建：`src/views/sop/composables/__tests__/useSSEStream.test.ts`

**约工作量：** 1 天

### Steps

- [ ] **Step 1：实现 useSSEStream**

参考 spec §4.2。关键点：
- 心跳行 `:\n\n` 通过 `parseEventBlock` 的 `startsWith(':')` 过滤
- thinking/message/error data 是 JSON-encoded **字符串**，需要 `JSON.parse` 还原（结果还是字符串）
- done 的 data 是 JSON **对象**，`JSON.parse` 后是 `{status, uploaded_file_ids?}`
- `doneFiredRef` 保证完成动作只触发一次（处理 ExecuteNodeStream 的双 done）
- AbortController 支持组件卸载时取消

- [ ] **Step 2：单测 mock fetch**

测试用例：
1. 输入流 `event: thinking\ndata: "片段1"\n\ndata: "片段2"\n\nevent: done\ndata: {"status":"completed"}\n\n` → 验证 onThinking 收到 "片段1"，onMessage 收到 "片段2"，onDone 收到 `{status:"completed"}`
2. 心跳行 `:\n\n` → 不触发任何 handler
3. 双 done 事件 → onDone 只触发一次
4. SSE 中断（reader 抛错）→ onError 被调用
5. data 跨 chunk 边界（buffer 处理）→ 完整事件被正确组装

### DoD

- [ ] composable 实现完成
- [ ] 5 个测试用例全部 pass
- [ ] `npm run type-check` + `npm run lint` 通过

### Commit

```
feat(sop): useSSEStream composable + 单测

基于实测后端 SSE 协议格式实现：
- 心跳行 :\n\n 过滤
- thinking/message/error data 是 JSON-encoded 字符串
- done data 是 JSON 对象
- 双 done 事件幂等保护

NDF: sop-runtime-vue-rewrite task 6
```

---

## Task 7: useScrollFollow composable + 单测

**依赖：** Task 5
**仓库：** numind-web-v3
**文件：**
- 新建：`src/views/sop/composables/useScrollFollow.ts`
- 新建：`src/views/sop/composables/__tests__/useScrollFollow.test.ts`

**约工作量：** 0.7 天

### Steps

- [ ] **Step 1：实现 useScrollFollow**

参考 spec §5.2 状态机。两个状态：Following / Interrupted。
- `registerStreamingElement(el)` / `unregisterStreamingElement(el)`
- `checkAndScroll(el)`：仅在 Following 状态下自动滚到底部
- 监听 wheel 事件（deltaY < 0 = 向上滚）→ Interrupted
- 监听 touchmove（移动端向下滑 = 向上看）→ Interrupted
- `resume()`：用户点跳回底部按钮 → Following
- 流式输出新一轮开始 → 自动 resume

- [ ] **Step 2：单测**

mock DOM 元素 + scroll event：
1. 注册元素 → 触发 wheel(-1) → isInterrupted = true
2. resume() → isInterrupted = false
3. 调用 checkAndScroll 时 isInterrupted=true → 不调用 scrollIntoView

### DoD

- [ ] composable 实现完成
- [ ] 单测 pass
- [ ] type-check + lint 通过

### Commit

```
feat(sop): useScrollFollow composable + 单测

实现自动滚动跟随状态机，等价复刻 legacy scrollFollowManager。

NDF: sop-runtime-vue-rewrite task 7
```

---

## Task 8: useDraftLifecycle + Beacon 清理

**依赖：** Task 5, Task 1（Step 3 必须验证 Beacon ?token= 支持）
**仓库：** numind-web-v3
**文件：**
- 新建：`src/views/sop/composables/useDraftLifecycle.ts`
- 新建：`src/views/sop/composables/__tests__/useDraftLifecycle.test.ts`

**约工作量：** 0.7 天

### Steps

- [ ] **Step 1：实现 useDraftLifecycle**

参考 spec §5.1 状态机。Actions：
- `enterDraftMode(templateId)`：纯前端 draft 状态，不创建后端记录
- `lazyCreateRun(text)`：调用 `POST /v1/sop/runs`，处理后端创建的 status="draft" 记录
- `migrateLocalStorageKeys(oldRunId, newRunId)`：把 input 持久化键迁移
- `cleanupDraft()`：构造 `?token=<jwt>` query，调用 `navigator.sendBeacon`

- [ ] **Step 2：单测**

mock localStorage / fetch / sendBeacon：
1. enterDraftMode → 状态正确
2. lazyCreateRun → API 调用正确，迁移 localStorage key
3. cleanupDraft → sendBeacon 被调用，URL 含 token query

### DoD

- [ ] 实现 + 单测 pass
- [ ] type-check + lint 通过

### Commit

```
feat(sop): useDraftLifecycle composable + Beacon 清理

实现 Draft 模式三段生命周期 + onBeforeUnmount Beacon 清理。
基于后端 SopStatusDraft 独立状态。

NDF: sop-runtime-vue-rewrite task 8
```

---

## Task 9: useInputPersistence composable

**依赖：** Task 5
**仓库：** numind-web-v3
**文件：**
- 新建：`src/views/sop/composables/useInputPersistence.ts`

**约工作量：** 0.3 天

### Steps

- [ ] **Step 1：实现 useInputPersistence**

封装 localStorage 操作：
- `loadInputForStep(runIdOrDraft: number | string, inputId: string): string`
- `saveInputForStep(runIdOrDraft, inputId, value: string)`
- `clearInputsForRun(runIdOrDraft)`

key 模式：`sop_input_{runIdOrDraft}_{inputId}`，draft 模式 runIdOrDraft = `draft_${templateId}`。

含 `originalInputValues` 快照机制（dirty 检测，spec §7.2）。

### DoD

- [ ] 实现完成
- [ ] type-check + lint 通过
- [ ] 简单单测（mock localStorage）

### Commit

```
feat(sop): useInputPersistence composable

封装步骤输入的 localStorage 持久化 + dirty 检测。

NDF: sop-runtime-vue-rewrite task 9
```

---

## Task 10: useFileUpload + useBookmarks composables

**依赖：** Task 5
**仓库：** numind-web-v3
**文件：**
- 新建：`src/views/sop/composables/useFileUpload.ts`
- 新建：`src/views/sop/composables/useBookmarks.ts`

**约工作量：** 0.7 天

### Steps

- [ ] **Step 1：useFileUpload**

参考 spec §6。处理文件类型分流：
- 图片 → POST `/v1/ali/vision/analyze`
- PDF → POST `/v1/pdf/convert-to-text`
- 文本类 → 直接 file.text()

分离 `baseText`（用户手输）和 `uploadResults` Map，避免覆盖（spec §6.2）。

- [ ] **Step 2：useBookmarks**

参考 spec §7.2：
- `loadBookmarks(templateId)` → GET /v1/sop/templates/:id/bookmarks
- `applyBookmark(runId, nodeId, bookmarkId)` → POST .../apply-bookmark
- `isInputDirty(nodeId, currentValue): boolean` → 与 originalValues 比较

### DoD

- [ ] 两个 composable 完成
- [ ] type-check + lint 通过

### Commit

```
feat(sop): useFileUpload + useBookmarks composables

文件上传分流（OCR/PDF/文本）+ 书签系统接入。

NDF: sop-runtime-vue-rewrite task 10
```

---

## Task 11: useStepNavigation composable

**依赖：** Task 5
**仓库：** numind-web-v3
**文件：**
- 新建：`src/views/sop/composables/useStepNavigation.ts`

**约工作量：** 0.4 天

### Steps

- [ ] **Step 1：实现 useStepNavigation**

参考 spec §5.3：
- `canAccessStep(stepIndex: number): boolean`
- `setActiveStep(step: number)` —— 含 sessionStorage 持久化
- `restoreFromSession(runId)` —— 页面刷新时恢复

### DoD

- [ ] 实现完成
- [ ] type-check + lint 通过
- [ ] 单测覆盖 canAccessStep 的 4 个分支（已完成可访问 / 已完成不可访问 / 下一节点 / trailing chat 全部完成）

### Commit

```
feat(sop): useStepNavigation composable

步骤切换 + 权限检查 + sessionStorage 恢复。

NDF: sop-runtime-vue-rewrite task 11
```

---

## Task 12: markdown.ts util（marked + DOMPurify 迁移 npm）

**依赖：** Task 5
**仓库：** numind-web-v3
**文件：**
- 修改：`numind-web-v3/package.json`（新增 marked / highlight.js / dompurify 依赖，如未安装）
- 新建：`src/utils/markdown.ts`

**约工作量：** 0.3 天

### Steps

- [ ] **Step 1：核验依赖（实测已存在，跳过安装）**

实测 `numind-web-v3/package.json` 已包含：
- `marked@^17.0.3`
- `highlight.js@^11.11.1`
- `dompurify@^3.3.1`
- `@types/dompurify@^3.0.5`

（来源：现有 `src/stores/sop.ts` 已 `import { marked } from 'marked'` + `import hljs from 'highlight.js'`）

无需 `npm install`。如果 implementer 重新执行 reviewer 验证发现某依赖缺失（package.json 被改动），再补装。

- [ ] **Step 2：实现 markdown.ts**

```typescript
import { marked } from 'marked'
import hljs from 'highlight.js'
import DOMPurify from 'dompurify'

marked.setOptions({ gfm: true, breaks: true, async: false })
marked.use({
  renderer: {
    code({ text, lang }) {
      const language = lang && hljs.getLanguage(lang) ? lang : 'plaintext'
      return `<pre><code class="hljs language-${language}">${hljs.highlight(text, { language }).value}</code></pre>`
    }
  }
})

export function renderMarkdown(content: string): string {
  if (!content) return ''
  const html = marked.parse(content) as string
  return DOMPurify.sanitize(html)
}
```

### DoD

- [ ] util 文件存在并可被 import
- [ ] type-check + lint 通过

### Commit

```
feat(sop): 新增 markdown.ts util，封装 marked + highlight.js + DOMPurify

为 Vue 重写准备统一的 Markdown 渲染入口。

NDF: sop-runtime-vue-rewrite task 12
```

---

## Task 13: ConfirmModal + AppNotification 通用组件

**依赖：** Task 5
**仓库：** numind-web-v3
**文件：**
- 新建：`src/components/common/ConfirmModal.vue`
- 新建：`src/components/common/AppNotification.vue`

**约工作量：** 0.5 天

### Steps

- [ ] **Step 1：ConfirmModal.vue**

接口：
```typescript
defineProps<{
  modelValue: boolean
  title?: string
  message: string
  confirmText?: string  // 默认 "确认"
  cancelText?: string   // 默认 "取消"
  variant?: 'default' | 'danger'  // danger 时确认按钮红色
}>()

defineEmits<{
  'update:modelValue': [boolean]
  'confirm': []
  'cancel': []
}>()
```

样式：使用 DESIGN.md token，与 InsufficientCreditsDialog 风格一致。

- [ ] **Step 2：AppNotification.vue**

提供 toast 通知能力。两种用法：
- 组件方式：`<AppNotification :messages="..." />`
- 全局函数方式：`useNotification().show('成功')`，封装在 composable

实现：使用 Pinia store 管理 messages 列表（id + content + type + timeout），AppNotification 组件挂在 App.vue 显示。

- [ ] **Step 3：更新 CLAUDE.md 公共组件清单**

修改 `numind-web-v3/CLAUDE.md` §2 的公共组件清单表，在 Common 行追加 `ConfirmModal`、`AppNotification`。

### DoD

- [ ] 两个组件 + 一个 composable 完成
- [ ] CLAUDE.md 同步更新
- [ ] type-check + lint 通过

### Commit

```
feat(common): 新增 ConfirmModal + AppNotification 公共组件

为 SOP 运行页 Vue 重写提供通用确认对话框和 toast 通知组件。
更新 CLAUDE.md 公共组件清单。

NDF: sop-runtime-vue-rewrite task 13
```

---

## Task 14: stores/uiDialogs + App.vue InsufficientCreditsDialog 接入

**依赖：** Task 5
**仓库：** numind-web-v3
**文件：**
- 新建：`src/stores/uiDialogs.ts`
- 修改：`src/App.vue`

**约工作量：** 0.4 天

### Steps

- [ ] **Step 1：创建 stores/uiDialogs.ts**

参考 spec §10.1：
```typescript
export const useUiDialogsStore = defineStore('uiDialogs', () => {
  const showCredits = ref(false)
  const creditsMessage = ref('')
  function openCreditsDialog(msg: string) { ... }
  function closeCreditsDialog() { ... }
  return { showCredits, creditsMessage, openCreditsDialog, closeCreditsDialog }
})
```

- [ ] **Step 2：改造 App.vue**

把现有 `<InsufficientCreditsDialog ref="creditsDialogRef" />` 改为绑定到 store state：
```vue
<InsufficientCreditsDialog 
  v-model="dialogsStore.showCredits" 
  :message="dialogsStore.creditsMessage" />
```

注意：保留向后兼容。如果有其他组件还在用 ref 触发，先确认后再改。

### DoD

- [ ] uiDialogs store 完成
- [ ] App.vue 接入完成
- [ ] type-check + lint 通过
- [ ] 现有 InsufficientCreditsDialog 在其他页面仍然能正常显示（手动测试一个已知触发场景）

### Commit

```
refactor(common): InsufficientCreditsDialog 改用 Pinia 事件总线

新增 stores/uiDialogs.ts 作为全局 dialog 状态总线。
App.vue 改为监听 store state 显示 dialog。
消除"组件 hierarchy 通过 ref 传递"的耦合。

NDF: sop-runtime-vue-rewrite task 14
```

---

## Task 15: StepperPanel.vue 组件 + DESIGN.md token 对齐

**依赖：** Task 5, 11
**仓库：** numind-web-v3
**文件：**
- 新建：`src/views/sop/components/StepperPanel.vue`

**约工作量：** 0.7 天

### Steps

- [ ] **Step 1：实现 StepperPanel**

Props：steps, trailing-chat-enabled, current-step, completed-ids, accessibility
Emits：navigate(step: number)

横向步骤指示器，含：
- 数字 + 名称 + 状态（active / completed / disabled）
- 桌面端 > 10 步时滚动 + collapsed 视图
- 移动端横向滚动
- 点击触发 navigate emit（受 canAccessStep 约束）

样式严格对齐 DESIGN.md token + variables.css。

- [ ] **Step 2：组件单测**

vitest + @vue/test-utils：
1. 渲染 4 个 nodes + trailing chat enabled → 显示 5 个步骤
2. completed-ids 包含 1, 2 → 步骤 1, 2 显示 completed 样式
3. 点击不可访问的步骤 → 不触发 navigate emit

### DoD

- [ ] 组件完成
- [ ] 单测通过
- [ ] type-check + lint 通过

### Commit

```
feat(sop): StepperPanel.vue 步骤指示器组件

动态从后端 nodes 数据渲染（无硬编码步骤数），含可访问性控制。
对齐 DESIGN.md token。

NDF: sop-runtime-vue-rewrite task 15
```

---

## Task 16: StepInput.vue（含拖拽 + 文件上传 UI）

**依赖：** Task 5, 10, 13
**仓库：** numind-web-v3
**文件：**
- 新建：`src/views/sop/components/StepInput.vue`

**约工作量：** 0.7 天

### Steps

- [ ] **Step 1：实现 StepInput**

包含：
- textarea（绑定 useInputPersistence）
- 文件上传按钮 + 隐藏的 input[type=file]
- 拖拽区域监听（dragenter/dragover/dragleave/drop）
- 上传中 loading 状态
- 图片预览缩略图
- 通过 useFileUpload 处理文件流程

- [ ] **Step 2：本地冒烟（无单测，因为重点测在 composables 层）**

实测渲染：在 `src/views/sop/__sandbox__/StepInputSandbox.vue`（临时空 wrapper）import StepInput.vue 并传入 mock props，`npm run dev` 启动后访问 sandbox 路由，确认：
- textarea 显示
- 上传按钮显示
- 拖拽 dragenter 时 .drag-over class 出现
- mock 一个 file 上传，loading 状态显示

冒烟通过后**删除 sandbox 临时文件**。

### DoD

- [ ] 组件文件存在且无 type 错误
- [ ] 本地 sandbox 冒烟：4 个交互全部正常
- [ ] sandbox 临时文件已清理
- [ ] type-check + lint 通过

### Commit

```
feat(sop): StepInput.vue 输入区组件（textarea + 文件上传 + 拖拽）

NDF: sop-runtime-vue-rewrite task 16
```

---

## Task 17: StepOutput.vue（流式 Markdown + 思维链 + 滚动跟随）

**依赖：** Task 5, 6, 7, 12
**仓库：** numind-web-v3
**文件：**
- 新建：`src/views/sop/components/StepOutput.vue`

**约工作量：** 0.8 天

### Steps

- [ ] **Step 1：实现 StepOutput**

参考 spec §4.5。Props：thinking, content, streaming
内部：
- 思维链折叠面板（可点击切换 collapsed）
- Markdown 渲染（通过 markdown.ts util）
- 流式增量更新时调用 useScrollFollow.checkAndScroll

- [ ] **Step 2：本地冒烟**

同 task 16 模式：临时 sandbox 文件 + 4 个验证：
- 给 thinking 字符串 → 思维链面板显示
- 点击折叠 → 内容隐藏
- 给 content 字符串（含 Markdown 列表 + code block）→ 渲染为 HTML
- content 增量变化 → useScrollFollow.checkAndScroll 被调用

冒烟通过后清理 sandbox。

### DoD

- [ ] 组件文件存在且无 type 错误
- [ ] sandbox 4 项冒烟通过
- [ ] sandbox 临时文件清理
- [ ] type-check + lint 通过

### Commit

```
feat(sop): StepOutput.vue 输出区组件（流式 + 思维链 + 滚动跟随）

NDF: sop-runtime-vue-rewrite task 17
```

---

## Task 18: ToolbarActions + EmptyStateCard + ScrollFollowButton

**依赖：** Task 5, 7, 13
**仓库：** numind-web-v3
**文件：**
- 新建：`src/views/sop/components/ToolbarActions.vue`
- 新建：`src/views/sop/components/EmptyStateCard.vue`
- 新建：`src/views/sop/components/ScrollFollowButton.vue`

**约工作量：** 0.5 天

### Steps

- [ ] **Step 1：ToolbarActions**

按钮：复制 / 重新生成 / 下一步 / 上一步
- 复制：navigator.clipboard.writeText + AppNotification 提示成功
- 重新生成：dirty 检测 → 如来自书签且 dirty → 弹 ConfirmModal "将删除该书签，确认？"

- [ ] **Step 2：EmptyStateCard**

通用空状态卡片：图标 + 文字 + 可选 CTA 按钮

- [ ] **Step 3：ScrollFollowButton**

通过 useScrollFollow 控制可见性，点击时 resume()

### DoD

- [ ] 三个组件完成
- [ ] type-check + lint 通过

### Commit

```
feat(sop): ToolbarActions + EmptyStateCard + ScrollFollowButton 组件

NDF: sop-runtime-vue-rewrite task 18
```

---

## Task 19: HistoryModal.vue 组件

**依赖：** Task 5, 13
**仓库：** numind-web-v3
**文件：**
- 新建：`src/views/sop/components/HistoryModal.vue`

**约工作量：** 0.7 天

### Steps

- [ ] **Step 1：实现 HistoryModal**

参考 spec §7.1。功能：
- 加载执行历史 GET /v1/sop/templates/executed
- 过滤 pending / failed
- 按 created_at 倒序
- 显示进度（completed / total）
- 点击切换 run（router.push）
- 删除按钮 → ConfirmModal 确认 → DELETE API → 重新加载

### DoD

- [ ] 组件完成
- [ ] type-check + lint 通过
- [ ] 简单单测：mock API，验证渲染

### Commit

```
feat(sop): HistoryModal.vue 历史记录弹窗

NDF: sop-runtime-vue-rewrite task 19
```

---

## Task 20: TrailingChatPanel + ChatBubble（含模型选择 wire）

**依赖：** Task 5, 6, 17
**仓库：** numind-web-v3
**文件：**
- 新建：`src/views/sop/components/TrailingChatPanel.vue`
- 新建：`src/views/sop/components/ChatBubble.vue`

**约工作量：** 1 天

### Steps

- [ ] **Step 1：ChatBubble**

显示单条消息（user / assistant），含：
- avatar + 名称
- 内容（复用 StepOutput 的 thinking + content 渲染）
- 操作按钮（复制 / 重新生成 仅 assistant）

- [ ] **Step 2：TrailingChatPanel**

参考 spec §8.2（**修订后版本**）。关键：
- 加载历史消息 GET /v1/sop/runs/:id/chat-messages
- 发送消息：JSON body POST /v1/sop/chat/stream（**不是 FormData**）
- payload：`{ run_id, conversation_id, question, deep_thinking, regenerate_msg_id }`
- conversation_id 从 task 1 调研结论的机制提取（done meta 或其他事件）
- 重新生成：删除当前 + 上一条消息，重新发送

### DoD

- [ ] 两个组件完成
- [ ] type-check + lint 通过
- [ ] 模型选择 + 深度思考开关从 modelStore 正确读取并传递

### Commit

```
feat(sop): TrailingChatPanel + ChatBubble 末尾聊天组件

ChatStream 调用使用 JSON body（不是 FormData），含模型选择 wire。

NDF: sop-runtime-vue-rewrite task 20
```

---

## Task 21: SOPRunView.vue 主组件集成 + 路由

**依赖：** Task 1-20 全部完成
**仓库：** numind-web-v3
**文件：**
- 新建：`src/views/sop/SOPRunView.vue`
- 修改：`src/router/index.ts`

**约工作量：** 0.7 天

### Steps

- [ ] **Step 1：实现 SOPRunView**

参考 spec §9.2。组装所有子组件：
```
<TopBar>
  <BackHomeButton>
  <TemplateTitle>
  <HistoryButton>
<StepperPanel>
<main>
  <StepContent v-if="!isOnTrailingChatStep">
    <StepInput />
    <StepOutput />
    <ToolbarActions />
  <TrailingChatPanel v-else />
<HistoryModal v-model="showHistory">
<ScrollFollowButton>
```

onMounted 流程：
1. loadTemplate(templateId)
2. loadRun(runId) 或 enterDraftMode(templateId)
3. restoreFromSession()

onBeforeUnmount：cleanup draft + reset store

- [ ] **Step 2：路由配置**

修改 `src/router/index.ts`：
```typescript
{
  path: '/sop',
  name: 'SOPRun',
  component: () => import('@/views/sop/SOPRunView.vue'),
  meta: { requiresAuth: true },
}
```

- 把原来指向 SOPView.vue 的路由改为指向 sop/SOPRunView.vue
- 注意：路径 `/sop` 保持不变

### DoD

- [ ] 主组件完成
- [ ] 路由更新
- [ ] type-check + lint 通过
- [ ] 本地启动（npm run dev）能加载页面（即使数据为空也不报错）

### Commit

```
feat(sop): SOPRunView.vue 主组件 + 路由更新

集成所有子组件 + composables，挂在原路由 /sop。
保持 URL 契约不变（templateId + runId query）。

NDF: sop-runtime-vue-rewrite task 21
```

---

## Task 22: 删除 legacy 文件

**依赖：** Task 21
**仓库：** numind-web-v3
**文件：**
- 删除：`public/legacy/sop-legacy.js`
- 删除：`public/legacy/sop-legacy.css`
- 删除：`public/legacy/` 整个目录（如除上述无其他文件）
- 删除：`src/views/SOPView.vue`（旧 wrapper）
- 删除：`src/stores/sop.ts`（legacy hydration 胶水）
- 修改：`src/stores/index.ts`（**实测有 `export { useSopStore } from './sop'` at line 3，必须移除**）
- 修改：`src/main.ts` / 其他可能 import `useSopStore` 的地方
- 修改：`numind-web-v3/CLAUDE.md`（如果列出了 sop-legacy.js）

**约工作量：** 0.2 天

### Steps

- [ ] **Step 1：扫描 import**

```bash
cd numind-web-v3
grep -rn "useSopStore\|stores/sop\|sop-legacy\|SOPView" src/ --include="*.ts" --include="*.vue"
```

列出所有引用 → 删除或迁移到 sopRun store。

- [ ] **Step 2：删除文件**

```bash
rm public/legacy/sop-legacy.js
rm public/legacy/sop-legacy.css
rm src/views/SOPView.vue
rm src/stores/sop.ts
```

如 public/legacy/ 还有其他 vendor 文件（github-dark-dimmed.css、font-awesome）：评估是否还有别处用，如无则一并删。

- [ ] **Step 3：grep 验证清理彻底**

```bash
grep -rn "TEMPLATE_CONFIGS\|STEP_NAME_MAP\|sop-legacy\|__sopLegacyInit" src/ public/ --include="*.ts" --include="*.vue" --include="*.js"
# 期望：0 results
```

### DoD

- [ ] 4 个文件已删除
- [ ] 0 个孤立 import
- [ ] grep 验证 0 命中硬编码常量
- [ ] type-check + lint 通过
- [ ] npm run dev 启动正常

### Commit

```
chore(sop): 删除 legacy SOP 运行页文件

退役：
- public/legacy/sop-legacy.js (7518 行)
- public/legacy/sop-legacy.css
- src/views/SOPView.vue (1019 行 hydration wrapper)
- src/stores/sop.ts (273 行 legacy 胶水)

至此 SOP 运行页 100% Vue 3 化。

NDF: sop-runtime-vue-rewrite task 22
```

---

## Task 23: Playwright E2E 测试（S5 验证策略 task）

**依赖：** Task 22
**仓库：** numind-web-v3
**文件：**
- 新建：`e2e/sop-runtime.spec.ts`

**约工作量：** 1.5 天

> ⚠️ **这是 S5 验证策略 task**。Playwright E2E 而非 gstack /qa 一次性验证。NDF Rule 10 要求。

### 验收路径清单（11 个）

参考 spec §12.2，加 P2 补充的模型切换路径：

- [ ] **Path 1：trial 账号走完 templateId=1 的 4 步**
  - assert 步骤名称 = DB.sop_node.name（流量选题口播稿 / AI拆解产品 等）
  - assert 配额从 X 减为 X-1
  - assert URL 从 ?templateId=1 → ?templateId=1&runId=Y

- [ ] **Path 2：templateId=3+ 步骤描述为空时优雅退化**
  - assert 描述行不渲染（DOM 中不存在 .step-description 元素或 textContent 为空）
  - assert 不显示 "undefined" / "null" / "[object Object]"

- [ ] **Path 3：trial 配额耗尽弹 InsufficientCreditsDialog**
  - mock 后端返回积分不足错误
  - assert dialog 显示

- [ ] **Path 4：standard 账号 templateId=2 走完 + trailing chat 多轮**
  - 第 1-4 步完成 → 进入第 5 步聊天
  - 发送 3 轮消息 → 验证 conversation_id 持续
  - 重新生成最后一条 → 验证消息列表正确

- [ ] **Path 5：上传 PDF 触发 OCR**
  - 上传一个测试 PDF（fixtures/test.pdf）
  - assert 输入框最终包含 PDF 提取的文本
  - assert 用户手输内容 + PDF 文本同时存在

- [ ] **Path 6：节点 SSE 流式输出 + 思维链显示**
  - mock SSE response 返回 thinking + message 事件
  - assert StepOutput 增量渲染
  - assert 思维链可折叠

- [ ] **Path 7：流式输出过程中刷新页面 → 步骤恢复**
  - 在第 3 步执行中刷新
  - assert 重新进入后停留在第 3 步（sessionStorage）

- [ ] **Path 8：历史记录弹窗 CRUD**
  - 打开 → 列表加载
  - 点击删除 → ConfirmModal 弹出
  - 确认 → API 调用 → 列表更新
  - 切换到不同 run → 路由跳转

- [ ] **Path 9：Draft 模式关闭浏览器**
  - 进入新 SOP 但不执行节点（用 Playwright `page.goto('/sop?templateId=3')` 后只做 input 不点执行）
  - 拿到测试用户 ID（从 token 解析或测试 fixtures）
  - 触发 onBeforeUnmount（`page.close()` 或 `page.goto('/')`）
  - assert sendBeacon 被调用（Playwright `page.evaluate` 拦截 navigator.sendBeacon）
  - **SQL 验证（dev 环境）**：
    ```bash
    sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no "$DEV_SSH_USER@$DEV_SSH_HOST" \
      "docker exec numind-mysql-dev sh -c 'mysql -uroot -p\$MYSQL_ROOT_PASSWORD numind-dev -e \
       \"SELECT COUNT(*) AS draft_count FROM sop_run WHERE status=\\\"draft\\\" AND user_id=<TEST_USER_ID>;\"' \
       2>&1 | grep -v 'Using a password'"
    # 期望：draft_count = 0
    ```
  - 测试需要 setUp / tearDown 清理：测试前 `DELETE FROM sop_run WHERE user_id=<TEST_USER_ID> AND status='draft'`

- [ ] **Path 10：API 安全验证**
  ```bash
  TOKEN=<dev token>
  RESP=$(curl -s -H "Authorization: Bearer $TOKEN" \
    http://49.233.219.254:9091/v1/sop/templates/1/nodes)
  echo "$RESP" | jq '.data.nodes[0]' | grep -Ec '(api_key|base_url|model_name|timeout_seconds|prompt)'
  # 期望：0
  ```
  作为 e2e 的一个 setup-time check，或者作为独立的 bash test 在 CI 跑

- [ ] **Path 11：模型切换 + 深度思考开关**
  - 在 ModelSelector 选择不同模型
  - 触发节点执行
  - 拦截 fetch 请求 → assert URL 含正确的 ?model_key=X&thinking=1
  - trailing chat 同样验证 deep_thinking JSON 字段

### DoD

- [ ] 11 个路径全部 pass
- [ ] `npm run test:e2e` 通过
- [ ] CI 配置允许 E2E 在 PR 上跑（如未配置则记入 deferred）

### Commit

```
test(sop): Playwright E2E 11 个关键路径

S5 验证策略 task：
- 数据契约验证（步骤名 = DB.name，配额扣减）
- 安全验证（GetTemplateNodes 不泄露字段）
- 功能等价（流式 / 思维链 / 文件上传 / 历史 / trailing chat / 模型选择）
- 边界情况（描述为空 / 配额耗尽 / 刷新恢复 / Draft 清理）

NDF: sop-runtime-vue-rewrite task 23 (S5 验证策略)
```

---

## Task 24: 全链路冒烟 + lint/typecheck/test 全 pass

**依赖：** Task 23
**仓库：** 双仓库
**文件：** 无新增
**约工作量：** 0.5 天

### Steps

- [ ] **Step 1：后端**

```bash
cd numind-server
task lint
go test ./...
task test  # 完整版含 race + coverage
```

- [ ] **Step 2：前端**

```bash
cd numind-web-v3
npm run lint
npm run type-check
npm run test:e2e
npm run build
```

- [ ] **Step 3：本地端到端**

启动 numind-server local + numind-web-v3 dev，用真实 trial 账号走完 templateId=1 / templateId=3+ 各一次。

- [ ] **Step 4：填写 last_verified**

在 manifest 中更新 last_verified 字段。

### DoD

- [ ] 后端 lint + test pass
- [ ] 前端 lint + type-check + e2e + build pass
- [ ] 本地手动冒烟通过
- [ ] manifest last_verified 更新

### Commit

```
chore(sop): S4 完成，全链路验证 pass

NDF: sop-runtime-vue-rewrite task 24
```

---

## Task 25: 部署 dev + curl 安全验证 + 多账号冒烟

**依赖：** Task 24
**仓库：** 双仓库
**文件：** 无
**约工作量：** 0.3 天

### Steps

- [ ] **Step 1：合并到 develop**

参考 spec §11.1 部署顺序：
1. **后端先行**：用 `/commit-merge-push` 合并 numind-server/feature/sop-runtime-vue-rewrite → develop
2. 等待 dev 自动部署完成（约 2-5 分钟）
3. curl 验证后端：
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" \
     http://49.233.219.254:9091/v1/sop/templates/1/nodes \
     | jq '.data | keys'
   # 期望：["nodes", "template", "total"]
   
   curl -s -H "Authorization: Bearer $TOKEN" \
     http://49.233.219.254:9091/v1/sop/templates/1/nodes \
     | jq '.data.nodes[0]' | grep -Ec '(api_key|base_url|model_name|timeout_seconds|prompt)'
   # 期望：0
   ```

- [ ] **Step 2：前端紧随**

合并 numind-web-v3/feature/sop-runtime-vue-rewrite → develop，等待部署。

- [ ] **Step 3：dev 环境冒烟**

用 `$DEV_SITE_URL` 浏览器打开：
- trial 账号：进 templateId=1 走完 4 步 + trailing chat
- standard 账号：进 templateId=3+ 走完所有步
- DevTools Network 面板复查：response 中 0 个 api_key / prompt
- 侧边栏：0 个绿色卡片

- [ ] **Step 4：更新 manifest**

- 设置 stage = "S6"（已部署 dev）或 "completed"（如果 prod 也跟进）
- 填写 completed_at（或留空等 prod）

### DoD

- [ ] 后端合并 + 部署成功
- [ ] 前端合并 + 部署成功
- [ ] curl 安全验证 0 字段命中
- [ ] dev 多账号冒烟通过
- [ ] manifest 更新

### Commit / Merge

```
（后端 merge commit）
Merge feature/sop-runtime-vue-rewrite into develop

NDF: sop-runtime-vue-rewrite task 25
```

```
（前端 merge commit）
Merge feature/sop-runtime-vue-rewrite into develop

NDF: sop-runtime-vue-rewrite task 25
```

---

## Rollback Runbook（紧急情况）

参考 spec §11.2。

```bash
# 前端紧急回退
cd numind-web-v3
git revert <vue-rewrite-merge-commit-hash>
git push develop

# 后端紧急回退（如需）
cd numind-server
git revert <backend-merge-commit-hash>
git push develop
```

**前置条件**：所有 task 25 的 merge 必须是 `--no-ff` 单一 merge commit（`/commit-merge-push` 命令默认行为，已在 spec §11 / commit-merge-push.md 验证）。

---

## 完成定义（整体）

- [ ] 25 个 task 全部 commit + 双 review pass
- [ ] manifest progress: total_tasks = 25, completed_tasks = 25, reviewed_tasks = 25
- [ ] 后端 + 前端 dev 环境部署成功
- [ ] curl 安全验证通过（5 字段全部隐藏）
- [ ] 多账号冒烟通过
- [ ] Playwright E2E 11 路径全部 pass
- [ ] manifest stage 更新为 "S6" 或 "completed"

## 后续（不在本 plan 范围）

- prod 部署（独立 NDF S7 阶段，按现有流程 cherry-pick 到 release / tag v*）
- single-session-enforcement 功能（独立 Standard 功能，作为后续项目）
- templateId=1, 2 的 description SQL backfill（用户自助处理）
