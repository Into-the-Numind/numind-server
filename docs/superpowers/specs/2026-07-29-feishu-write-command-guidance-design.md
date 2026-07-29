# 飞书写入命令指引与纠错能力修复 — 技术设计

日期：2026-07-29  
仓库：`numind-server`  
轨道：NDF Standard  
状态：S2 待确认

## 1. 问题定义与已知证据

Dev `agent_run #359` 已成功完成 8 条小红书笔记分析，但连续 5 次 Base 写入都没有通过服务端执行前校验，飞书 API 没有收到写请求。

现有证据能确认：

- run 内成功调用过 4 次 `lark_skill_read`；
- 其中一次紧邻首次写入；
- 持久化轨迹没有保存具体 `reference`，因此不能证明 Agent 是否读到了批量创建和 CellValue 两份关键 reference；
- `lark_execute` 的模型可见 Schema 宣称接受 `stdin_json`；
- Command Catalog 明确拒绝非空 stdin；
- 官方批量创建 reference 同时展示内联 `--json` 和 `--json @batch-create.json`，而有数托管环境禁止文件间接引用；
- 纠错窗口达到 5 次后，第 6 次会被执行器前置拦截。

所以根因不是“Agent 没能力生成长 JSON”，而是模型可见契约不唯一，加上现有轨迹无法还原 Agent 到底看过哪份细则。提高次数只能增加纠错空间，不能单独解决根因。

## 2. 设计目标

1. 同一 run 的连续可纠错总尝试由 5 次提高为 10 次。
2. 让模型只看到一种托管写法：完整 JSON 作为 `--json` 后的一个内联 argv。
3. 模型不再看到 `stdin_json`，也不再把 `@file` 或 `-` 当作托管环境可用写法。
4. Agent 1 第一次批量创建前明确读取：
   - `references/lark-base-record-batch-create.md`
   - `references/lark-base-cell-value.md`
5. Command Catalog 继续负责 shape、批次、字段宽度和风险等级校验；后台不构造业务 payload。
6. Langfuse trace 能安全回答：读了什么 reference、执行的命令类别、失败类别、尝试次数、飞书是否被调用。
7. 不改变面向最终用户的汇总提示文案。

## 3. 非目标

- 不新增 Base 专用结构化写入工具。
- 不把分析结果对象交给后台转换成 lark-cli JSON。
- 不降低 Command Catalog、scope preflight、确认、幂等或 unknown-result fence。
- 不新增数据库表、API endpoint 或前端页面。
- 不复制或修改本机安装的 lark-cli 官方 skill 文件；有数通过优先级更高的 hosted policy 覆盖托管差异。
- 不重新引入由模型抄写 skill receipt 的校验。

## 4. 方案比较

### 4.1 最小方案：只改次数和 Schema

把常量改为 10，从 Schema 删除 `stdin_json`。

优点是改动小；缺点是 `@file`、reference 读取和故障证据仍不完整，下一次失败依旧难以解释。

### 4.2 采用方案：统一模型契约 + 安全证据

同步修改工具 Schema、托管策略、Agent 1 运行契约、命令纠错和 trace；保留 Agent 自写完整 JSON。

这是本次采用方案，因为它同时解决“为什么写错”和“以后如何证明”的问题，没有把业务职责移到后台。

### 4.3 未采用方案：后台 payload 转换

新增业务对象 Schema，由后台生成 lark-cli JSON。该方案可减少命令格式错误，但与用户明确要求冲突，也会让后台绑定 Agent 1 的业务字段，故不采用。

## 5. 总体架构

```text
Agent 1 runtime contract
  ├─ 读取 lark-base 主技能
  ├─ 读取 batch-create reference
  ├─ 读取 CellValue reference
  └─ 自己生成完整 argv:
       ["base","+record-batch-create",...,"--json","{...}"]
                         │
                         ▼
lark_execute tool boundary
  ├─ 模型 Schema：只暴露 argv
  ├─ 旧请求兼容：stdin_json:null 可忽略
  ├─ 非空 stdin_json：执行前纠错拒绝
  ├─ 10 次连续纠错预算
  └─ 安全 trace：只记录类别，不记录 argv/payload
                         │
                         ▼
Command Catalog
  ├─ 精确命令/flag allowlist
  ├─ 禁止 stdin、@file、"-"
  ├─ 校验 fields + rows
  ├─ 1–200 行；21–200 行升级高风险确认
  └─ 返回固定、脱敏、可纠错原因
                         │
                         ▼
Operation Service / Feishu
```

## 6. 组件设计

### 6.1 纠错预算

文件：

- `internal/numind/biz/agent/tool_lark_retry_budget.go`
- `internal/numind/biz/agent/tool_lark_skill_read.go`
- `internal/numind/biz/agent/tool_lark_personal_workspace_test.go`

将 `larkExecuteMaxCorrectableAttempts` 从 5 改为 10。语义保持不变：

- 第 1–9 次可纠错失败：`recoverable=true`；
- 第 10 次失败：返回 `correction_exhausted`；
- 第 11 次：`larkExecuteRetryBegin` 直接返回 exhausted，不访问 executor；
- 任意成功结果：计数重置；
- terminal 非可纠错结果：恢复 ready，不消耗后续不同命令；
- unknown write：仍只 fence 完全相同的写命令。

所有硬编码“5 次”和测试循环统一改为常量或 10 次，避免以后只改一处。

### 6.2 `stdin_json` 契约

文件：

- `internal/numind/biz/agent/tool_lark_execute.go`
- `internal/numind/biz/agent/tool_lark_skill_read.go`
- `internal/numind/biz/feishu/command_catalog.go`

模型新协议：

```json
{
  "type": "object",
  "properties": {
    "argv": {
      "type": "array",
      "minItems": 1,
      "items": {"type": "string"}
    }
  },
  "required": ["argv"],
  "additionalProperties": false
}
```

滚动发布兼容：

- 解码层继续识别历史 `skill_receipts`，但不解析、不使用；
- 解码层可接受历史 `stdin_json:null` 并当作缺失；
- 任意非空 `stdin_json` 返回固定 `unsupported_stdin_json` 可纠错结果；
- 非空 stdin 不进入 Command Catalog，不调用 operation executor，不访问飞书；
- 新 Schema、Description、hosted policy 和纠错信息都只要求 `argv`。

Command Catalog 仍保留“stdin 必须为空”的防御性校验，保护绕过 Agent 工具的内部调用路径。该防线不再与模型协议矛盾，因为模型根本看不到 stdin 参数。

### 6.3 托管 JSON 传递规则

`larkHostedExecutionPolicy` 增加一段高优先级托管覆盖：

> 有数托管环境的 JSON 必须作为对应 `--json` 参数后的一个完整内联 argv；不支持 `stdin_json`、`@file`、`-` 或任何本地文件/stdin 间接引用。官方 skill 若同时展示内联和 `@file`，只采用内联示例。

`lark_execute.Description()` 同步简短声明。`HostedCommandContract("lark-base")` 的契约测试必须证明：

- 包含 `base +record-batch-create` 和 `--json`；
- 明确只允许内联 JSON；
- 不宣称支持 stdin 或文件间接引用。

Command Catalog 继续通过 `normalizeInlineText`/`normalizeJSON` 拒绝以 `@` 开头或 `-` 形式的值。

### 6.4 Agent 1 精确 reference 指引

文件：

- `docs/agent-definitions/three-agent-feishu-pipeline/agent-1-runtime-contract.md`
- `docs/agent-definitions/three-agent-feishu-pipeline/agent-1-system-prompt.md`
- `docs/agent-definitions/three-agent-feishu-pipeline/manifest.json`
- `internal/numind/biz/skill/three_agent_definition_contract_test.go`
- `internal/numind/biz/agent/three_agent_pipeline_workflow_contract_test.go`

在 Agent 1 的“默认扫描与逐条写入算法”中加入确定顺序：

1. 读取 Base 主技能；
2. 读取真实字段；
3. 第一次 `record-batch-create` 前调用两次 `lark_skill_read`：
   - `{"skill":"lark-base","reference":"lark-base-record-batch-create.md"}`
   - `{"skill":"lark-base","reference":"lark-base-cell-value.md"}`
4. 按 reference 生成完整内联 `--json`；
5. 串行提交每一批。

这里采用 basename，是因为 `SkillReader` 已安全地把唯一 basename 解析到当前 skill 声明的 canonical path；trace 输出使用服务端返回的 canonical path。

不增加“reference receipt 必须由模型复制到写命令”的硬门槛。原因：

- receipt 不是用户身份、授权、scope 或业务 payload 证据；
-历史事故已证明让模型复制长 receipt 会使合法命令在飞书前失败；
- 本次通过明确 Prompt、模型始终可见的 hosted policy、workflow contract test 和 exact-reference trace 形成闭环。

最终 Prompt 仍由 runtime contract + 受控业务 Prompt 确定性生成；修改后重新计算 manifest 中 `prompt_sha256`，不改业务判断 SSOT。

### 6.5 可纠错命令反馈

Command Catalog 保留现有严格校验，并保证 `record-batch-create` 至少提供以下固定安全原因：

- 缺少 `--base-token`、`--table-id` 或 `--json`；
- `--json` 不是合法 JSON；
- 顶层不是严格的 `{"fields":[...],"rows":[...]}`；
- fields 为空、超过字段上限或重复；
- rows 为空或超过 200；
- row 宽度与 fields 不一致；
- `@file`、`-` 或其他间接引用；
- 未允许的 flag 或命令。

`lark_execute` 对 `SafeCommandValidationHint` 可安全公开的 catalog 错误统一在执行前返回，不再只对 `+inspect` 特例本地处理。结果包含：

```json
{
  "code": "command_validation",
  "category": "validation",
  "stage": "pre_execution",
  "attempt": 1,
  "max_attempts": 10,
  "remaining_attempts": 9,
  "feishu_called": false,
  "recoverable": true
}
```

错误正文只包含 catalog 生成的最长 256 字节固定原因，不回显 flag 值、token、URL、正文或 JSON。

### 6.6 安全可观测性

复用 `safePipelineToolSpan`，不新增 generation。

#### Trace topology

- Trace 起点：现有 `agent-runtime-run`
- Span 1：`tool.lark_skill_read.execute`
- Span 2：`tool.lark_execute.execute`
- Operation 层现有 span/observer：保持不变
- 新 LLM generation：无

#### `tool.lark_skill_read.execute`

安全 input：

```json
{
  "run_id": 359,
  "skill": "lark-base",
  "requested_reference": "lark-base-record-batch-create.md"
}
```

安全 output：

```json
{
  "ok": true,
  "resolved_path": "references/lark-base-record-batch-create.md",
  "page_count": 1,
  "error_class": "none"
}
```

只允许五个固定 skill 名和由当前 skill 声明并解析后的 reference path，因此这些值可记录。不得记录 content、cursor、receipt、完整工具输入或错误原文。

#### `tool.lark_execute.execute`

安全 input：

```json
{
  "run_id": 359,
  "command_class": "base +record-batch-create"
}
```

`command_class` 只在前两个 token 通过安全字符集时记录规范化 path，否则写 `invalid`。

安全 output：

```json
{
  "attempt": 5,
  "max_attempts": 10,
  "feishu_called": false,
  "error_class": "catalog_validation"
}
```

固定 `error_class`：

- `none`
- `invalid_identity`
- `invalid_execute_input`
- `unsupported_stdin_json`
- `catalog_denied`
- `catalog_validation`
- `topic_guard`
- `operation_request_validation`
- `operation_unavailable`
- `waiting_authorization`
- `invalid_result`
- `operation_failed`
- `correction_exhausted`
- `execution_fenced`
- `command_in_flight`

`feishu_called` 规则：

- Agent 解码、Catalog、Prompt topic guard、operation request validation 前失败：`false`；
- local-only 命令：`false`；
- waiting connection/scope/auth/confirmation：`false`；
- operation 明确成功或 failure 中 `BusinessStarted=true`：`true`；
- operation 内部错误无法证明时：`unknown`。

为避免误导，trace 字段允许布尔值或字符串 `unknown`；模型可见的执行前纠错结果仍为布尔 `false`。

不得进入 span 的字段：`user_id`、Base token、table ID、record ID、URL、完整 argv、stdin、payload、正文、飞书响应正文、provider 原始错误。

## 7. 数据流与错误流

### 7.1 正常批量写入

1. Agent 读取 Base 主技能与两份指定 reference。
2. Agent 读取 field list 并生成 8 行完整 payload。
3. `lark_execute` 解码 argv，安全提取命令类别。
4. Command Catalog 校验内联 JSON、fields/rows 和风险。
5. 8 行属于 `RiskWrite`，进入现有 operation 流程。
6. 飞书成功后 retry budget 重置。
7. Agent 按现有流程重新读取 Base 或继续下一批。

### 7.2 格式错误

1. Catalog 在任何网络访问前返回分类错误。
2. retry budget 增加 1。
3. 模型得到固定 hint、attempt/max/remaining 和 `feishu_called=false`。
4. trace 记录 `catalog_validation`，不记录 payload。
5. Agent 根据原因重建完整内联 JSON。

### 7.3 第 10 与第 11 次

- 第 10 次失败返回 `correction_exhausted`，attempt=10。
- 第 11 次在 retry begin 阶段拦截，operation executor 调用次数保持 10。
- 不改变现有最终用户汇总文案；Agent 按已有运行契约如实报告未完成。

## 8. 测试设计

### 8.1 客户问题 RED commit

feature 分支的第一个 commit 只加入失败复现测试，commit message：

`test(qa): reproduce Feishu write command guidance conflict`

测试固定 Dev run #359 的核心事实：

- 8 条不同分析可组成一个长内联 `fields + rows` payload；
- Schema 不应暴露 `stdin_json`；
- hosted policy 不应允许 stdin / `@file`；
- Agent 1 Prompt 应在首次 batch create 前要求两份 exact reference；
- 10 次预算语义应成立；
- 测试在修复前失败。

### 8.2 单元与契约测试

- `tool_lark_personal_workspace_test.go`
  - attempts 1–9 可纠错、10 exhausted、11 不访问 executor；
  - 第 10 次成功可执行并重置；
  - Schema 只含 argv；
  - `stdin_json:null` 兼容；
  - 非空 stdin 拒绝且 executor 0 次；
  - hosted policy 的内联规则；
  - lark skill read/execute span 无敏感内容。
- `command_catalog_test.go`
  - 8 行长内联 JSON 通过；
  - `@file`、stdin、错误顶层、重复 fields、row width、201 行分别给安全 hint；
  - 21–200 行仍为 `RiskHigh`。
- `three_agent_definition_contract_test.go`
  - runtime contract 与最终 Prompt 包含两份 exact reference 和内联约束；
  - prompt hash/manifest 保持一致。
- `three_agent_pipeline_workflow_contract_test.go`
  - scripted Agent 1 流程中两次 reference read 发生在首次 batch create 前；
  - 8 条结果保持逐条不同，不做后台转换。
- Langfuse capture test
  - exact reference 与 safe command class 可见；
  - token、正文、完整 argv、JSON 和原始错误不可见。

### 8.3 验证命令

```bash
go test ./internal/numind/biz/feishu
go test ./internal/numind/biz/agent
go test ./internal/numind/biz/skill
go test ./...
task lint
```

## 9. PRD 覆盖检查

| PRD 验收项 | 设计覆盖 |
|---|---|
| 10 次纠错，第 11 次不访问 executor | §6.1、§7.3、§8 |
| Schema 不再暴露 stdin_json | §6.2 |
| 旧 null 兼容、非空拒绝 | §6.2 |
| 只允许完整内联 JSON | §6.3 |
| 两份 reference 在首次写入前读取 | §6.4、§8.2 |
| 可纠错分类原因 | §6.5 |
| exact reference 与脱敏执行证据 | §6.6 |
| run #359 永久回归 | §8.1 |
| Agent 自己构造 payload | §2、§3、§7.1 |
| 不修改最终用户汇总文案 | §2、§3、§7.3 |

## 10. 回滚与兼容

- 若新 Schema 引发旧会话兼容问题，可保留解码层的 `stdin_json:null` 兼容，不需要重新暴露 Schema。
- 若 trace 发现任何非 allowlist 字段，立即移除相应 span 字段；功能不依赖 trace 才能执行。
- 若 Agent Prompt 修改导致流水线行为偏离，只回滚 Prompt/reference 顺序；Catalog 内联安全规则与 10 次预算可独立保留。
- 本设计无 migration、无外部 API 契约变化，回滚为普通代码回滚。

## 11. 明确决策

1. Agent 继续负责完整业务 JSON。
2. 后台只做命令安全校验，不做业务 payload 转换。
3. 模型协议只暴露 `argv`。
4. 官方 reference 中的 `@file` 在有数 hosted policy 下明确不可用。
5. exact-reference 可观测，但不要求模型复制 receipt。
6. correction budget 为 10。
7. 最终用户汇总文案不变。
