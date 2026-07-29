# feishu-stability-recovery — 飞书 Agent 稳定性与首次成功率修复

## 来源
- 提出人：用户（产品负责人）
- 提出日期：2026-07-29
- 背景：dev 历史运行显示 Agent 操作飞书时存在连接/授权恢复、资源路由、错误分类和 unknown_result 处理体验不稳定。

## 需求描述
提升 Agent 连接飞书与执行 Docs/Base/Wiki/Drive 操作的首次成功率，并避免后台恢复扫描制造审计噪声。用户离开导致授权卡片自然过期是正常场景；修复重点不是强制清掉历史卡片，而是确保它们不影响其它正常场景、不长期消耗进程、不让 Agent 因模糊错误或命令边界误解而失败。

## 验收目标
- 后台 external resume 扫描不再刷 `compliance_audit_log`。
- `unknown_result` 后只禁止重复同一条不确定写命令，允许其它读/核验/无关操作继续。
- `lark_execute` 对错误命令边界给出可纠正提示，例如 `drive +inspect` 应提示使用 `lark_inspect`。
- Feishu 结构化错误分类覆盖更多固定 tuple，减少泛化 `feishu_operation_failed`。
- 历史问题场景形成 Go 回归测试：unknown-result fence、命令边界、read 类恢复提示。

## Triage
- 推荐轨道：Standard
- 理由：
  1. 数据库 schema 变更：否
  2. 新增 API 端点：否
  3. 新外部服务集成：否
  4. 影响文件数：预计 >3（Agent tool、Feishu classifier、resume scanner、测试）
  5. 涉及权限/外部工具执行安全边界：是
- 人类决定：确认 Standard（2026-07-29，对“可以做修改”“同意”的确认）

## 非目标
- 不强制清理所有过期历史卡片。
- 不允许 unknown_result 的同一写命令自动重放。
- 不做每个用户私有 skill/prompt 定制；只改平台内置工具契约和全局托管规则。
