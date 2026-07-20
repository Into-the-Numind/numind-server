# S4 第二轮双审修复

- 日期：2026-07-20
- 第二轮结论：Spec FAIL（P0=0/P1=4/P2=1）；Quality/Security FAIL（P0=0/P1=1/P2=1）。本轮逐项修复后再跑门禁和双审，不带已知问题进入 S5。
- Runner 权限边界：`Run` 与 `RunStream` 在成功加载并完成租户校验后，以 AgentDefinition 的直接工具键覆盖请求中的陈旧/失配 `ToolNames` 并强制 allowlist。工作流非流式矩阵和流式测试均故意传入 fail-open 请求，证明未授权工具不会到达模型；category-only 旧定义保持兼容。
- Parser 最坏边界：20 MiB 正文按 JSON 控制字符 `\\u00xx` 的 6 倍最坏膨胀计算，stdout 上限为 `6 * 20 MiB + 1 MiB`；测试同时固定实际 NUL 转义比与常量边界。
- Agent 1 数据契约：所有 create/upsert 完成记录必须包含顺序固定的 34 字段、`分析状态=已完成` 与 `有数契约版本=xhs-viral-base/v1`；Base 创建包含完整字段类型、单/多选选项和第一文本业务键。新增标题精确搜索 0/1/>1 的 create/reuse/ask 可执行场景。
- Agent 2 数据契约：create/overwrite 正文使用唯一成对的 `[有数AI受管区：客户画像｜契约 profile/v1｜开始/结束]`，并完整包含资料来源判断、账号定位素材、核心人群画像、向内求素材库、第三方素材说明、深度看见候选点、资料缺口清单七模块。
- Agent 3 数据契约：文档头固定为 `[有数AI受管文档：选题规划｜契约 topics/v1]`；轮次使用带合法 UTC+hex round ID 的唯一成对中文标记；每条九字段中生成路径只允许 `向内求/向外求`，参考类型只允许 `结构+内容/仅参考结构/无，独立生成`。Agent 3 来源 fixture 直接消费 Agent 1 的完整 34 字段记录和 Agent 2 正式画像卡。
- 官方飞书授权：删除“模型询问用户后重放读取”的伪场景。新集成测试由真实 `lark_execute` 返回 `waiting_user_auth`，Runner 持久化不含临时 URL 的 external action；完成结果按原 operation ID/tool-call ID 构建无新增用户输入的 continuation，业务 argv 只执行一次。
