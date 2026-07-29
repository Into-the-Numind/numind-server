# feishu-stability-recovery — 实施计划

## T1 — resume 扫描审计优化
- 文件：`internal/numind/biz/agent/external_tool_resume.go`、相关测试
- 内容：给 reclaimer scan 的全局候选扫描注入 `WithSkipScope("external_resume_reclaimer")`。
- 验收：单测捕获 context skip reason。

## T2 — 命令边界纠错提示
- 文件：`internal/numind/biz/agent/tool_lark_execute.go`、`tool_lark_skill_read.go`、测试
- 内容：对未注册 command path 返回可纠正 hint，特别处理 `drive +inspect` -> `lark_inspect`。
- 验收：模型可见输出包含“不是连接异常”“使用 lark_inspect”。

## T3 — unknown_result exact fence 回归
- 文件：`internal/numind/biz/agent/tool_lark_retry_budget.go`、测试
- 内容：确认 unknown_result 只阻止 exact same write key，不阻止 read 或不同 write。
- 验收：新增单测通过。

## T4 — 错误分类覆盖
- 文件：`internal/numind/biz/feishu/error_classifier.go`、`error_classifier_test.go`
- 内容：补固定 tuple 覆盖，减少泛化 `feishu_operation_failed`。
- 验收：分类测试覆盖 not_found/resource_denied/validation/temporary/reauth。

## T5 — 集成验证
- 运行 focused tests。
- 运行相关 package tests。
- 运行 `task lint`。
