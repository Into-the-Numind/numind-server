# Proposal / PRD — tool-soft-error-sweep

## 目标

agent 工具层对"模型发来的烂参数"和"可恢复运行期错误"全面免疫：错误以 LLM 可读的 soft error 回传，agent run 永不因此终止。对齐 Claude Code 的工具错误处理哲学（一切包成 is_error tool_result，会话不死）。

## 方案选型

**Option A（选定）：沿用已验证的 soft error 模式做全量清扫**
- 仓库已有 3 个成熟范例：web_search `returnSoftError`（commit ad007b10）、ask_user_question `softError`（816a43fe）、png_chart `chartFriendlyError`
- 有文件级 helper 的复用既有 helper（image_gen/web_fetch 的 helper 已存在，只是入口没用）；没有的用新增共享 helper `softToolError(tool, format, ...)`
- 错误 JSON 形状沿用既有约定 `{"error": "ERROR: <tool>: <msg>"}`

**Option B（否决）：升级/魔改 Eino 加 tool-error→tool-message 钩子**
- 改框架层影响全部 ToolsNode 行为，风险大、收益同 A；Eino 升级是独立 follow-up

**Option C（否决）：runner 层统一 recover 包装所有工具**
- 会把真正的系统不变量错误（ctx 取消、yield 机制）也吞掉，破坏暂停/终止语义

## 错误分类契约（本 feature 的核心设计）

| 错误类别 | 处理 | 理由 |
|---|---|---|
| 输入 unmarshal 失败 | soft | 模型类型错/截断，可自我纠正 |
| 输入业务校验失败（缺字段/越界/空值） | soft | 同上 |
| 可恢复运行期错误（KB 检索失败、COS 上传失败、memory 存储读写失败、模板渲染失败） | soft | 瞬态故障不应杀 run，模型可重试或绕过 |
| context 取消 / deadline | **hard（保留）** | run 级终止语义，必须传播 |
| yield（ask_user_question 暂停） | **hard（保留）** | 暂停机制依赖 error 通道 |
| 计费 Reserve 失败 | soft（image_gen 已是） | 已有先例，积分不足应告知模型收尾 |

## 影响范围

仅 numind-server `internal/numind/biz/agent/tool_*.go` + 配套测试。无 DB、无 API、无前端。

## 不做什么

- 不做输入类型纠偏扩展（如 bool→string coerce）——soft error + 模型重试已足够（Claude Code 同样只回传 Zod 错误不做 coerce）；web_search 的 coerceJSONInt 保留不动
- 不动 Eino、不动 runner 状态机、不动已加固工具
- 不做"复读退化检测/质量 fallback/think 标签清理"（Claude Code 未做，用户拍板不做，见 memory）
