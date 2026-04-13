# SOP 运行页 Vue 3 完整重写

## 来源
- 提出人：产品负责人 / 创始人
- 提出日期：2026-04-10

## 需求描述

### 触发问题
B 端使用 self-service-config 配置器创建的 SOP，在 C 端实际运行时与配置严重不一致：

1. **步骤指示器与配置不一致**
   - 步骤数量不匹配（配置器配置 N 步，运行页显示固定 M 步）
   - 每个步骤显示的名称与配置器中填写的不一致

2. **步骤标题与描述与配置不一致**
   - 配置器中输入的 step name / description 在运行页未生效
   - 这是配置契约被破坏，属于核心可信度问题

3. **侧边栏存在硬编码"绿色卡片"**
   - 写死显示 "运行次数 156" 与 "65% 进度条"
   - 与当前 SOP 主题完全无关，是早期模板遗留

### 根因（已通过代码调查确认）
SOP 运行页 `numind-web-v3/src/views/SOPView.vue` 是个空壳，真正的页面逻辑在
`public/legacy/sop-legacy.js`（**7518 行 vanilla JavaScript**）+ 配套的
`sop-legacy.css`。关键问题：

- legacy JS 内有 `TEMPLATE_CONFIGS` 硬编码模板（templateId=1, 2 走硬编码分支，B 端
  自助配置走另一条动态分支）—— **双渲染路径并存**
- 即便走动态分支，字段映射也存在断点，配置器中的 name/description 未被消费
- 侧边栏绿色卡片是 CSS + HTML 完全硬编码，不读任何后端数据

这些问题不是单纯的数据绑定 bug，而是 **legacy JS 架构债务的表层症状**。任何"修补式"
方案都只能暂缓症状，无法根除双渲染路径与硬编码模板的耦合。

### 解决方向（已与决策人确认）
**完整重写**：将 SOP 运行页从 7518 行 legacy vanilla JS 重写为 Vue 3 Composition
API + TypeScript 组件，所有渲染数据由后端 API 驱动，删除一切硬编码模板与硬编码 UI 元素。

- **方向 (B)**：完整 Vue 重写，而非外科手术式部分迁移
- **绿色卡片处理 (C)**：直接删除，不替换为其他数据展示

## 业务目标

1. **修复配置契约**：让 self-service-config 真正可用 —— B 端配置什么，C 端就显示什么
2. **消除技术债务**：retire 7518 行无类型、无组件化、无测试的 legacy JS
3. **建立可维护基线**：未来 SOP 运行页的所有功能扩展（多模态输入、新步骤类型、协作等）
   都能在 Vue + TS 体系下安全演进
4. **降低回归风险**：从"改一行 legacy JS 心惊胆战"变为"组件级单测 + Playwright E2E
   守护"

## 优先级
**高**。这是 self-service-config 的可信度问题 —— B 端客户配置后看到运行页与配置不
一致，会立刻丧失对平台的信任。属于已上线功能的核心契约缺陷。

## Triage

- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：**否**（仅前端重写，可能补 1-2 个查询字段）
  2. 新增 API 端点：**可能**（需 S1/S2 评估当前 SOP runtime API 是否提供完整字段，
     如缺失则在 numind-server 补端点）
  3. 新外部服务集成：**否**
  4. 影响文件数：**远 > 3**（预计前端 15-25 文件，可能含后端 2-5 文件）
  5. 高风险业务逻辑（支付/权限）：**间接是** —— SOP 运行涉及 trial/standard/premium
     额度扣减、credit 计费、langfuse trace，重写时必须保持这些链路完整

- 人类决定：**确认 Standard 轨道，方向 = 完整重写**

## 范围（初步，待 S1 proposal 精化）

### 必须保留的功能
- 步骤切换 / 进度跟踪 / 上一步下一步导航
- AI 调用（LLM 流式输出、错误重试、超时处理）
- 配额扣减与计费（trial 总量、standard 月度、premium 无限）
- Langfuse trace 记录
- 历史记录 / 结果展示 / 分享 / 导出（如 legacy 中存在）
- 对早期硬编码模板（templateId=1, 2）的兼容运行 —— **迁移到 Vue 后这些模板必须照常可用**

### 必须删除的内容
- legacy `sop-legacy.js` 与 `sop-legacy.css`
- `TEMPLATE_CONFIGS` 硬编码模板分支
- 侧边栏"运行次数 156 / 65% 进度条"绿色卡片

### 显式不在范围内（避免 scope creep）
- SOP 配置器（self-service-config 已完成，本次不动）
- SOP 后端业务逻辑（除非发现字段链路断裂，须补 API）
- 视觉重设计（保持现有视觉风格 + DESIGN.md token，不做品牌升级）
- 新增 SOP 运行功能（仅做"等价重写 + 删除硬编码"）

## 风险与开放问题（待 S1 解决）

1. **legacy 中"沉默功能"清单**：7518 行 JS 必须先做一次穷举式 feature inventory，
   否则迁移后必然丢功能。S1 必须产出此清单。
2. **早期模板兼容路径**：templateId=1, 2 是 admin 创建的硬编码模板，迁移后这些
   SOP 数据怎么承载？是迁移到 self-service-config 表，还是保留特殊兼容路径？
3. **后端 API 完整度**：当前 SOP runtime API 返回的字段是否足以覆盖 Vue 组件所需的
   全部数据？需要在 S2 spec 阶段穷举字段映射。
4. **回归测试策略**：如此规模的重写，S5 验收必须用 Playwright E2E 而非 gstack /qa
   一次性验证（已在 Rule 10 强制要求）。

## 备注

- 本功能与已完成的 `self-service-config` 强耦合：那个功能交付了"配置生成的能力"，
  本功能交付"配置真正生效的能力"。两者合起来才完成 B2B2C 自助闭环。
- 前置阅读：S2 spec 之前必须先做 legacy `sop-legacy.js` 的穷举调研（feature
  inventory + 字段链路追踪），否则 spec 无法精确。
- 决策追溯：本次 Triage 经历了一次修订 —— AI 初判 Hotfix → 用户挑战 → AI 调查代
  码 → 改判 Standard。决策人在了解"完整重写 vs 外科手术式迁移"两种选项后选择完整重写。
