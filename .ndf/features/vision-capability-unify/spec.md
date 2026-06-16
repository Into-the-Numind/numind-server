# 视觉能力字段统一 — S1 提案 + S2 技术设计

> feature `vision-capability-unify` · Standard · 2026-06-16
> 目标：把"模型能不能看图"塌成单一真相源 = `input_modalities` 含 `image`。

## §1 提案（为什么）

**问题**：同一个概念"模型能不能看图"被表示成 4 个字段，互不对齐：
- `capabilities` 含 vision —— **死值**（0 个 task_profile 要求 capability=vision）
- `features.vision` + `input_modalities` 含 image —— **冗余对**（识图任务两遍表达同一事）
- `accepts_image_inline` —— **唯一真正控发图**（chatbot+agent 只读它），但 **admin 表单没有它**、只能 DB 改

根因：路由层（`profile.ServiceCapability`: input_modalities/capabilities/features）与发图层（`capability.Capabilities`: accepts_image_inline）两套能力表示，从同一个 `capability_json` 各读各的 key，从未对齐。

**目标**：admin 勾「输入模态 image」就生效；chatbot/agent 同一口径；删冗余/死字段。

**复用现状利好**：发图判断（chatbot `biz/multimodal` + agent `biz/agent/multimodal`）都经 `capability.GetCapabilities()` 读 `AcceptsImageInline`，所以**只改 `GetCapabilities` 的投影一处，下游全自动统一，零改动**。

## §2 技术设计（怎么改）

### 2.1 核心：发图判断改读 input_modalities（`capability/api.go`）
`lookupCapabilities`（api.go:148）当前把 capability_json 反序列化进 `Capabilities`（读 `accepts_image_inline`）。改为**同时读 `input_modalities`，令 `AcceptsImageInline = input_modalities 含 "image"`**，退役直接读 `accepts_image_inline`。

```go
// 反序列化时额外取 input_modalities：
type capsWire struct {
    Capabilities                       // 复用现有字段
    InputModalities []string `json:"input_modalities"`
}
var wire capsWire
json.Unmarshal(jsonStr, &wire)
caps := wire.Capabilities
// 单一真相源：input_modalities 含 image ⟺ 能内联看图
caps.AcceptsImageInline = containsFold(wire.InputModalities, "image")
```
- **语义变更（要锁进测试）**：显式 `accepts_image_inline` **不再被读**。一个模型若 `accepts_image_inline=true` 但 `input_modalities` 无 image → 新行为判**不能看图**（input_modalities 是唯一真相）。反之 input_modalities 含 image → 能看图，无论有没有 accepts_image_inline。
- `defaultConservative`（无 capability_json / 解析失败 / 未知模型）仍 = 不能看图。
- **范围**：仅 image。`AcceptsPDFInline`/`AcceptsAudioInline` 暂不动（pdf/audio 独立概念，input_modalities enum 无 pdf；本次只统一看图）。
- **死字段标注**：`MaxInlineSizeBytes`/`SupportsVisionToolCalling`/`PreferredImageFormat` 经查全代码无业务消费者 → 注释标 vestigial（本次不删，避免扩大改动面；留 follow-up）。
- **pdf/audio 明确不变**：`capsWire` embed `Capabilities`，`AcceptsPDFInline`/`AcceptsAudioInline` 仍直接从 `accepts_pdf_inline`/`accepts_audio_inline` 反序列化（不派生），与现有行为一致、无副作用——只有 image 改派生。

### 2.2 schema 清理（`profile/capability_schema.go`）
- `capabilities` 字段 EnumValues 去掉 `"vision"`（line 68，死值）→ admin 表单自动不再渲染该勾选（schema 驱动）。
- `input_modalities` 字段 Description 改为点明：「勾选 image = 允许该模型看图（这是唯一生效的看图开关）」。
- `features` 字段 Description 去掉 `"vision":true` 示例（vision 不再是 feature 信号）。
- **admin-web 零代码改动**（表单按 schema 渲染 + 用 Description 文案）。

### 2.3 数据迁移（新 migration，幂等 JSON 守卫）—— **S2 review 修正**

**前提澄清（review P0/P1，已核实真实数据）**：ai_service.capability_json 里的视觉信号**跨模型分布不均**——
- agnes 有 `features.vision:true` + `capabilities:["chat","vision"]` + `input_modalities:[text,image]`（无 accepts_image_inline）
- 20260524 seed 的 qwen-vl 系只有 `accepts_image_inline=TRUE`（**input_modalities 可能没写**，prod 尤甚）
- dev 我手设的 claude/gpt 只有 `accepts_image_inline=TRUE`

所以**没有单一信号能覆盖所有视觉模型**——必须把三种信号 OR 起来回填，**`accepts_image_inline=true` 这条尤其不能丢**（否则 seed 的 qwen-vl + 我手设的模型会漏判、退役后变"不能看图"）。

按**安全顺序**执行：

- **(a) 回填 input_modalities（关键，三信号 OR，缺一不可）**：
  ```sql
  UPDATE ai_service
  SET capability_json = JSON_SET(IFNULL(capability_json,'{}'), '$.input_modalities',
        JSON_ARRAY('text','image'))
  WHERE deprecated_at IS NULL
    AND (  JSON_EXTRACT(capability_json,'$.accepts_image_inline') = TRUE
        OR JSON_EXTRACT(capability_json,'$.features.vision')      = TRUE
        OR JSON_CONTAINS(capability_json->'$.capabilities','"vision"') = 1 )
    AND ( capability_json->'$.input_modalities' IS NULL
        OR JSON_CONTAINS(capability_json->'$.input_modalities','"image"') = 0 );
  ```
  （已有 input_modalities 但没 image 的，JSON_ARRAY 覆盖会丢掉原有 text 外的模态——故对"已有 input_modalities 非空"的行改用 JSON_ARRAY_APPEND 追加 image；S4 按数据实测拆两条 UPDATE 或用 JSON_MERGE。保证 text 不丢。）

- **(b) task_profile requirements 去 features 里的 vision**：仅对**确实含**该值的任务，防御性 WHERE 避免 no-op 歧义：
  ```sql
  UPDATE task_profile
  SET requirements = JSON_REMOVE(requirements, JSON_UNQUOTE(JSON_SEARCH(requirements,'one','vision','$','$.features')))
  WHERE JSON_CONTAINS(requirements->'$.features','"vision"') = 1;
  ```
  实测含 `features:["...","vision"]` 的只有 `sop.vision`/`salesrag.profile`/`salesrag.chatstyle`（保留 streaming 等）。**`attachment.vision_describe` 的 requirements 是空对象**（走 default_service_id 直连，不过 matchLLM）→ 无需改、上面 WHERE 自动跳过。
  - matchLLM 影响：ai_service 侧视觉模型大多**没有** `features.vision=true`（除 agnes），所以识图任务的 features.vision 校验**当前本就是 dead check**（供给侧无此 key，靠 input_modalities + 直连绕过）。去掉它 = 清死条件，**不改路由行为**（既有匹配不变、非视觉模型仍被 input_modalities 拦住）。

- **(c) 数据清理（回填+验收后再做）**：删模型 capability_json 里 `capabilities` 数组的 `vision`（否则 schema enum 删 vision 后 admin 表单把它显示成不可取消的 unknown chip）；可选删 `features.vision`/`accepts_image_inline`（代码已不读，纯数据卫生，留着无害）。

- **⚠ 缓存（review P1）**：capability 有 5min 进程内缓存。admin 改走 API 会 `InvalidateCache`，但 **migration 直改 DB 不触发失效**。故**执行 migration 后必须重启服务**（`docker restart`）清所有实例缓存，否则 5min 内仍服务旧路径。
- **执行步骤顺序**：跑 (a)+(b) → **重启服务清缓存** → dev 验收 → 跑 (c) 清理旧字段数据 → schema enum 变更随代码部署（§2.2）。
- **手工执行**：dev/prod 不自动跑 migration（[[project_dev_deploy_migration_gap]]），部署前 SSH 执行。

### 2.4 不改的地方
- chatbot `biz/multimodal`、agent `biz/agent/multimodal`、salesrag —— 零代码改动（都经 GetCapabilities 读 AcceptsImageInline，投影改了自动统一）。
- matchLLM 逻辑本身不改（只改它读的 task_profile **数据**）。

## §3 验证策略（S5）
- **Go 单测（持久回归）**：
  - capability 投影：`input_modalities=[text,image]`→AcceptsImageInline=true；`[text]`→false；缺失/空→false；**`accepts_image_inline=true` 但 input_modalities 无 image → false（锁 SOT 语义）**；含 image 但无 accepts_image_inline → true。
  - matchLLM 回归：去 features.vision 后的识图任务 requirements + 视觉模型 → 仍匹配；非视觉模型不匹配。
  - chatbot/agent 视觉路由回归（已有 multimodal 单测覆盖 BuildUserParts，确认仍绿）。
- **dev 端到端**（部署后）：admin 给一个原本不能看图的模型勾 input_modalities=image（不碰任何隐藏字段）→ chatbot 选它上传图片 → 验真能识图（证明勾选生效）；反向取消勾选 → 验回到不能看图。
- **migration 验证**：执行 (a)(b) → **重启服务清 5min 缓存** → 核对所有真·视觉模型 input_modalities 含 image（且 text 没被覆盖丢失）；task_profile 识图任务 requirements 已无 vision feature；抽查一个原 accepts_image_inline-only 的模型（如 seed 的 qwen-vl）确认 input_modalities 已回填、退役后仍判"能看图"（防 P0 回归）。

## §4 涉及文件预览（S3 细化）
- 改：`internal/pkg/aiservice/capability/api.go`（投影派生）+ `_test.go`
- 改：`internal/pkg/aiservice/profile/capability_schema.go`（enum/description）+ `_test.go`
- 新增：`migrations/20260616_HHMMSS_vision_capability_unify.sql`（回填 + task_profile + 清理）
- 不改：chatbot/agent/salesrag 业务代码、matchLLM 逻辑、admin-web
