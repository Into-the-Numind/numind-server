# R0 产品形态对齐会 — 议题清单

> 目的：在任何界面走查之前，先对齐 agent mode 的产品定位/形态（用户确认这是出入最大的层面）。
> 形式：纯对话，不碰环境。每个议题 = 现状摘要 + 要拍板的问题。
> 产出：`design-baseline.md` 第一章（北极星）+ v1 试点 in/out 清单。**out 的功能相关 bug 自动不修。**

---

## 现状形态摘要（AI 已核实，2026-06-10）

当前实现的 agent mode 是这样一个东西：

- **定位**：与 SOP、Chatbot 并列的"第三模态"，类 Claude-Code 的自主多轮工具调用 agent（Eino ReAct，MaxStep 120）
- **三种角色**：父账户在 web-v3 `/config/agents` **配置** agent（给自己的子账户用）；子账户在 `/agent/chat` **使用**；admin 端只有**监控**（运行列表/强制取消/Langfuse 跳转）
- **配置方式**：四条路——8 题问卷 / 模板库（预填 6 道隐藏题）/ from-scratch（当前 422 死路，修复待合并）/ 高级模式（只读占位，prompt 编辑"即将上线"）
- **Skill 体系**：独立顶级菜单 `/config/skills`——skill=纯指引文档（不再 gate 工具），v2 独立表+agent 绑定+版本历史回滚+**marketplace**（脱敏发布/订阅克隆）；另有 4 个平台磁盘 skill（xlsx/docx/pptx/pdf 生成配方）
- **工具**：全开模型（Codex 式，2026-05-31 拍板）——每个 agent 默认可用全部 ~20 个工具（搜索/网页/知识库/读文件/生成文件图表/图像生成/Python 沙箱/bash 沙箱/记忆读写/向用户提问……），按任务自主选用
- **计费**：子账户跑 agent 扣**子账户自己**的积分池（Reserve/Reconcile 每次 LLM 调用），已接通并 dev 验证
- **记忆**：L1 会话记忆（per-agent，自动提取）+ L2 用户全局记忆（agent 主动读写）+ AGENT.md
- **安全**：合规三层（输入检查已接/输出检查未接）+ 权限管线（enforce 默认开）+ bash 语义校验 14 检查器 + Docker 沙箱（每次调用销毁）

---

## 议题（按依赖顺序）

### T1 定位与形态 ⭐ 最上游

现状：独立的第三模态，独立入口 `/agent/chat`，与 SOP/Chatbot 互不感知。

**拍板**：
1. 你设计中的 agent mode 到底是什么？给用户解决什么问题？（用一两句话说，我来记）
2. 与 SOP、Chatbot 的关系：并列三入口？还是融合（如 agent 能执行 SOP、Chatbot 升级为 agent）？还是替代关系？
3. "类 Claude-Code 自主多轮工具调用"这个核心范式本身对不对？

### T2 角色与关系模型

现状：父账户配置 → 子账户使用；父账户自己也能跑自己的 agent（试聊）；admin 只监控。

**拍板**：
1. "父配置→子使用"的 B2B2C 关系对吗？
2. 父账户本人是不是 agent 的日常使用者（还是只调试）？
3. 子账户要不要有任何配置能力（哪怕选 agent/开关记忆）？

### T3 入口与触发

现状：使用者进 `/agent/chat` → 看到可用 agent 卡片 → 选一个 → 开聊。每个 agent 是一个独立"人格"。

**拍板**：
1. 用户怎么"遇到"agent？独立页面选择 vs 嵌在某个工作流里？
2. "多个 agent 人格供选择"对吗？还是应该一个统一助手？

### T4 配置体验形态

现状：问卷（8 题）/ 模板 / from-scratch / 高级模式（空占位）四条路并存。

**拍板**：
1. 主路径是哪条？四条都保留进 v1 吗？
2. 高级模式（直接编辑 prompt）进不进 v1？（不进 → 占位页怎么处理）
3. 配置后的调试方式：当前"试聊"toast 是空承诺，你要的调试/预览体验是什么样？

### T5 Skill 体系角色

现状：skill=纯指引（已拍板）；但还有独立菜单、与 builder 断裂的信息架构、版本回滚、marketplace 整套。

**拍板**：
1. 配置者需要"显式管理 skill"吗？还是 skill 应该藏在 agent 配置里（甚至完全后台化）？
2. marketplace（跨租户发布/订阅）进 v1 试点吗？
3. 版本历史/回滚进 v1 吗？

### T6 能力面裁剪（v1 in/out 清单）⭐ 杠杆最大

现状全开：搜索/网页/KB/文件读/文件生成(xlsx/docx/pptx/pdf/csv/html/图表)/图像生成/Python/bash/记忆/提问。

**拍板**：逐项过——哪些是试点客户的核心价值，哪些砍出 v1（砍掉的相关 bug 全不修）：
- 文档生成四件套？图像生成？代码沙箱（bash/python）？
- L1/L2 记忆系统？
- ask_user_question 交互？
- 多模态附件（图/PDF/docx）？

### T7 计费与商业

现状：子账户扣自己积分池；父账户试聊疑似不记账（HW-6）；agent 积分消耗速率显著高于 SOP/Chatbot（多轮工具调用）。

**拍板**：
1. 子账户扣自己积分对吗？还是该扣父账户？
2. 试点期 agent 的计费策略：照常扣 / 试点期免费或折扣？
3. 父账户配置调试（试聊）记不记账？

### T8 遗留 go/no-go 决策（顺带清掉）

1. **persistent-sandbox-session**（持久沙箱，S2 设计完成等你拍板）：进 v1 / 推迟 / 砍
2. **agent-from-scratch-q6q7**（修复已完成待合并）：from-scratch 路径保留则立即合并
3. **agent-mode-e2e-rollout** 僵尸条目（3/38 停滞）：剩余 scope 并入本冲刺 Phase 3/4，原条目关闭？

---

## 会后产出清单

- [ ] design-baseline.md §1 北极星：定位/角色/入口/核心范式
- [ ] design-baseline.md §2 v1 in/out 清单（T4/T5/T6 结论）
- [ ] backlog 重分级：§4 待裁决条目中，因 out 而关闭的标 `post-launch`/`wontfix`
- [ ] R1 走查脚本定稿（按 T1-T4 结论改写）
- [ ] WK-1（from-scratch worktree）处置执行
