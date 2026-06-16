# 视觉能力字段统一（vision-capability-unify）

## 来源
- 提出人：用户（2026-06-16 设计讨论）
- 触发：chatbot-image-recognition 上线 dev 后，用户发现 admin 勾「输入模态=image」不生效，深挖出"能不能看图"被表示成 4 个字段、互不对齐的设计缺陷。

## 需求描述（用户原话精炼）
> "为什么要保留三个开关？这才是根本的问题，是不是三个开关都是必要的。" → "standard，治本。"

把"模型能不能看图"从 4 个互相冗余/失效的字段，**塌成单一真相源 = `input_modalities` 含 `image`**。

## 业务目标
让 admin 在能力配置里勾「输入模态 image」就**真的生效**（模型能看图），消除"管理员能调的全是死开关、唯一活的开关管理员碰不到"的设计缺陷。chatbot 与 agent 用同一套口径。

## 现状取证（4 个字段，本 session 已查实）
| 字段 | 现状 |
|------|------|
| `capabilities` 含 `vision` | **死值**：全 28 个 task_profile 无一要求 `capability=vision`（capability 只被 rerank/embedding 用）|
| `features.vision` | **冗余**：识图任务(sop.vision/salesrag.profile/chatstyle/attachment.vision_describe) requirements 同时要 `features:[vision]` + `input_modalities:[text,image]`，两遍表达同一事 |
| `input_modalities` 含 `image` | 该留的标准写法（"接受什么输入"），admin 已能勾 |
| `accepts_image_inline` | **过度设计的重复**：真正控制发图（chatbot `biz/multimodal` + agent 都只读它），但 admin 表单没有它、只能 DB 改；且无任何模型用到"接受图但非内联"的区分 |

根因：同一个概念被两套能力表示各读各的 key——路由层(profile.ServiceCapability: input_modalities/capabilities/features) vs 发图层(capability.Capabilities: accepts_image_inline)，从未对齐。agent 附件功能(V1.5)引入 accepts_image_inline 时没并入既有 input_modalities，chatbot 沿用后再次暴露。

## 优先级
中高（设计债，影响 admin 可运营性 + 用户已踩坑）

## Triage
- 推荐轨道：**Standard**（用户已确认）
- 分类理由：
  1. 数据库 schema 变更：**是**（task_profile requirements + 模型 capability_json 数据清洗 migration）
  2. 新增 API 端点：否
  3. 新外部服务集成：否
  4. 影响文件数：>3（capability 投影 + schema + migration + 测试，且改动是**全任务路由都经过的共享层**）
  5. 高风险业务逻辑：**是**（改的是所有 LLM 调用的能力判断/路由共享层 + agent fallback 决策）→ 高 blast radius
- 人类决定：**确认 Standard，治本**（2026-06-16）

## 拟定方案（S2 细化，4 点）
1. **发图判断改读 `input_modalities`**：`capability.Capabilities.AcceptsImageInline` 投影从 `input_modalities` 含 `image` 派生，退役直接读 `accepts_image_inline`。→ admin 勾 image 立即对 chatbot+agent 生效。
2. **识图任务 requirements 去 `features:[vision]`**：sop.vision / salesrag.profile / salesrag.chatstyle / attachment.vision_describe 只留 `input_modalities:[text,image]`（不再要求两遍）。migration 改 task_profile。
3. **capabilities enum 删 `vision`**（`capability_schema.go` EnumValues）+ 历史数据清掉 capabilities 里的 vision。
4. **数据迁移先把真相搬进 input_modalities**：凡 `accepts_image_inline=true` OR `features.vision=true` OR `capabilities含vision` 的模型，确保 `input_modalities` 含 image（再退役其余字段，避免漏判）。admin 表单 schema 驱动，改后端 schema/Description 自动反映，无需改 admin-web 代码。

## 边界 / 不做
- **仅 image/vision**：accepts_pdf_inline / accepts_audio_inline 暂不动（pdf/audio 是独立概念，且 input_modalities enum 无 pdf）；本次只统一"看图"。
- max_inline_size_bytes / preferred_image_format 等内联细化字段：S2 评估是否仍被读，被读则保留为可选细化、不作为"能否看图"主开关。
- dev 已手动给若干模型设了 accepts_image_inline=TRUE（止血），本 feature 上线后这些应被 input_modalities 口径取代（migration 兜底保证不回退）。

## 风险
- 改的是**所有任务路由 + agent fallback 都经过的共享能力层**，回归面大 → S4 必须有 capability 投影单测 + matchLLM 路由单测 + chatbot/agent 视觉路由回归。
- migration 数据清洗前必须审计：确保所有真·视觉模型都有 input_modalities=image，否则退役旧字段会误判某些模型"不能看图"。
