# feishu-stability-recovery — 技术设计

## §1 设计原则
- 用户离开造成授权卡片过期是正常行为；系统只保证不会无限锁、不会保留活 worker、不会影响其它用户。
- 写命令 unknown 后不得自动重放同一条写，因为副作用是否发生不可证明。
- 读/核验/无关操作应继续，平台输出必须清楚告知模型下一步可做什么。
- 错误分类只基于固定结构字段，不解析自由文本。

## §2 改动点
1. `ExternalResumeReclaimer.scan` 调用 `ListExternalToolResumeCandidates` 前注入 `compliance_scope.WithSkipScope(ctx, "external_resume_reclaimer")`。
2. `lark_execute` 对 catalog rejected command 生成更具体的 correction hint：
   - 未注册 `drive +inspect`：提示这是工具边界混淆，应调用 `lark_inspect`。
   - 其它未注册 Docs/Base/Wiki/Drive command：列出 command path 未注册，不要当作连接异常。
3. `larkWorkspaceErrorExecuteStopped` 文案继续强调只停止 exact same write，不阻止其它操作。
4. `ErrorClassifier` 增加/验证固定结构 tuple 覆盖，确保安全 tuple 映射到 public code。
5. 新增测试覆盖 AC。

## §3 验收映射
- AC1：agent package 单测断言 reclaimer store 收到 skip-scope context。
- AC2：`tool_lark_personal_workspace_test` 增加 `drive +inspect` correction 输出断言。
- AC3：retry budget 单测覆盖 unknown fence 只拦 exact write，不拦读/其它写。
- AC4：classifier table tests 覆盖新增 tuple。
- AC5：运行 focused tests、相关 package tests、`task lint`。

## §4 不改项
- 不放开 shell/auth/config/whoami 等非业务命令。
- 不自动重放 unknown write。
- 不新增前端交互。
