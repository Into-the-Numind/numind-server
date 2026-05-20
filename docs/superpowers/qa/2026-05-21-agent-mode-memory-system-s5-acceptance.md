# NDF S5 Acceptance Record · `agent-mode-memory-system`

**Feature ID**：`agent-mode-memory-system`（14-feature 分解 #7/14）
**Acceptance 日期**：2026-05-21
**前置 stage**：S0 / S1 / S2 / S3 / S4 全部通过
**当前 stage 入口 commit**：`e4198472`（lint clean）

---

## 1. 概览

S4 全部 11 个 M task 完成 + 全部 P0/P1 reviewer 通过 → S5 验收成立。

| Task | 实现 commit | 修复 commit | 状态 |
|------|------------|-------------|------|
| M1 migration SQL | `d6d30a67` | `6af198d3` (header + ampersand) | ✓ |
| M4 biz/memory types + fence + errno | `e80b3735` | `6af198d3` (en comment) | ✓ |
| M2 GORM model + Create boundary | `46e684ce` | `e8e3a9a4` (gotcha assert + tag quotes) | ✓ |
| M3 store impl + IStore wire | `dec326da` | `3fb8dead` (OrderBy whitelist + concurrent assert) | ✓ |
| M5 Notepad | `6caae059` (含 M6) | `60830a31` (dead code + store conf=0 test) | ✓ |
| M6 Retriever + mockEmbedder | `6caae059` | — | ✓ |
| M7 Provider (composite) | `165c70ee` | `c5191161` (Clear partial fail + Prefetch zero) | ✓ |
| M8 memory_write + memory_read tools + ctx_keys | `c7beb306` | — | ✓ |
| M9 factory_platform registration | `a348b485` | — | ✓ |
| M10 runner.go + biz wire + AutoMigrate | `cf42542b` | `0198a4a9` (注释更新) | ✓ |
| M11 (S5 验证策略) | 本文档 | — | ✓ |
| S5 prep | — | `e4198472` (lint clean) | ✓ |

---

## 2. PI 验证矩阵（S3 plan §3 M11 定义）

| PI | 描述 | 验证 test | 结果 |
|----|------|----------|------|
| **PI-1** | `EnableMemory=true && provider != nil` → SystemPrompt 含 `<memory-context>` | `TestRunner_EnableMemoryTrue_WithProvider_HasMemoryBlock` (runner_memory_test.go) | PASS |
| **PI-2** | memory_write 写入后 SystemPromptBlock 含此条 | `TestProvider_SystemPromptBlock_AfterWrite` (provider_test.go) | PASS |
| **PI-3** | 跨 user 隔离 | `TestNotepad_CrossUserIsolation` + `TestMemoryReadTool_CrossUserIsolation` | PASS |
| **PI-4** | fence 防注入 | `TestEscapeForStorage_HTMLEntities` + `TestFenceInjection_ScriptTag` + `TestFenceInjection_ClosingTag` + `TestFenceInjection_Ampersand` + `TestMemoryReadTool_UnescapeValue` | PASS |
| **PI-5** | Notepad 并发 upsert | `TestStore_UserGlobalMemory_Upsert_Concurrent100` (≥50/100 必须 PASS with WAL+busy_timeout) | PASS |

---

## 3. 覆盖率（plan §3 验收门槛）

```
biz/memory     : 94.8%  (target ≥80%)  ✓ 大幅超额
biz/agent      : 84.2%  (target 不降级 ≥80%)  ✓ 反而提升
store          : 24.3%  (注：含整个 numind/store 包；新增 store 自测约 75%+)
model          : 10.4%  (注：含整个 model 包；新增 model 仅 boundary test)
```

**biz/memory 子包内分文件覆盖率**：
- `types.go` + `fence.go` + `errno.go`: 100%
- `notepad.go`: 90.5%
- `retrieval.go`: 95.0% (RetrieveL1) + 87.5% (RetrieveL2)
- `provider.go`: 93.3%

---

## 4. Race Detector 验证

```bash
go test -race ./internal/numind/biz/memory/... \
              ./internal/numind/biz/agent/... \
              ./internal/numind/store/... \
              ./internal/pkg/model/... \
              ./internal/pkg/middleware/...
```

**结果**：全部 PASS，无 data race 告警。

关键并发场景：
- `TestStore_UserGlobalMemory_Upsert_Concurrent100`: 100 goroutine 同 (user, key) → 最终行数 1，PASS
- `TestRunner_*`: 所有 runner_memory_test 在 race detector 下 PASS

---

## 5. 整包编译 + vet

```bash
go build ./...
```
**结果**：PASS（仅 macOS CGo sqlite-vec deprecated API warning，与本 feature 无关）

```bash
go vet ./...
```
**结果**：exit 0（仅同上 macOS CGo warning）

```bash
task lint
```
**结果**：#7 范围 zero warning（commit `e4198472` 修复了 retrieval.go 未使用字段 + retrieval_test.go SA9004）

**剩余 lint warning（与 #7 无关，沿用历史代码）**：
- `cmd/agent-phase0-eino-demo/adapter.go:30` SA1019 ChatModel deprecated
- `internal/numind/biz/sandbox/pool.go:236` SA9003 empty branch（#4 sandbox 历史）
- `internal/numind/biz/agent/adapter_test.go:16` S1040 type assertion same type（#2 runtime-skeleton 历史）

不在本 feature 范围 — 由历史 feature owner 处理。

---

## 6. 0 prod 影响声明

| 检查项 | 结果 |
|--------|------|
| `config_prod.yaml` zero diff | ✓ `git diff develop..HEAD -- config_prod.yaml` 返回 0 行 |
| 不打 git tag (`v*` / `admin-v*`) | ✓ 未执行 |
| 不调 `/deploy-prod` | ✓ 未执行 |
| feature 分支不推 GitHub | ✓ pre-push hook 主动拦截，本 session 未尝试 push |
| `credit_transaction.source_type` CHECK constraint 零修改 | ✓ 本 feature 不引入新 source_type 枚举值 |
| 不动 prod SSH（`PROD_SSH_*`） | ✓ 未执行 |
| 不修改任何 cmd/numind* 启动入口 | ✓ 未触及 |

---

## 7. Reviewer 累计统计

| 阶段 | reviewer 次数 | P0 | P1 | P2 |
|------|--------------|----|----|-----|
| S0 | 1 | 2 | 0 | 0 |
| S1 | 1 | 0 | 3 | 6 |
| S2 | 1 | 2 | 3 | 6 |
| S3 | 1 | 2 | 3 | 6 |
| S4 Wave 1 (M1+M4) | 1 | 0 | 0 | 3 |
| S4 M2 | 1 | 0 | 1 | 5 |
| S4 M3 | 1 | 0 | 2 | 4 |
| S4 Wave 3b (M5+M6) | 1 | 0 | 2 | 4 |
| S4 M7 | 1 | 0 | 0 | 3 |
| S4 M8 | 1 | 0 | 0 | 3 |
| S4 Wave 6 (M9+M10) | 1 | 0 | 0 | 3 |
| **累计** | **11** | **6** | **14** | **43** |

**全部 P0 + P1 已修复并 commit；P2 大部分已修，少数明确推迟到后续 feature（如 #10 admin UI / #11 学员端 UI / #14 真实 SyncTurn）。**

---

## 8. 验证策略对照（S3 M11 决议）

- **方式**：纯后端 TDD + go test -race（不走 Playwright E2E / gstack /qa）
- **规则 10 适用性**：本 feature 不涉及"支付、权限、会员等级"高风险类别 — 学员隐私走跨 user 隔离单测（PI-3），防注入走 fence 单测（PI-4）— **不强制 E2E**
- **回归保护**：5 个 PI test 持久化在代码库 + race detector + GORM gotcha boundary test 永久保留
- **诚实声明**：本 feature **没有持久化 UI E2E 回归保护**；未来 #11 学员端落地时需自写学员侧 E2E

---

## 9. Out of scope 兑现

S0 / S1 明确推迟的 11 项（参 S1 §5 反例表），全部未实装、未泄漏：

- Qdrant 向量集成 — v1 仅占位接口（v2）
- 真实 aiservice.Embed 调用 — v1 mockEmbedder 1024 维零向量
- L1 90 天 TTL cron 清理 — 写 expires_at 但无 cron（#14 / 运维）
- L1 行数硬上限 GC — spec 定义不实装（#14 SyncTurn 引用）
- SyncTurn 真实写入 — v1 stub return nil（#14）
- OnPreCompress 真实写入 — v1 stub return nil（#9）
- 跨学员脱敏 memory 共享 — 永久不实装（蓝本 §4.5.1 三层隔离）
- 学员/管理端 UI — 完整推到 #10 / #11
- LLM rerank — v1 BM25 + recency boost 占位（v2）
- 23 条 admin memory CRUD API — #10
- MMR 真实计算 — v1 跳过（v2）

---

## 10. 接入下一阶段（#9 / #11 / #14）

本 feature 落地的接入点（spec §10 协调表）：

- **#9 (compact-system)**：`compositeProvider.OnPreCompress` 已 stub return nil；#9 替换实现体即可，调用方无需特殊处理
- **#11 (student-ux backend handoff)**：`MemoryProvider.Clear(ctx, userID)` 已暴露 biz；#11 自加 HTTP controller + router（POST `/v1/agent/memory/clear-all`）+ `GET /v1/agent/memory/list` 等
- **#14 (real ReAct loop)**：
  - 真实 SyncTurn 接入时调 `provider.SyncTurn(ctx, userID, agentDefID, sessionID, userMsg, assistantMsg)`
  - 通过 ctx `middleware.AgentDefinitionIDFromCtx` 取 agentDefID（已在 runner.go Step 4 注入）
  - 若需要 sessionID 入 ctx，#14 自行加 `CtxKeySessionID`（本 feature 不预先添加 — P2-3 决议）
- **#6 (permission-pipeline)**：runner.go 中 `tenantHardRulesPlaceholder` 占位变量已就位；#6 直接替换初始化表达式
- **#14 (tools_section)**：runner.go 中 `toolsSectionPlaceholder` 占位变量已就位

---

## 11. 关键设计决策回顾

主要决策 commit log（每条对应一个 P0/P1 修复后保留的设计选择）：

- **S0 P0-1 / S1 §3.9**：L1 schema 偏离蓝本 — 采用 `kind+content` append-only 而非 `key+value` upsert（理由：蓝本 §4.5.2 Hybrid 检索要求 content 全文字段；L2 Notepad 保留蓝本 key-value 语义）
- **S0 P0-2 / S1 §3.5**：fence tag 写入时转义（与蓝本 §4.5.4 对齐；避免双重转义）
- **S1 P1-2 / S2 P1-3**：PlatformBase disclaimer 落地选方案 B — runner.go 局部变量 `memoryDisclaimerBlock`，仅 memory 启用时注入；纯文本格式（非 HTML 注释）
- **S2 P0-1**：FullTool 36 方法 + BaseTool 嵌入 + 5 重写
- **S2 P1-2 / S3 P1-2**：IStore 复数命名 `AgentSessionMemories()` / `UserGlobalMemories()`
- **S2 P2-2 / Wave 3b**：Notepad confidence=0.0 是合法低置信度 — 不强制覆盖；Upsert UpdateColumn fixup 仅在 wantConfidence=0 && GORM 覆盖时触发
- **S3 P2-3**：删 `CtxKeySessionID`（过度设计 — sessionID 通过参数传递）
- **M3 P1-1**：`ListOpts.OrderBy` 加白名单防 SQL injection

---

## 12. S6 准入

S5 PASS — 进入 S6 ndf-done。

**S6 注意事项**：
- 并行 #6 / #8 / #9 session 也在跑；S6 merge 可能与 `runner.go` / `biz.go` / `helper.go` 冲突
- 各 feature 段位归属互不交叉（按 S2 §10 协调表 + Functional Option 模式），逐文件手工 resolve 即可
- 走手动 merge 路径（`ndf-done` 经常失败 — 沿用 #5 经验）
- 推 develop 前 `git fetch && git diff develop..HEAD -- internal/numind/biz/agent/runner.go internal/numind/biz/biz.go internal/numind/helper.go` 预览冲突

---

**S5 ACCEPTED — 进入 S6。**
