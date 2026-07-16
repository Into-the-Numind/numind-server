# 飞书创建个人应用完成态修复 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 正确完成飞书个人应用创建，并允许当前绑定的失败授权卡安全重试。

**Architecture:** 受控执行器根据固定命令采用相应的完成证据；工作器继续以完整应用配置为创建命令的最终证明。刷新链路只对精确绑定的失败会话复用现有事务性替换。

**Tech Stack:** Go、GORM、现有 lark-cli adapter、Go testing。

---

### Task 1: 固化用户报错的回归测试

**Files:**
- Modify: `internal/numind/biz/feishu/auth_session_service_test.go`
- Modify: `internal/numind/biz/feishu/service_test.go`
- Modify: `internal/numind/store/feishu_workspace_test.go`

- [ ] 写入伪 `config init --new`：输出合法官方 URL 与普通文本、写入完整应用配置；断言当前代码拒绝该完成结果。
- [ ] 写入当前任务精确绑定 `failed` 会话的刷新测试，以及存储层替换测试。
- [ ] 运行聚焦测试并确认它们在修复前失败。
- [ ] 单独提交失败测试：`test(qa): reproduce feishu app creation completion failure`。

### Task 2: 修正创建应用完成证据

**Files:**
- Modify: `internal/numind/biz/feishu/auth_session_service.go`

- [ ] 仅为严格匹配的 `config init --new` 跳过 JSON 回执解析；保留其余受控执行器检查。
- [ ] 保持 `runWorker` 的 `AppIDFromHome` 完整配置校验，未完成配置仍失败。
- [ ] 运行授权服务聚焦测试，确认回归测试通过。

### Task 3: 允许精确绑定的失败卡重试

**Files:**
- Modify: `internal/numind/biz/feishu/service.go`
- Modify: `internal/numind/biz/feishu/auth_session_service.go`
- Modify: `internal/numind/store/feishu_workspace.go`

- [ ] 在生命周期、授权服务与存储层接受受限 `failed` 来源，其他精确围栏不变。
- [ ] 把失败来源也纳入活动替代会话检查，阻止并发生成。
- [ ] 运行生命周期、授权服务、存储层聚焦测试。

### Task 4: 完整验证与开发环境验收

**Files:**
- Create: `docs/superpowers/qa/2026-07-16-feishu-create-app-finalization-s5-acceptance.md`

- [ ] 运行 `task lint` 和 `task test`。
- [ ] 复核回归测试的 RED → GREEN 提交顺序和设计验收标准。
- [ ] 合并、推送、部署开发环境并检查健康状态。
- [ ] 请用户从当前卡片重新生成链接，完成飞书页面后点击“我已完成，继续”。
