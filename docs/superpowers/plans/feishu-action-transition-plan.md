# 飞书无业务确认与生命周期兼容 — 实施计划

日期：2026-07-20  
设计：`docs/superpowers/specs/2026-07-20-feishu-action-transition-design.md`  
执行方式：快速 Standard，串行 RED → GREEN；只跑定向回归和仓库强制检查。

## 依赖图

```text
T1 RED ─→ T2 backend GREEN ─→ T3 frontend GREEN ─→ T4 gates/review ─→ T5 merge/deploy
```

跨仓库改动共享 operation lifecycle 契约，按后端优先串行执行；不做并行写入。

## T1 — 客户故障 RED 回归

### 描述
在两个 feature worktree 中先提交失败测试，永久复现：

1. 新高风险飞书操作错误地返回 confirmation，而不是直接执行并由服务器追加 `--yes`。
2. 历史 `waiting_confirmation` 的 `Resume` 仍停留等待或旧 `user_completed` 返回 InvalidParameter。
3. 前端 legacy confirmation 仍展示业务确认按钮，且没有自动兼容继续。

### 涉及文件
- `numind-server/internal/numind/biz/feishu/operation_service_test.go`
- `numind-server/internal/numind/biz/feishu/service_test.go`
- `numind-web-v3/src/components/agent/__tests__/FeishuActionCard.spec.ts`
- 必要时 `numind-web-v3/src/stores/__tests__/agentChat-resume.spec.ts`

### 验收条件
- 定向 Go 测试在修复前失败，失败点指向 confirmation 状态/未执行 runner/InvalidParameter。
- 定向 Vitest 在修复前失败，失败点指向仍存在的确认按钮或未触发兼容恢复。
- 两个仓库的第一个 feature commit 均为 `test(qa): reproduce ...`。

## T2 — 后端删除主动确认并兼容历史状态

### 描述
- 新 operation 不再创建 confirmation action 或转换到 `waiting_confirmation`。
- 所有连接态外部命令进入 execution gate。
- `RequiresCLIYes` 由服务器执行时自动追加。
- 历史 `waiting_confirmation` 由 `Resume` 直接执行；`Confirm` 成为兼容别名。
- Workspace lifecycle 收到旧 `user_completed` 时按当前服务端状态恢复。

### 涉及文件
- `numind-server/internal/numind/biz/feishu/operation_service.go`
- `numind-server/internal/numind/biz/feishu/service.go`
- 对应测试文件

### 验收条件
- T1 后端 RED 全部转绿。
- 新操作返回执行/授权/终态之一，绝不返回新的 confirmation。
- 历史状态重复恢复最多调用 runner 一次。
- server-controlled `--yes` 测试通过。
- API 契约保持 `POST /v1/feishu/operations/:id/resume`，无 router/controller 变更。

## T3 — 前端移除业务确认交互

### 描述
- 删除 confirmation 的确认/取消按钮和引导文案。
- 收到 legacy confirmation 时只显示非交互的继续状态，并通过现有 store in-flight 去重触发一次兼容恢复。
- 其他官方授权卡片行为、刷新链接和错误提示保持不变。

### 涉及文件
- `numind-web-v3/src/components/agent/FeishuActionCard.vue`
- `numind-web-v3/src/components/agent/AgentMessageItem.vue` 或 `src/stores/agentChat.ts`（只选现有最窄恢复入口）
- 对应定向测试

### 验收条件
- T1 前端 RED 转绿。
- DOM 中无 confirmation 确认/取消按钮。
- 同一 operation 的 legacy 自动恢复最多发出一次请求。
- create_app、user_auth、app_scope 现有测试不回归。

## T4 — 定向质量门与双复核

### 描述
按客户明确要求跳过全仓测试、全量 E2E、race、coverage 和无关视觉检查，只执行：

- server：受影响 package 的定向 Go 测试；`task lint`。
- web：相关 Vitest；`npm run lint`；`npm run type-check`。
- 独立 spec-compliance 与 code-quality/security 只读复核。

### 涉及文件
- 仅测试输出和 `.ndf/manifest.yaml`/QA 报告。

### 验收条件
- 所有上述命令 exit 0。
- reviewer 无 P0/P1；任何 P2 必须修复或记录。
- diff 不含 schema、路由、prod 配置、凭据或无关格式化。

## T5 — 原子合并、推送与 Dev 部署

### 描述
- 两个 worktree 分别运行 `ndf-done`，合并并推送 develop。
- 先部署 server，再部署 web。
- 检查公开 health、容器 health、部署 SHA 和启动日志。
- Prod 不在范围内。

### 涉及文件
- 无产品代码新增。
- `numind-server/.ndf/manifest.yaml`
- `docs/superpowers/qa/2026-07-20-feishu-action-transition-s5.md`

### 验收条件
- develop 与 origin/develop 一致，无未提交/未推送改动。
- Dev server/web 运行期镜像与 develop SHA 一致，健康检查通过。
- 向客户提供一条需要 `--yes` 的新 Base 字段修改测试提示词。

## 需求覆盖检查

| 设计需求 | Task |
|---|---|
| 不再生成 confirmation | T1、T2 |
| server-owned `--yes` | T1、T2 |
| 历史确认态兼容 | T1、T2、T3 |
| 旧卡片不再 InvalidParameter | T1、T2 |
| 前端不显示确认 UI | T1、T3 |
| 质量门与 Dev 交付 | T4、T5 |

计划无环，每个任务结束时仓库均可编译；T2 锁定后端契约后才开始 T3。
