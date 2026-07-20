# 飞书首次绑定全流程连续性

## 来源

- 提出人：Dev 真实用户（user_id=438）
- 提出日期：2026-07-20
- 性质：客户可复现 P0 授权阻断

## 需求描述

用户首次通过 Agent 使用飞书时，会先看到“创建个人应用”卡片。user 438 已在飞书官方页面完成创建并返回点击“我已完成，继续”，但原卡片持续提示“尚未检测到授权完成”。多次点击均无法继续原任务。

本次不能只修复该账号或延长轮询时间。必须从真实用户旅程出发，统一修复首次绑定、已有应用重新授权和后续增量授权中的阶段交接，使用户始终看到当前真正需要完成的步骤，并在完成后自动回到原 Agent 任务。

## 已确认的真实故障证据

- 20:21:34：operation 进入 `create_app`，Agent Run 持久化创建应用卡片。
- 20:22:05：创建应用完成，operation 已切换到新的 `user_auth` session。
- Agent Run 的 pending external action 没有同步替换，浏览器仍持有旧 `create_app` 卡片。
- 20:22:07：旧卡片的迟到点击只携带 operation ID，被后端错误解释为对新 `user_auth` session 的确认。
- 此后每次请求都等待约 30 秒并返回 `polling_pending_timeout → authorization_pending`，用户无法获得正确的新授权步骤。

## 业务目标

1. 首次连接对普通用户表现为一个连续、可理解、可恢复的向导，而不是多个互相脱节的后台状态。
2. 每张卡片只确认它展示的 exact session；迟到、重复、跨标签页或缓存中的旧卡片不得推进更新后的阶段。
3. 后端 operation、Agent Run pending action 和前端当前卡片必须最终收敛到同一个最新阶段。
4. 阶段完成后自动继续原 Agent 任务；不要求用户刷新页面、重发指令或理解技术术语。
5. 方案适用于 `create_app → user_auth`、`app_scope → user_auth`、重新授权及后续增量 scope，不含 user 438 特判。

## 用户体验边界

- 每一时刻最多只有一个可操作的“当前步骤”；历史步骤保留完成态，但不能继续提交。
- 阶段切换时，当前卡片原位或连续地更新标题、说明、链接、session 和按钮状态，明确告诉用户“应用已创建，下一步授权账号”。
- 用户过早点击、重复点击、网络超时、页面重开、另一个标签页完成、链接过期或服务重启时，系统都应返回最新安全动作，而不是永久 pending 或通用 500。
- 检测仍在处理中时给出短时、可理解的进度；不得让一次点击无反馈地阻塞 30 秒后才恢复按钮。
- 成功后同一页面继续展示 Agent 思考和最终结果，不制造第二个聊天任务。
- URL、device code、token、App Secret、HOME 路径和业务正文不得进入持久化 action、日志、LLM 或错误文案。

## 验收标准

1. 客户 RED 测试首先复现：旧 `create_app` 卡片在 operation 已进入 `user_auth` 后点击，不能确认新 session，且用户能获得最新 `user_auth` 动作。
2. 新阶段产生后，Agent Run 的 pending external action 以 operation/tool-call/current-session 身份原子或可补偿地更新；重启后仍能恢复。
3. 浏览器提交携带它实际展示的 session 身份；服务端拒绝把 stale session acknowledgement 应用到 current session，并返回最新可恢复状态。
4. 对同一 current session 的合法重复点击保持幂等；并发请求最多一个完成者，不重复启动 CLI、不重复执行飞书业务写入。
5. Playwright 覆盖首次连接完整旅程、迟到点击、双击/多击、阶段切换、页面重开、链接过期刷新、授权 pending/processing/success 和自动继续原任务。
6. user 438 的真实 Dev 链路修复后可重新生成最新授权步骤，并完成原任务；不直接修改生产数据。
7. Go 测试、race 重点套件、`task lint`、前端 unit、`npm run lint`、`npm run type-check` 和相关 Playwright 全部通过。

## 非目标

- 不新增飞书外部能力，不扩大 scope，不改变业务命令目录。
- 不通过延长轮询、无限重试、持久化一次性 URL 或手工改库掩盖状态机错误。
- 不部署生产；生产仍需独立授权。

## 优先级

P0 / 高

## Triage

- 推荐轨道：Standard（快速精简执行）
- 分类理由：
  1. 数据库 schema 变更：预计否
  2. 新增 API 端点：否；需要收紧现有 resume 请求契约
  3. 新外部服务集成：否
  4. 影响文件数：>3，涉及 Feishu dispatcher、Agent Run 持久化、HTTP 契约、前端 Store/卡片及端到端测试
  5. 高风险业务逻辑（支付/权限）：是，涉及第三方授权身份与阶段隔离
- 人类决定：确认 Standard；要求快速、精简、彻底且 universal

## 备注

- Bug-from-Customer 规则生效：feature 分支第一个独立 commit 必须是失败复现测试，随后再提交实现。
- 快速精简指缩短工件和只跑与风险匹配的必要流程，不跳过 S0→S7 阶段或质量门禁。
