# 飞书创建个人应用完成态修复 — S5 自动验收

日期：2026-07-16

## 回归路径

1. 伪 `lark-cli config init --new` 输出飞书官方页面 URL、普通文本完成提示，并写入完整 `config.json`。
2. `ControlledLarkCLIRunner` 接受该固定命令的正常退出；随后 `AppIDFromHome` 验证完整应用配置。
3. 当前任务摘要精确绑定的 `failed` 创建应用会话可替换为新会话。
4. 仍覆盖历史 `superseded` 卡片、活动租约和不同 scope 的活动替代会话拒绝逻辑。

## 结果

- RED：三个新回归测试在修复前分别因“非 JSON 完成输出”“失败卡返回未找到”“存储层找不到失败来源”失败。
- GREEN：飞书业务层与存储层聚焦回归测试通过。
- `task lint`：通过。macOS sqlite cgo 仅有已知弃用警告；初次执行未将 Go bin 目录加入 PATH，重试后 lint 正常完成。
- `task test`：通过（全仓 Go 测试）。
- 独立规格与代码质量审查：均 PASS，无 P0/P1。

## 不适用项

本次未修改前端，也未新增 API 或 LLM 调用；因此没有本地前端 Playwright/E2E 或 Langfuse 验证项。

## 待 S6 开发环境验收

部署后，使用当前飞书卡点击“重新生成链接”，在飞书官方页面完成创建，再点击“我已完成，继续”。预期不再返回 Internal server error，而是进入下一步授权或恢复原任务。
