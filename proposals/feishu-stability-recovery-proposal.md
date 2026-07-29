# feishu-stability-recovery — 提案

## §1 方案概述
把飞书操作稳定性从“靠模型读提示猜下一步”推进到“平台给出明确边界和恢复信号”。本次先做后端工具层修复：消除合法后台扫描的审计刷屏，增强 `lark_execute` 命令纠错提示，补足 unknown_result 后可继续读/核验的回归保护，并扩展固定结构错误分类。

## §2 技术可行性
复用现有组件：
- `compliance_scope.WithSkipScope` 已存在，可用于内部系统扫描。
- `lark_execute` 已有 correction budget 与 exact write fence。
- `ErrorClassifier` 已有固定 tuple 和 public code 语义。
- `CommandCatalog` 已有 Docs/Base/Wiki/Drive allowlist，可用于命令边界提示。

主要风险：
- 过度放开导致重复写入：保留 exact write fence，不自动重放 unknown write。
- 错误分类过宽泄露敏感信息：只接受固定结构 tuple，不读取 stderr 文本。
- prompt 优化不可持续：本次只改平台内置 tool 输出和测试，不依赖用户私有 prompt。

## §3 PRD
### 用户故事
- 作为飞书 Agent 用户，我希望授权、查找资源、读取/写入时失败能自动进入正确恢复路径，而不是让我看到“稍后再试”或让 Agent 乱试命令。
- 作为系统维护者，我希望后台恢复扫描不污染 compliance audit，让真实风险容易被发现。

### 验收标准
- AC1：external resume scanner 的全局 `agent_run` 查询带系统 skip reason，不产生 scope audit 刷屏。
- AC2：`drive +inspect` 等未注册命令返回明确可纠正提示，指出使用 `lark_inspect`。
- AC3：`unknown_result` 仅 fence exact write key，后续不同读命令/不同写命令不被全局停止。
- AC4：固定结构错误 tuple 能被归类为 not_found/resource_denied/validation/temporary 等 public code。
- AC5：全部新增行为有 Go 单测覆盖，`task lint` 通过。

## §4 交付范围
- 仓库：numind-server
- 无 DB migration
- 无前端改动
- 无新 API endpoint
