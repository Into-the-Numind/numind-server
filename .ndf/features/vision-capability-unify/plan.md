# 视觉能力字段统一 — 实施计划（S3）

> feature `vision-capability-unify` · 2026-06-16 · 蓝本 spec.md
> 仅 numind-server。code task = T1–T3，T4 验证策略。文件互不重叠，主 session 顺序实现 + 每 task 双 reviewer。

## 依赖图
```
T1(投影派生) ──┐  互不依赖（不同包/文件）
T2(schema)   ──┼─► T4(S5 验证策略, 文档)
T3(migration)──┘
```
T1/T2/T3 **代码实现**文件 disjoint（capability 包 / profile 包 / migrations/），可任意顺序实现。

### ⚠ 部署时序硬约束（S3 review P0）
**T3 migration (a)(b) 必须先于 T1 代码上线执行。** T1 一旦上线即停读 `accepts_image_inline`、改读 `input_modalities`；若此时 seed 的 qwen-vl / 我手设的模型（只有 accepts_image_inline 信号、input_modalities 无 image）还没被回填，它们会在 T1 容器启动瞬间判"不能看图"。
- **安全顺序（dev 与 prod 同）**：① 在**旧容器**的 DB 上跑 migration (a)(b)（旧代码读 accepts_image_inline，加 input_modalities 对它无影响，向后兼容）→ ② 部署 T1 新镜像（新容器启动即读已回填的 input_modalities，fresh cache 无 stale）→ ③ 验收 → ④ 跑 (c) 清理 → ⑤ schema enum 变更随代码已在镜像内。
- S6 部署脚本/步骤必须把"先 migration 后 deploy"写死。

---

## T1 — capability 投影从 input_modalities 派生 AcceptsImageInline（核心）
**涉及文件**：
- 改 `internal/pkg/aiservice/capability/api.go`（`lookupCapabilities`）
- 改 `internal/pkg/aiservice/capability/types.go`（vestigial 注释）
- 改/增 `internal/pkg/aiservice/capability/*_test.go`

**内容**：
- `lookupCapabilities`：反序列化改用内部 `capsWire struct { Capabilities; InputModalities []string \`json:"input_modalities"\` }`；解析后 `caps.AcceptsImageInline = containsFold(wire.InputModalities, "image")`（单一真相源；不再读显式 accepts_image_inline）。
- `AcceptsPDFInline`/`AcceptsAudioInline` 仍从各自 JSON 字段直接反序列化（embed 继承，不变）。
- `defaultConservative`（空/解析失败/未知模型）仍 AcceptsImageInline=false。
- 加 `containsFold([]string, string) bool` 私有 helper（大小写不敏感，防 "Image"）。
- `MaxInlineSizeBytes`/`SupportsVisionToolCalling`/`PreferredImageFormat` 注释标 `// vestigial: 无业务消费者，留待 follow-up 清理`。

**单测**（锁 SOT 语义）：
- `input_modalities=[text,image]` → AcceptsImageInline=true
- `input_modalities=[text]` → false
- `input_modalities` 缺失/空 → false
- **`accepts_image_inline=true` 但 input_modalities 无 image → false（退役显式字段，锁 P0 语义）**
- `input_modalities=[image]` 含 image 但无 accepts_image_inline → true
- 大小写：`input_modalities=[Image]` → true（containsFold）
- pdf/audio 不受影响：`accepts_pdf_inline=true` → AcceptsPDFInline 仍 true
- 走 in-mem sqlite 注入 capability.Init(testDB) + seed ai_service 行（或直接单测 lookupCapabilities 的解析逻辑——若 packageDB 依赖难测，抽 `parseCapsJSON(jsonStr) Capabilities` 纯函数测派生）。

**验收**：`go test ./internal/pkg/aiservice/capability/...` 绿；`task lint` 0。

---

## T2 — schema 清理 capabilities enum + descriptions（profile 包）
**涉及文件**：
- 改 `internal/pkg/aiservice/profile/capability_schema.go`（llmSchema）
- 改/增 `internal/pkg/aiservice/profile/capability_schema_json_test.go`（若断言 enum 值需同步）

**内容**：
- `capabilities` 字段 EnumValues 删 `"vision"` → `["chat","embedding","rerank"]`。
- `input_modalities` Description 改为：`输入模态列表；勾选 image = 允许该模型看图（唯一生效的看图开关）`。
- `features` Description 去掉 `"vision":true` 示例（vision 不再是 feature 信号）。

**单测（S3 review P1，必做非可选）**：
- **断言 `SchemaFor("llm")` 的 capabilities EnumValues 不含 `"vision"`、input_modalities 仍含 `"image"`**（锁住"vision 已删"防误加回）。capability_schema_json_test 若硬断言 enum 内容/字段数则同步更新。
- **matchLLM 回归（spec §3，必做）**：去掉 `features.vision` 后的识图 task requirements（如 `Requirements{InputModalities:["text","image"]}`，无 vision feature）+ 视觉模型 `ServiceCapability{InputModalities:["text","image"]}` → `Match` 仍 compatible；纯文本模型（InputModalities:["text"]）→ 不 compatible。证明"去 features.vision = 清死条件、不改路由行为"。

**验收**：`go test ./internal/pkg/aiservice/profile/...` 绿；`task lint` 0。admin-web 零改动（schema 驱动）。

---

## T3 — 数据迁移（回填 + task_profile + 清理）
**涉及文件**：新增 `migrations/20260616_HHMMSS_vision_capability_unify.sql`

**内容**（按 spec §2.3 安全顺序，幂等）：
- **(a) 回填 input_modalities**：三信号 OR（`accepts_image_inline=true` OR `features.vision=true` OR `capabilities` 含 vision）且 input_modalities 无 image 的模型 → 追加 image。
  - **保 text 不丢**：拆两条——input_modalities 为空/NULL 的 `JSON_SET` 成 `["text","image"]`；非空但无 image 的用 `JSON_ARRAY_APPEND(...,'$.input_modalities','image')`。
- **(b) task_profile 去 features 里 vision**：`JSON_REMOVE` 仅对 `JSON_CONTAINS(requirements->'$.features','"vision"')=1` 的行（sop.vision/salesrag.profile/salesrag.chatstyle；attachment.vision_describe 空对象自动跳过）。保留 streaming 等 + input_modalities。
- **(c) 清理**：删模型 capabilities 数组的 vision（`JSON_REMOVE` via JSON_SEARCH）；可选删 features.vision / accepts_image_inline（注释标可选）。
- 顶部注释写清**执行步骤**：跑(a)(b)→**重启服务清 5min 缓存**→dev 验收→跑(c)→schema 变更随代码部署。手工 SSH 执行（不自动跑）。

**验收**：SQL 语法自查（mysql 8 JSON 函数）；migration 顶部注释完整含重启步骤。S5 在 dev 实跑验证。

---

## T4 — S5 验证策略（rule 10，文档 task）
**验证方式**：Go 单测（持久回归，核心语义）+ dev 端到端浏览器（治本验收）。
**理由**：核心是能力判断逻辑（投影派生），Go 单测锁"input_modalities 含 image ⟺ 能看图"+pdf/audio 不变+SOT 语义（显式字段退役）。schema 改动靠 schema 单测。migration 是数据、靠 dev 实跑 + 核对。非 bug-from-customer（设计重构），无强制复现测试。改的是共享高风险层 → 关键语义必须 Go 单测覆盖（已在 T1）。
**S5 关键路径**（dev 部署后）：
1. 跑 migration (a)(b) → **重启 numind-server-dev** → 核对所有真·视觉模型 input_modalities 含 image（text 未丢）+ 识图任务 requirements 无 vision feature。
2. 抽查一个原 `accepts_image_inline`-only 的模型 → 退役后仍判"能看图"（防 P0 回归）。
3. **治本验收**：admin 给一个原本不能看图的文本模型**只勾 input_modalities=image**（不碰隐藏字段）→ 重启/等缓存 → chatbot 选它上传图片提问 → 验真识图；取消勾选 → 验回到不能看图。
4. 回归：原视觉模型（qwen3-vl-flash 等）chatbot 上传图仍正常；纯文本对话不变。
5. 跑 (c) 清理 → admin 表单 capabilities 不再显示 vision（含旧数据无 unknown chip）。
6. **下游单测仍绿（S3 review P2）**：`go test ./internal/numind/biz/multimodal/... ./internal/numind/biz/agent/...` 确认 T1 投影改动未破坏 chatbot/agent 既有 multimodal 路径。

> **执行时序（S3 review P0）**：S5/S6 严格按 dependency 区的"先 migration (a)(b) + 重启 → 再部署 T1 镜像"顺序，防视觉模型真空窗口。

---

## 并发 / 提醒
- 活跃 feature（nickname-edit/notif-ui-tweaks）不碰 capability/profile/migration → 无冲突。
- **migration 高敏感**：prod 数据迁移单独把 SQL+步骤交用户过目（用户已要求）；dev 我执行。
- Tier：T1/T2/T3 文件 disjoint，主 session 顺序实现（量小，不值 sub-worktree）+ 每 task 完成并行双 reviewer。
