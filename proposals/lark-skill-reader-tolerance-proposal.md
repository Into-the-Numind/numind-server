# 飞书技能读取容错与平台分页 — 提案

## §1 方案概述 [客户可见]
继续让 LLM 理解用户意图、选择官方飞书技能和参考说明，再决定 Docs、Base、Wiki、Drive 操作。平台只接管没有业务智能的机械工作：技能说明自动读完、旧 cursor 不再展示给 LLM、明确放错字段的参考文件自动归位、可修正的无效输入标记为“正在调整”而不是红色失败。

这不是让平台替 Agent 做决定，也不是取消安全校验。每个参考文件在读取前仍必须属于当前技能自己声明的白名单；任意路径、跨技能引用和伪造 cursor 仍拒绝。

## §2 报价与周期 [客户可见]
- 预估工作量：快速 Standard，一个开发周期
- 报价：内部产品开发，不单独报价
- 交付时间线：2026-07-19 部署 Dev 验收

## §3 技术可行性 [AI 内部]
### 现有功能复用
- `larkSkillReadTool` 已是唯一模型可见的受控技能读取入口，适合隐藏 cursor 和统一错误语义。
- `SkillReader.Read` 已绑定 run、skill、reference、digest、TTL，并且只通过固定 `lark-cli skills read` 读取五个官方嵌入技能。
- `resolveSkillReference` 已保证标准路径和安全 basename 只在当前技能声明集内唯一解析。
- `boundedAtomicSkillTool` 已对最终模型可见 JSON 信封设置 64 KiB 硬上限。
- 前端已把 `recoverable:true` 的工具结果渲染成中性进度，因此本期无需前端生产改动。

### 方案比较
#### A. 只纠正一次字段放错（最小方案）
- 做法：发现 `cursor` 看起来像参考文件时移到 `reference`，其他协议不变。
- 优点：改动最少。
- 缺点：LLM 仍能看到并复制分页 cursor；非法引用仍被误报成暂时服务故障；同类体验问题会继续出现。

#### B. 保留 LLM 判断，平台接管技能读取机械细节（采用）
- 做法：新 schema 只展示 `skill/reference`；服务端继续接受旧 cursor 以兼容在途会话；明确的 reference/cursor 放错自动纠正；平台按受签名 cursor 有界续读并一次返回完整说明；模型输入错误映射为 recoverable，真实读取故障仍终止。
- 优点：解决本次根因和同类分页问题；不改变 Agent 的业务判断；安全边界不变。
- 缺点：需要同时锁定兼容、分页上限和错误分类，测试面比 A 大。

#### C. 平台替 LLM 选择技能和操作
- 做法：服务端根据用户自然语言固定路由技能和命令。
- 优点：简单请求更确定。
- 缺点：平台本身没有 LLM 的语义判断能力，会复制一套脆弱规则，降低自定义 Agent 的开放性；与客户明确选择冲突，不采用。

### 技术风险
- `cursor` 是签名 opaque token，不能把任意字符串当参考。自动纠正只在 `reference` 为空且旧 cursor 具有官方 Markdown 参考形状时触发，最终仍交给 `SkillReader` 的当前技能声明集验证。
- 内部分页必须有页数、重复 cursor 和最终 JSON 信封三重上限；越界返回固定真实故障，禁止截断后假装完整。
- 旧对话可能已经持有有效 cursor。后端解码继续接受并验证，模型新 schema 不再生成新 cursor 调用。
- `ErrSkillReadInvalid` 只代表模型可修正输入，映射 recoverable；进程失败、资源漂移、循环 cursor、信封超限仍不可恢复。

### 涉及仓库
- [x] numind-server
- [ ] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性（如功能涉及 LLM 调用）
- [x] 涉及 LLM 调用：沿用现有 Agent run，不新增调用
- Trace 起点：沿用 Agent run trace
- Generation 点：无新增 generation
- 关键元数据：沿用 agent_run_id/tool_call_id；禁止记录 cursor、路径输入、技能正文和飞书内容

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]
### 用户故事
- 作为任意自定义 Agent 的使用者，我需要 LLM 继续自主判断飞书操作，同时不会因内部 cursor/reference 字段抄错看到多次红色失败。
- 作为平台，我需要自动处理确定性分页和兼容纠错，但仍对任意路径、跨技能引用、伪造 cursor 和过大输出 fail closed。

### 验收标准
- [ ] 新 `lark_skill_read` schema 只向模型展示 `skill` 和 `reference`，输出不含 cursor。
- [ ] 旧调用中的有效签名 cursor 仍可读取；新调用无需管理 cursor。
- [ ] `reference` 为空且 `cursor` 为 `references/...md` 或安全 `.md` basename 时，平台按 reference 处理并成功读取当前技能已声明资源。
- [ ] undeclared、ambiguous、跨技能、路径穿越和伪造值仍在参考资源读取前拒绝。
- [ ] 多页资源由平台自动续读；页数、重复 cursor、内容和最终 JSON 信封均有硬上限，绝不静默截断。
- [ ] 首次请求的 `ErrSkillReadInvalid` 返回 `invalid_skill_input`、`recoverable:true`；真实依赖/协议故障仍返回不可恢复 `skill_read_unavailable`。
- [ ] 现有前端无需修改即可把 recoverable 尝试显示为处理中；终止错误继续显示红色。
- [ ] 客户复现测试先红后绿，后端全测、lint、相关 race 与双重独立审查通过。

### 边界情况
- 同时提供 reference 和 cursor：按旧分页协议严格验证，不自动覆盖。
- cursor 是安全形状但当前技能未声明：recoverable 参数错误，不访问目标参考资源。
- 自动续页中 cursor 重复、元数据变化、内容漂移或返回 invalid：按内部真实故障终止。
- 聚合后信封超过 64 KiB：拒绝完整结果，不截断、不写普通 artifact、不暴露内部 cursor。

### 权限规则
- 只有五个现有官方技能可读。
- reference 必须由当前技能主页声明并唯一解析；不跨技能、不碰 OS 文件系统。
- run ID、HMAC cursor、digest、TTL、进程并发和 CLI 固定版本约束保持不变。

### UI 行为规格
- 页面位置：Agent 对话执行时间线。
- 布局要求：不改现有页面或组件。
- 交互模式：自动纠正成功时只出现正常“读取飞书技能/完成”；可恢复无效输入沿用“正在调整执行方式”。
- 状态处理：success=完成；recoverable=处理中；真实读取故障=红色终止错误。
