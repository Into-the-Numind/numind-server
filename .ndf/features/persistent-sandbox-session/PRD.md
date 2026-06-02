# PRD: persistent-sandbox-session

> 用户视角的功能描述与验收标准 · 2026-06-01 · （设计评估阶段，描述"若实施"的目标行为）

## 用户能感知到的变化

### 变化 1：AI 能做"超大"文档了
**现在**：一份文档大到"生成它的 Python 代码超过 AI 一次能写的量"时，AI 卡死——既不能分两次（沙箱无状态），也塞不进一次。
**实施后**：AI 可以"先搭框架→再续写→再加图表"分多步完成，每步的文件都还在，像人用电脑一样自然。

### 变化 2：AI 不再被"必须一次做完"束缚
**现在**：工具提示告诉 AI "整份文档一次生成、别重开文件"（创可贴）。
**实施后**：提示反转为"你可以分步搭建，文件在本次对话内一直保留"。AI 行为更自然，少了被无状态坑到再重试的浪费。

### 变化 3（用户无感，但很关键）：隔离与资源不退化
- 不同用户、不同对话的沙箱**绝对互不可见**（和现在一样，硬保证）。
- 容器不会因为持久化而无限堆积——空闲会自动回收，单容器寿命封顶。

## 验收标准（若实施）

| # | 标准 | 验证方式 | 门槛 |
|---|---|---|---|
| AC-1 | 同一对话内：run_python 第 1 次写文件 `/workdir/output/x.pptx`，第 2 次 `Presentation("/workdir/output/x.pptx")` 重开成功 | dev gstack /qa + 集成测试 | 成功重开 |
| AC-2 | run 达终态（completed/error/aborted）后，绑定容器在 N 秒内释放销毁 | 集成测试 + 日志 | ≤10s 释放 |
| AC-3 | 对话空闲超过 TTL（120s）后，停泊容器被 reaper 回收 | 集成测试（mock 时钟） | 回收 |
| AC-4 | 单容器存活超过最大上限（30min）强制销毁，无论活跃 | 单测（mock 时钟） | 强制销毁 |
| AC-5 | **容器绝不跨 run_id 共享**：两个不同 run 并发，各自拿到独立容器，A 写的文件 B 看不到 | 集成测试（隔离用例） | 100% 隔离 |
| AC-6 | feature flag **off** → 行为与当前无状态完全一致。**明确 oracle（S1 reviewer P1）**：flag=off 下连跑两次 run_python，第 1 次写 `/workdir/output/x.txt`，**断言第 2 次的新容器里该文件不存在**（否则回归测试可能空过） | 回归测试（显式负断言） | 第 2 次看不到第 1 次的文件 |
| AC-6b | 服务重启后，停泊容器按 §risk 选定策略处理（倾向：全驱逐，下次调用拿新容器，不报错） | 集成测试（重启模拟） | 不残留 orphan、run 不崩 |
| AC-7 | feature flag **on** 时 guidance 反转为"可分步搭建"；off 时保持"一次做完" | 单测断言 prompt 文本 | 随 flag 联动 |
| AC-8 | 池满（活跃对话 > 容器上限）时按降级策略处理，run **不直接失败** | 集成测试（压满池子） | 优雅降级 |
| AC-9 | 多租户威胁分析文档完成并通过安全评审（prod 启用前置） | S2 文档 + 人工评审 | 通过 |
| AC-10 | go test ./... + task lint 全过 | CI | 0 失败 |

## 不在范围（明确告知）

- ❌ **Subagent / 任务分解 / 多沙箱并行**（用户 Q2 另一半）——独立未来 initiative。本 feature 只保证 per-run 容器模型**未来可扩展**到 subagent，不实现 subagent 本身。
- ❌ **prod 启用沙箱**——本 feature 只让架构就绪 + 留 feature flag；prod 开关 + 容量定额 + 安全评审通过后另行决定。
- ❌ run_python/bash_exec 的功能扩展（新库、新格式、更长 timeout）。

## 用户故事样例

> **场景**：用户让 AI "做一份 80 页的年度报告 PPT，含 20 张图表"。
>
> **实施后流程**：
> 1. AI 调 read_skill(pptx-author) 拿指南
> 2. AI 调 run_python 建封面+目录+前 30 页 → 存 `/workdir/output/report.pptx`
> 3. AI 调 run_python 重开 report.pptx，续写 31-60 页 → 文件**还在**，成功
> 4. AI 调 run_python 重开，续写 61-80 页 + 嵌入图表 → 成功
> 5. AI 把最终 COS 链接给用户
>
> **当前失败模式**：第 3 步重开时文件没了（新容器），AI 卡死或被迫塞进一次（超 token 上限做不到）。

## 与现有工作的关系

- 本 feature 实施后，`run-python-stateless-guidance` hotfix 的"一次做完"guidance 在 flag on 时**反转**。两者是"创可贴 vs 根治"。
- 不改 read_skill / run_python / 4 个 SKILL.md 的**功能**，只改底下沙箱状态模型。
- prod `skills_enabled=false` 现状不变——本 feature 不碰 prod 开关。
