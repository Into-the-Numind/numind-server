# 需求卡片：SOP 流式执行健壮性批次（sop-stream-robustness）

- **Feature ID**: sop-stream-robustness
- **录入日期**: 2026-06-05
- **Track**: Standard（S0→S7）
- **状态**: S0 录入

## 1. 背景

SOP（多步 AI 工作流）的流式执行链路存在一组健壮性问题，集中在同一批文件
（`biz/sop/sop.go`、`biz/sop/executor.go`、`controller/v1/sop/sop.go`、
`store/sop.go`、`pkg/aiservice/adapter/stream.go`、`biz/errtranslate`）。
打包在一个 feature 里修复。

执行链路有两条流式路径：
- **生产主路（Gateway）**：`modelKey != ""` → `executor.executeViaGateway` →
  `aiservice.ChatStream` → adapter `runOAIStream`（`bufio.Scanner`）。
  `ResolveUserModel` 几乎总返回非空 modelKey，线上走这条。
- **兜底路**：`modelKey == ""`（模型解析失败）→ `ExecuteNodeStreamWithThinking`
  → `callAliDeepThinkingStream` / `callVolcDeepThinkingStream`（裸 HTTP +
  `bufio.Reader`）。平时不走，但本批次两条路都要覆盖。

## 2. 要修的 6 个问题

| # | 优先级 | 问题 | 修复目标 |
|---|--------|------|---------|
| 1 | P1 | 网络瞬断 → 节点判失败退积分 | LLM 生成不随请求连接取消而中断；瞬断后后台照常生成+落库，重连可拉到结果，前端无错误弹窗 |
| 2 | P1 | AI 卡死无限转圈（无 idle 超时） | idle 超时=连续 4 分钟无新字节 → 判超时掐掉；整体超时=可配置默认 30 分钟兜底；两条路都覆盖；错误友好化 |
| 3 | P2 | 3 个 SSE 接口各抄心跳样板 + 冗余双 ctx 检查 | 抽公共心跳 helper（mutex+ticker+ctx），三处复用，去冗余；心跳逻辑本身保留 |
| 4 | P2 | 兜底路发两个 done 事件 | 统一只发一次 done（控制器在 biz 正常返回后发，executor 不再透传 done）；ChatAfterRunStream 的 message_id done 不破坏 |
| 5 | P2 | 再生清理非事务（3 次删各自失败只 Warnw） | store 层加事务方法把 3 个删除包进一个 DB 事务（全成/全不动）|
| 6 | P2 | 状态更新失败被吞 + 内存先置 Running 跳过二次兜底 | draft→running 更新带退避重试；重试都失败对用户静默、自愈（zombie-reset 可扫）；内存与 DB 最终一致 |

## 3. Triage 判定

**步骤 1（Micro 边界）**：改 `biz/`、`store/`、config、跨多文件 → 不是 Micro。

**步骤 2（Hotfix vs Standard，5 条）**：
1. DB schema 变更：否（仅新增 store 事务方法，无 schema）
2. 新增 API 端点：否（复用 `GET /sop/runs/:id/status` 做重连查询）
3. 新外部服务：否
4. 影响文件数 ≤3：**否**（≥6 文件）
5. 支付/权限等高风险：**是**（问题 1/2 直接相邻 Reserve/Reconcile 退积分逻辑）

→ 第 4、5 条失败 → **Standard，不可降级**。

## 4. 验收标准

- 模拟客户端中途断开：节点不被判失败、后台生成照常落库、重连能看到结果、前端无错误弹窗。
- 模拟 provider 卡住不吐字：4 分钟后被 idle 超时干净中止并返回友好错误，不再无限转圈。
- 整体超时可配置、默认 30 分钟、不误杀正常长流。
- done 事件只发一次；再生清理原子化；状态更新失败能重试成功且用户无感。
- `go test ./...` 全绿，`task lint` 通过。

## 5. 范围外

- 不动 `config_prod.yaml`（只加 local/dev/qa + 代码兜底默认）。
- 不改前端（纯后端健壮性；重连查询复用既有接口）。
- 与并行 session（`biz/contextbudget/*` + `sop_fragments.go` context 排序）文件基本不重叠；
  若 `ndf-done` merge `sop.go` 冲突，正常解决。
