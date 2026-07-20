# S4 Task 7：三 Agent 集成契约与全量门禁

- 日期：2026-07-20
- 可执行工作流：runner 场景加载仓库内正式 Prompt，Runner 从已加载 AgentDefinition 强制推导严格 allowlist，并由 fake executor 按真实 `lark_execute` command catalog、`file_read` 管理 URL、XHS 签名 cursor 与 `ask_user_question` yield 输入逐项校验。
- Agent 1：243 条 index 按 100/100/43 分页；首轮写入 40 条检查点，续跑只取剩余 203 条，full 每批不超过 100、Base create 每批不超过 20；另覆盖全 skip、新增、未完成 row 单条 upsert、显式范围重分析、重复业务键、部分成功和 unknown-write 读后对账，禁止 `record-batch-update`。
- Agent 2：覆盖上传、飞书和混合来源读到末页，精确搜索 0/1/>1，create/managed overwrite、unmanaged collision、受管 marker 损坏和来源未读完禁写；输出严格使用 `profile/v1` 成对标记与七个正式模块。官方授权另由真实 `lark_execute` external-action wait → 持久化 → 原 operation/tool-call resume 集成测试证明，不用模型提问或重放业务读取。
- Agent 3：只消费 Agent 1 的 34 字段 Base 记录和 Agent 2 的正式画像卡；真实内容 fixture 逐条验证九字段、固定生成路径/参考类型枚举、达标/部分达标借法、不达标排除、非蓝 V 零硬广、0-1 主语和不足 70 条说明；append/replace/unknown reconcile 前均 full fetch 受管目标并使用 `topics/v1` 文档头与成对中文轮次标记。
- 后端门禁：focused packages、focused race、`go test ./...`、`task lint`、`git diff --check` 全部通过；`config_prod.yaml` 零改动。
- 前端零回归：按锁文件安装 worktree 依赖后 `npm run lint` 与 `npm run type-check` 通过，0 error、7 条既有 warning，Git worktree 无源码变化。
- 外部边界：S4 没有调用真实 LLM、真实飞书或 Dev 环境写入；真实模型/授权/写入 smoke 留给 S5/S6 对应门禁。
