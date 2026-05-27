# Agent ReAct 流式化 — S5 验证策略

> **Feature**: `agent-react-streaming` (Standard track)
> **Date**: 2026-05-27
> **Decision**: Locked at S2-D3 (spec §8). This document formalizes the strategy as the NDF Rule 10 S5 task artifact reviewable by the S3 gate reviewer.

---

## §1 选定验证方式

**Playwright E2E + 后端 Go test 双覆盖**

gstack `/qa` 不作为本 feature 的主验证手段，详见 §4。

---

## §2 Playwright E2E 场景（5 个）

文件: `numind-web-v3/e2e/agent-streaming.spec.ts`

| # | 场景名 | 关键断言 |
|---|--------|---------|
| **E1** | 普通流（happy path） | 提交问题 → 第一个 token ≤2s 内渲染到界面 → tool_call_start 卡片出现 → tool_call_result 更新状态徽章 → terminal 收到后显示 final_answer |
| **E2** | 用户中止 | 流式进行中点击"中止"按钮 → SSE 连接立即关闭 → 后端 agent_run.state_reason = `aborted_streaming` → 输入框恢复可用 |
| **E3** | 多标签降级轮询 | 开第二个浏览器标签访问同一 run → 后端返回 409 → 前端自动 fallback 至 GET 轮询 → 最终结果与第一个标签一致 |
| **E4** | 断流恢复 | 通过 `page.route` 拦截并中断 SSE fetch → 重试 1 次 → 再失败转 5s 轮询 → 最终显示正确的 terminal state |
| **E5** | question_prompt 流式交互 | 流式触发 ask_user_question → 界面弹出选项 → 用户选择后 SSE 继续 → 流正常完成 |

---

## §3 后端 Go test 类别（6 类）

覆盖散布在 T01–T07 的 `*_test.go` 文件中：

| # | 类别 | 文件 | 关键场景 |
|---|------|------|---------|
| **G1** | 事件协议序列化 round-trip | `stream/events_test.go` | 14 种 EventType × JSON marshal/unmarshal；字段名 regression |
| **G2** | consumeEinoStream 消息处理 | `runner_stream_test.go` | 纯文本流 / 含工具调用 / 多 step / reasoning content / stream error |
| **G3** | SubscriptionLock 并发 | `stream/lock_test.go` | 1000 goroutine 争同一 runID，只有 1 个赢；Release 幂等 |
| **G4** | Hook 链在流式路径下触发顺序 | `runner_stream_hookchain_test.go` | 流式路径下 hook 顺序与非流式 Run() 一致；HookActionStop 正确截断 |
| **G5** | ctx cancel 不泄露 goroutine | `runner_stream_test.go`（TestRunStream_AbortedByCtx） | 中途 ctx cancel → emit terminal(aborted_streaming) → goleak 验证无 goroutine 泄露 |
| **G6** | terminal_metadata 在流式失败时写入 | `terminal_metadata_consistency_test.go` | stream 遇 model_error → terminal_metadata 字段与非流式路径写入一致（Hotfix model-error-recovery 的延伸覆盖） |

---

## §4 为何不用 gstack /qa 作为主验证

1. **时序敏感**：流式 SSE 涉及逐帧渲染时序（token delta 间隔、断流重试时机、abort 响应延迟）。gstack `/qa` 是单帧截图，无法验证帧序列或精确的 timing 断言。

2. **无回归保护**：gstack `/qa` 是一次性手动验证，不产生持久化测试代码。agent-mode 是高风险核心功能（多步骤 LLM + 工具），未来修改时必须有自动回归保护。Playwright E2E 测试代码永久留在 `e2e/` 目录中。

3. **边界场景覆盖**：断流恢复（E4）、多标签 409 降级（E3）、question_prompt 流式交互（E5）需要精确控制网络拦截和多标签状态，Playwright 的 `page.route` 和多上下文 API 是专为此设计的。

4. **高风险 business 逻辑**：per `.claude/rules/testing.md` §3，涉及支付、权限、会员等高风险逻辑的功能应使用 Playwright E2E（本 feature 涉及用户积分计费路径 + 流式路径下的 agent 执行）。

---

## §5 Langfuse 人工验证（dev 部署后）

不计入自动化，属于 S6 dev 验收的可选步骤：

- [ ] dev 触发一次 agent run，在 Langfuse UI 确认 trace 中出现 `sse.connection` span
- [ ] `sse.connection` span 的 output 字段包含 `first_byte_ms`、`event_count`、`disconnect_reason`
- [ ] 流式路径下的 generation 数量与非流式 Run() 路径一致（同样的 per-step generation 粒度）

---

## §6 S5 执行入口命令

```bash
# 后端 Go tests（含 race detector）
go test -race -timeout 60s ./internal/numind/biz/agent/...

# Playwright E2E
cd numind-web-v3
E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD \
  npm run test:e2e -- e2e/agent-streaming.spec.ts
```
