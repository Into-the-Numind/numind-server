# 飞书技能读取容错与平台分页设计

## 1. 目标与非目标

系统继续采用“LLM 判断，平台防护”的架构：LLM 选择五个官方技能之一、选择当前任务需要的参考说明，并根据说明生成 Docs/Base/Wiki/Drive 业务命令。平台只处理 cursor、分页、兼容和固定错误分类。

本期不新增 LLM 调用、HTTP API、数据库字段、飞书技能或业务命令；不升级 lark-cli；不改变当前用户身份绑定、scope preflight、授权恢复、确认、幂等和 unknown-write 停止逻辑；不修改前端生产代码。

## 2. 模型协议

### 2.1 新调用协议

`lark_skill_read` 的模型可见输入只有：

- `skill`：必填，仍限 `lark-shared`、`lark-doc`、`lark-base`、`lark-wiki`、`lark-drive`。
- `reference`：可选，使用当前技能主页声明的标准 `references/...` 或安全唯一 basename。

模型 schema 不再出现 `cursor`。工具输出不再出现 cursor 或 receipt。描述明确告诉模型平台会自动读完受控说明。

### 2.2 滚动兼容

后端严格 JSON decoder 继续接受旧 `cursor` 字段，因此已生成的在途调用不会因部署而中断。兼容字段不重新出现在模型 schema。

同时提供 `reference` 和 `cursor` 时保留旧续页语义，不做字段交换；cursor 必须与 run、skill、canonical reference、digest 和 TTL 完全匹配。

## 3. 精确字段纠错

纠错位于 `feishu.SkillReader.Read`，发生在参考语法校验和资源读取之前：

1. 仅当 `Reference == "" && Cursor != ""` 时考虑纠错。
2. 先按现有 HMAC 协议尝试解码 cursor。能解码的真实 cursor 保持原语义，后续继续验证 run/skill/reference。
3. 解码失败时，只有值满足现有 `validSkillReferenceInput` 且具有官方 Markdown 参考形状（标准 `references/...md` 或安全 `.md` basename），才移动为 `Reference` 并清空 `Cursor`。
4. 之后完整执行现有流程：读取当前技能主页、派生声明白名单、用 `resolveSkillReference` 唯一规范化，再调用固定 CLI。

因此“字段放错”可被修正，但“路径不合法、当前技能未声明、同名歧义、跨技能、伪造或篡改 cursor”仍 fail closed。纠错从不读取 OS 文件，也不扩展声明白名单。

## 4. 平台内部分页

### 4.1 聚合流程

`larkSkillReadTool.Execute` 首次调用现有 `SkillReadExecutor.Read`，然后：

1. 保存第一页经过 reader 验证的 skill、path、references。
2. 拼接 UTF-8 content。
3. 如果 page cursor 为空，返回完整 JSON。
4. 如果 cursor 非空，平台把 opaque cursor 复制到下一次请求；模型不可见。
5. 首次请求是“cursor 中放 reference”且工具层原始 `Reference` 为空时，使用第一页返回的 canonical `Path` 固定后续请求 reference；主 `SKILL.md` 的 path 不写入 reference。
6. 第二页必须保持相同 skill/path；不一致、重复 cursor 或 reader 错误均终止。

### 4.2 有界性

- 每次工具调用最多读取 2 页。默认每页最多 32 KiB；当前固定 1.0.68 五个主页均为一页。
- 每次聚合后都对完整 `json.Marshal` 信封检查 `larkSkillReadAtomicOutputLimit`（64 KiB），而不是只计算 content。这样覆盖引号、反斜杠、控制字符转义、hosted policy 和最多 16 KiB references 元数据。
- 第二页后仍有 cursor、cursor 重复、信封超限或任何元数据不一致时，返回固定不可恢复 `skill_read_unavailable`；不返回部分正文、不暴露内部 cursor、不写普通 artifact。
- `boundedAtomicSkillTool` 保留同一个 64 KiB 常量作为第二道防线，且仍只信任真实 `*larkSkillReadTool` 类型。

## 5. 错误语义

首次 reader 调用的错误按错误链分类：

- `errors.Is(err, feishu.ErrSkillReadInvalid)`：模型输入可修正，返回 `code=invalid_skill_input`、`recoverable=true`、`retryable=false`。固定文案只提示重新选择 `skill/reference`，不回显输入。
- `ErrSkillReadFailed`、nil page、进程/协议故障：返回 `skill_read_unavailable`、`recoverable=false`。

自动续页开始后，cursor 已由可信平台生成；第二页的 invalid、内容漂移、循环或超限均属于内部读取故障，必须不可恢复，避免诱导 LLM 反复试错。

现有前端已经把 recoverable 工具结果显示为中性“正在调整执行方式”，所以不需要前端改动。真实故障继续显示红色错误。

## 6. 组件与文件

### `internal/numind/biz/feishu/skill_reader.go`

- 新增窄 `cursor`→`reference` 兼容规范化。
- 不修改 allowed skills、reference 解析、token 签名、CLI runner 或资源白名单。

### `internal/numind/biz/agent/tool_lark_skill_read.go`

- 隐藏模型 schema/output cursor。
- 保留旧 JSON 字段解码。
- 有界聚合最多两页并严格验证页间元数据。
- 区分可恢复输入错误与真实读取失败。

### `internal/numind/biz/agent/runner_v2_artifact.go`

- 复用现有 64 KiB 最终信封上限并更新注释；wrapper 仍做独立 fail-closed 校验。

不改 `numind-web-v3`。如果后端回归证明 recoverable 已按现有 SSE 适配器输出，前端现有单测/Playwright 结论继续有效。

## 7. 安全不变量

- 五技能枚举不变。
- 参考文件必须属于当前技能主页声明集合，简称只允许唯一命中。
- 不接受绝对路径、目录穿越、反斜杠、NUL、Unicode 同形字、歧义 basename 或跨技能引用。
- 不读取服务器 OS 文件，不执行 shell，不接受 user_id、HOME、token、App ID、App Secret。
- cursor 仍由 HMAC、run ID、skill、canonical reference、digest、offset、TTL 绑定。
- 进程并发上限、CLI 超时、stdout 上限、reference 数量/总字节上限不变。
- 任何聚合异常不返回部分说明，避免 LLM 基于缺页内容生成危险或错误命令。

## 8. 测试策略

客户 bug 的第一个代码 commit 必须是失败回归测试，固定 Dev run 227 的两种真实调用：

- `lark-drive` + `cursor="references/lark-drive-search.md"`
- `lark-doc` + `cursor="references/lark-doc-fetch.md"`

随后覆盖：

- 标准路径和 basename 放错字段均只在当前技能声明集内成功。
- 合法真实 cursor 不被误判；错 run/skill/reference、篡改、过期仍拒绝。
- undeclared、ambiguous、跨技能、遍历、绝对路径、反斜杠、NUL、Unicode、过长值拒绝，且不读取目标参考资源。
- schema/output 不含 cursor/receipt，但旧输入仍接受。
- 两页自动拼接、canonical reference 固定、页间 metadata 检查、重复 cursor、第三页、第二页 invalid、最终 JSON 超限全部有界且不泄露部分正文。
- 首次 invalid 为 recoverable；真实 failed 和续页 invalid 不可恢复。
- focused、`go test ./...`、`task lint`、Agent/Feishu race、双重独立审查通过。

## 9. 验收映射

部署后使用新的 Agent 对话读取一个只给标题的飞书文档。预期 LLM 可读取 `lark-drive` 搜索说明和 `lark-doc` fetch 说明，完成 Drive 搜索和 Docs 读取；时间线不出现由 reference/cursor 放错引起的红色“执行出错”。任何真实连接、授权或资源权限问题仍按原授权卡片/终止错误流程处理。
