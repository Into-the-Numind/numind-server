# 飞书写入命令指引与纠错能力修复 — 提案

## §1 方案概述 [客户可见]

这次问题不是 Agent “不会写长内容”，而是系统给了 Agent 几套互相打架的规则：

- `lark_execute` 的工具参数里写着可以传 `stdin_json`；
- Command Catalog 实际又拒绝任何 stdin；
- Base 批量写入参考文档还展示了当前托管环境不能使用的 `@文件` 写法；
- Agent 只被笼统要求“读取 Base 说明”，没有被明确要求在第一次写入前读完批量创建和 CellValue 两份关键参考。

修复后，Agent 仍然自己生成完整的内联 `--json`。平台不会替 Agent 拼装业务数据，只负责提供唯一、清楚、可校验的命令契约，并在错误时指出具体是哪类格式问题。

同时把同一会话内连续可纠错次数从 5 次提高到 10 次。第 1–9 次失败仍允许 Agent 修正，第 10 次进入耗尽状态，第 11 次直接拦截，不再调用执行器。

## §2 报价与周期 [客户可见]

- 预估工作量：1–2 个开发日
- 报价：内部产品修复，不单独计价
- 交付时间线：完成设计、回归测试与 Dev 验收后交付

## §3 技术可行性 [AI 内部]

### 产品前提与方案比较

**前提挑战：** 仅把 5 改成 10，会放大纠错空间，但不能消除 Agent 反复遵循错误契约的根因。因此“提高次数”和“统一契约”必须一起做。

**方案 A：最小改动**

- 只把上限改为 10，并从工具 Schema 删除 `stdin_json`。
- 优点：改动小、交付快。
- 缺点：`@file` 文档、必读 reference、失败诊断仍可能互相矛盾，不能解释下次为何失败。

**方案 B：完整契约修复（采用）**

- 上限改为 10；
- 模型可见的 `lark_execute` 只接受 `argv`，滚动发布期间仅在内部兼容旧的 `stdin_json:null`；
- 托管说明明确只允许内联 `--json`，不允许 stdin 或 `@file`；
- Agent 1 首次批量写入前必须读取 `record-batch-create` 和 CellValue 两份指定 reference；
- 增加命令级纠错提示与脱敏诊断事件。
- 优点：直接解决根因，同时保留 Agent 自己构造完整 JSON 的能力。
- 缺点：涉及文件和回归测试较多。

**方案 C：后端结构化转换**

- Agent 只提交业务对象，后台转换成 lark-cli payload。
- 优点：命令格式最稳定。
- 缺点：把业务格式职责从 Agent 转移到后台，不符合用户明确选择，本次不采用。

### 现有功能复用

- 复用 `larkExecuteRetryBudget` 的连续失败计数、成功重置和 exhausted 拦截。
- 复用 `CommandCatalog.Normalize` 的严格命令 allowlist 与批量 JSON shape 校验。
- 复用 `lark_skill_read` 的受控技能/reference 读取能力。
- 复用现有 Agent trace/narration 与结构化日志能力，不新增数据库表。
- 复用三 Agent 流水线的 runtime contract、系统提示和 workflow contract test。

### 技术风险

- **兼容风险：** 历史模型调用可能仍带 `stdin_json:null`。缓解：模型 Schema 与说明立即移除，解码层短期只兼容 `null`，非空 stdin 明确拒绝。
- **安全风险：** 放宽到 10 次不能绕过安全策略。缓解：每次仍走相同 Command Catalog 校验，第 10 次耗尽，第 11 次不访问执行器。
- **泄露风险：** 长 JSON、Base token 或正文进入日志。缓解：诊断只记录 run/tool、规范化命令类别、错误分类、attempt/max_attempts、`feishu_called` 和所读 reference 标识，不记录 argv、stdin 或 payload。
- **提示漂移：** 文档和 Prompt 以后再次分叉。缓解：增加契约测试，让模型可见 Schema、托管策略、Agent 1 Prompt 和 reference 用法保持一致。

### 涉及仓库

- [x] numind-server
- [ ] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性（如功能涉及 LLM 调用）

- [x] 涉及 LLM 调用：是
- Trace 起点：沿用现有 Agent run trace，不新增 trace 根。
- Generation 点：不新增 LLM API 调用或 generation。
- 关键元数据：`run_id`、`tool_name`、`skill`、`reference`、`command_class`、`error_class`、`attempt`、`max_attempts`、`feishu_called`。
- 数据边界：不得记录 Base token、正文、完整 argv、完整 JSON 或飞书响应正文。

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事

- 作为使用飞书流水线的用户，我需要 Agent 在写入前获得一套唯一且可执行的 Base 批量写入规则，以便分析结果能实际写入，而不是停在本地校验。
- 作为 Agent，我需要知道必须读取哪两份参考、只能使用哪种 JSON 传递方式，以及具体哪类参数不合法，以便自主修正长命令。
- 作为系统维护者，我需要在不看到用户正文和凭据的情况下确认 Agent 读了哪个 reference、失败在哪一层、飞书是否被调用，以便快速定位同类问题。

### 验收标准

- [ ] 连续可纠错上限为 10：失败 1–9 可继续，第 10 次 exhausted，第 11 次不访问执行器；成功后计数重置。
- [ ] 模型可见的 `lark_execute` Schema 不再宣称接受 `stdin_json`。
- [ ] 兼容层只允许历史 `stdin_json:null`；非空 stdin 返回一致的纠错错误，且不访问飞书。
- [ ] 托管环境所有模型可见说明一致声明：Base 写入使用完整内联 `--json`，不支持 stdin 和 `@file`。
- [ ] Agent 1 第一次 `record-batch-create` 前明确读取批量创建和 CellValue 两份指定 reference。
- [ ] `record-batch-create` 的错误反馈能区分 JSON 解析、顶层 shape、字段/行长度、批次上限等可纠错类别，同时不回显 payload。
- [ ] 安全诊断能识别 exact `skill/reference`、命令类别、错误类别、attempt/max_attempts 与 `feishu_called`，且无敏感字段。
- [ ] Dev run #359 的 8 条分析批量写入场景有永久回归测试。
- [ ] Agent 仍自己构造完整 `--json`；不存在后台业务 payload 转换。
- [ ] 不修改最终面向用户的汇总提示文案。
- [ ] 相关 Go 测试与 `task lint` 通过。

### 边界情况

- `stdin_json` 缺失或为 `null`：兼容并按仅 `argv` 执行。
- `stdin_json` 为对象、数组、字符串或其他非空值：执行前拒绝。
- `--json` 不是合法 JSON、顶层 key 错误、fields 重复、row 长度不匹配、0 行或超过 200 行：给出固定类别的安全纠错提示。
- 21–200 行仍按现有高风险确认流程处理，不因本次修改降低风险等级。
- 一次合法成功执行后，连续失败计数归零。
- reference 读取失败时不得假装已读取；首次写入的契约测试必须失败。

### 权限规则

- 不改变任何用户、父子账户、飞书 scope、资源或字段权限规则。
- 不改变现有授权恢复、风险确认、unknown-result fence 和 operation 幂等规则。

### UI 行为规格

- 无前端页面或交互改动。
- 不修改现有最终用户汇总提示文案。
