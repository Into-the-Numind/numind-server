# S4 Task 7：三 Agent 集成契约与全量门禁

- 日期：2026-07-20
- 可执行工作流：新增 22 个 runner + scripted-model + fake-tool 场景。Fake tool 按固定顺序返回分页、搜索、授权、写入成功/失败/unknown 等结果，runner 的最终安全 marker 与预期操作计数或输出模式逐项核对。
- Agent 1：覆盖超过 100 条的 index 分页、第二次全 skip、新增一条、未完成 row 单条 upsert、显式范围重分析、重复业务键、部分成功和 unknown-write 读后对账；所有不同分析结果都禁止 `record-batch-update`。
- Agent 2：覆盖上传、飞书和混合来源读到末页，精确搜索 0/1/>1，官方授权恢复，create/managed overwrite、unmanaged collision、受管 marker 损坏和来源未读完禁写。
- Agent 3：只消费 Agent 1/2 产物；覆盖九字段、达标筛选、蓝 V 上限、create、append、指定 round 精确 replace、unknown-write marker 对账、同名多目标和无标记不接管。
- 后端门禁：focused packages、focused race、`go test ./...`、`task lint`、`git diff --check` 全部通过；`config_prod.yaml` 零改动。
- 前端零回归：按锁文件安装 worktree 依赖后 `npm run lint` 与 `npm run type-check` 通过，0 error、7 条既有 warning，Git worktree 无源码变化。
- 外部边界：S4 没有调用真实 LLM、真实飞书或 Dev 环境写入；真实模型/授权/写入 smoke 留给 S5/S6 对应门禁。
